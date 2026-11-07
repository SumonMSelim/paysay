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
	cfg      *config.Config
	agent    Agent
	synth    voice.Synthesizer
	log      *slog.Logger
	webFS    http.FileSystem
	liveMode bool
	invoices InvoiceLister

	// Separate budgets so speech playback never eats into chat turns.
	chatLimit  *rateLimiter
	speakLimit *rateLimiter

	sessions *sessionInvoices
	pending  *pendingActions
}

// Options configures a Server.
type Options struct {
	Config      *config.Config
	Agent       Agent
	Synthesizer voice.Synthesizer
	Logger      *slog.Logger
	// WebFS serves the static UI. May be nil to disable static serving.
	WebFS http.FileSystem
	// LiveMode indicates real PayPal credentials are in use (vs. mock), surfaced
	// to the UI so users know whether invoices are real sandbox invoices.
	LiveMode bool
	// Invoices provides invoice listing for the dashboard and runs actions the
	// merchant approved. Optional; when nil, the dashboard shows no rows and
	// approvals fail.
	Invoices InvoiceLister
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
	return &Server{
		cfg:      opts.Config,
		agent:    opts.Agent,
		synth:    synth,
		log:      log,
		webFS:    opts.WebFS,
		liveMode: opts.LiveMode,
		invoices: opts.Invoices,

		chatLimit:  newRateLimiter(opts.Config.Server.RateLimitPerMinute, opts.Config.Server.RateLimitPerHour),
		speakLimit: newRateLimiter(opts.Config.Server.RateLimitPerMinute, opts.Config.Server.RateLimitPerHour),

		sessions: newSessionInvoices(),
		pending:  newPendingActions(),
	}
}

// Handler builds the HTTP handler with all routes and middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/locales", s.handleLocales)
	mux.HandleFunc("POST /api/chat", s.chatLimit.limit(s.handleChat))
	mux.HandleFunc("POST /api/speak", s.speakLimit.limit(s.handleSpeak))
	mux.HandleFunc("POST /api/confirm", s.chatLimit.limit(s.handleConfirm))
	mux.HandleFunc("GET /api/invoices", s.handleInvoices)
	mux.HandleFunc("GET /api/invoices/{id}/qr", s.handleInvoiceQR)

	if s.webFS != nil {
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
