// Package server exposes the PaySay agent over HTTP with a small, dependency-
// free handler set built on net/http. It serves the accessible web UI, a chat
// endpoint that drives the agent, a text-to-speech endpoint, a locale listing,
// and a health check.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/SumonMSelim/paysay/internal/agent"
	"github.com/SumonMSelim/paysay/internal/config"
	"github.com/SumonMSelim/paysay/internal/i18n"
	"github.com/SumonMSelim/paysay/internal/voice"
)

// Agent is the behavior the server needs from the conversational agent.
type Agent interface {
	Respond(ctx context.Context, history []agent.Turn, message string, locale i18n.Locale) (*agent.Result, error)
}

// Server holds dependencies and configuration for the HTTP layer.
type Server struct {
	cfg        *config.Config
	agent      Agent
	synth      voice.Synthesizer
	log        *slog.Logger
	webFS      http.FileSystem
	webHandler http.Handler
	liveMode   bool
	// gate holds the PayPal invoice provider and its readiness. In mock mode it
	// is ready immediately; in live mode it becomes ready once the background
	// connect succeeds. PayPal-backed routes return 503 paypal_connecting until
	// then.
	gate *providerGate

	// Separate budgets so speech playback never eats into chat turns.
	chatLimit  *rateLimiter
	speakLimit *rateLimiter
	// invoiceLimit caps the PayPal-backed read routes (list and QR) per client.
	// It shares the global hourly window with chat and speech.
	invoiceLimit *rateLimiter

	// invoiceCache memoizes the invoice list for a few seconds so refreshes and
	// multiple tabs do not each hit PayPal. Invalidated after a successful
	// create, send or remind.
	invoiceCache *invoiceCache

	sessions *sessionInvoices
	pending  *pendingActions

	collections *collectionsStore

	// approvalTools is the defence-in-depth allow-list of tool names that
	// handleConfirm is permitted to run. It is the same set the agent gates on
	// (paypal.ApprovalTools), wired in from main so the server package keeps no
	// dependency on paypal. When empty, no tool is allowed to run via confirm.
	approvalTools map[string]bool
}

// Options configures a Server.
type Options struct {
	Config      *config.Config
	Agent       Agent
	Synthesizer voice.Synthesizer
	Logger      *slog.Logger
	// WebFS serves the static UI. May be nil to disable static serving.
	WebFS http.FileSystem
	// WebHandler serves the static UI with caching (ETag/Cache-Control). When
	// set it takes precedence over a plain file server over WebFS. WebFS is
	// still used to probe for optional assets (the Bryntum bundle).
	WebHandler http.Handler
	// LiveMode indicates real PayPal credentials are in use (vs. mock), surfaced
	// to the UI so users know whether invoices are real sandbox invoices.
	LiveMode bool
	// Invoices provides invoice listing for the dashboard and runs actions the
	// merchant approved. Optional; when nil, the dashboard shows no rows and
	// approvals fail.
	Invoices InvoiceLister
	// PayPalConnecting indicates PayPal is being connected in the background
	// (live mode) and Invoices is not yet available. While true and before
	// SetInvoices is called, PayPal-backed routes return 503 paypal_connecting.
	// It is ignored when Invoices is non-nil.
	PayPalConnecting bool
	// ApprovalTools is the allow-list of tool names handleConfirm may run, as
	// defence in depth behind the agent's own gating. Wire in
	// paypal.ApprovalTools. When empty, confirm runs nothing.
	ApprovalTools []string
}

// New constructs a Server.
func New(opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	synth := opts.Synthesizer
	if synth == nil {
		synth = voice.NoopSynthesizer{}
	}
	approval := make(map[string]bool, len(opts.ApprovalTools))
	for _, n := range opts.ApprovalTools {
		approval[n] = true
	}
	// One shared hourly window so chat, speech and PayPal-backed reads all
	// count toward a single global per-hour cap.
	globalHour := newGlobalWindow(opts.Config.Server.RateLimitPerHour)
	return &Server{
		cfg:        opts.Config,
		agent:      opts.Agent,
		synth:      synth,
		log:        log,
		webFS:      opts.WebFS,
		webHandler: opts.WebHandler,
		liveMode:   opts.LiveMode,
		// The gate is ready at construction unless PayPal is connecting in the
		// background with no provider yet. A deliberate nil provider (no PayPal
		// configured) is still "ready": its routes keep their no-provider
		// behavior rather than reporting paypal_connecting.
		gate: newProviderGate(opts.Invoices, opts.Invoices != nil || !opts.PayPalConnecting),

		chatLimit:    newRateLimiterShared(opts.Config.Server.RateLimitPerMinute, globalHour),
		speakLimit:   newRateLimiterShared(opts.Config.Server.RateLimitPerMinute, globalHour),
		invoiceLimit: newRateLimiterShared(invoiceRateLimitPerMinute, globalHour),

		invoiceCache: newInvoiceCache(invoiceCacheTTL),

		sessions: newSessionInvoices(),
		pending:  newPendingActions(),

		collections: newCollectionsStore(),

		approvalTools: approval,
	}
}

// Handler builds the HTTP handler with all routes and middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/locales", s.handleLocales)
	mux.HandleFunc("POST /api/chat", s.requireOrigin(s.chatLimit.limit(s.handleChat)))
	mux.HandleFunc("POST /api/speak", s.requireOrigin(s.speakLimit.limit(s.handleSpeak)))
	mux.HandleFunc("POST /api/confirm", s.requireOrigin(s.chatLimit.limit(s.handleConfirm)))
	mux.HandleFunc("GET /api/invoices", s.invoiceLimit.limit(s.handleInvoices))
	mux.HandleFunc("GET /api/invoices/{id}/qr", s.invoiceLimit.limit(s.handleInvoiceQR))
	mux.HandleFunc("GET /api/collections", s.handleCollections)
	mux.HandleFunc("POST /api/collections/approve", s.requireOrigin(s.chatLimit.limit(s.handleCollectionsApprove)))
	mux.HandleFunc("POST /api/internal/collections", s.handleCollectionsInternal)

	if s.webHandler != nil {
		mux.Handle("GET /", s.webHandler)
	} else if s.webFS != nil {
		mux.Handle("GET /", http.FileServer(s.webFS))
	}

	// Middleware chain (outermost first): panic recovery, logging, security
	// headers, CORS, body size limit.
	var h http.Handler = mux
	h = s.withBodyLimit(h)
	h = s.withCORS(h)
	h = s.withSecurityHeaders(h)
	h = s.withLogging(h)
	h = s.withRecover(h)
	return h
}

// Run starts the HTTP server and blocks until ctx is cancelled, then performs a
// graceful shutdown bounded by the configured shutdown timeout.
func (s *Server) Run(ctx context.Context) error {
	// Periodically drop expired pending actions and collections claims, so the
	// in-memory stores do not accumulate stale entries on a long-lived process.
	go s.sweepLoop(ctx)

	srv := &http.Server{
		Addr:         s.cfg.Server.Addr,
		Handler:      s.Handler(),
		ReadTimeout:  s.cfg.Server.ReadTimeout,
		WriteTimeout: s.cfg.Server.WriteTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", "addr", s.cfg.Server.Addr, "live_mode", s.liveMode)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.Server.ShutdownTimeout)
		defer cancel()
		s.log.Info("shutting down http server")
		return srv.Shutdown(shutdownCtx)
	}
}

// now is overridable in tests.
var now = time.Now

// sweepInterval is how often the background sweeper drops expired state.
const sweepInterval = 10 * time.Minute

// sweepLoop periodically drops expired pending actions and collections claims
// until ctx is cancelled. The session cap is enforced inline on insert, so it
// needs no sweeping here.
func (s *Server) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t := now()
			s.pending.sweepExpired(t)
			s.collections.sweepExpired(t)
		}
	}
}

// SetInvoices records a connected PayPal provider and flips the readiness gate,
// so PayPal-backed routes stop returning 503 paypal_connecting. It is called by
// the background connect once the live provider is ready. Safe to call
// concurrently with request handling.
func (s *Server) SetInvoices(inv InvoiceLister) {
	s.gate.set(inv)
}
