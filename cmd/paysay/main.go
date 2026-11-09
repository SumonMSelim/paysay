// Command paysay is the PaySay server entrypoint. It wires configuration,
// the Bedrock-backed agent, the PayPal tool provider (live over MCP when
// sandbox client credentials are present, otherwise an in-memory mock so the app is always
// runnable), optional Polly text-to-speech, and the HTTP server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SumonMSelim/paysay/internal/agent"
	"github.com/SumonMSelim/paysay/internal/bedrock"
	"github.com/SumonMSelim/paysay/internal/config"
	"github.com/SumonMSelim/paysay/internal/paypal"
	"github.com/SumonMSelim/paysay/internal/server"
	"github.com/SumonMSelim/paysay/internal/voice"
	"github.com/SumonMSelim/paysay/internal/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Cancel the root context on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Bedrock agent brain.
	brclient, err := bedrock.New(ctx, cfg.AWS.Region)
	if err != nil {
		return err
	}

	// PayPal tools: live MCP when credentials exist, otherwise an in-memory
	// mock. In live mode this returns immediately with a not-yet-connected
	// deferred provider and a connect function; the real token mint, MCP launch
	// and handshake run in the background so the HTTP server starts at once.
	tools, liveMode, connecting, connect := buildToolProvider(ctx, cfg, logger)
	defer func() {
		if cerr := tools.Close(); cerr != nil {
			logger.Warn("closing tool provider", "error", cerr)
		}
	}()

	ag := agent.New(brclient, tools, agent.Options{
		ModelID:     cfg.Bedrock.ModelID,
		MaxTokens:   cfg.Bedrock.MaxTokens,
		Temperature: cfg.Bedrock.Temperature,
		BrandName:   cfg.Brand.Name,

		ApprovalRequired: paypal.ApprovalTools,
	})

	// Text-to-speech (optional).
	synth := buildSynthesizer(ctx, cfg, logger)

	srv := server.New(server.Options{
		Config:           cfg,
		Agent:            ag,
		Synthesizer:      synth,
		Logger:           logger,
		WebFS:            web.FS(),
		WebHandler:       web.Handler(),
		LiveMode:         liveMode,
		Invoices:         server.NewInvoiceListerAdapter(invoiceShim{tools}),
		PayPalConnecting: connecting,
		ApprovalTools:    paypal.ApprovalTools,
	})

	// Connect PayPal in the background (live mode only) so startup is not
	// blocked on it. On success the server's readiness gate flips and
	// PayPal-backed routes stop returning 503 paypal_connecting.
	if connect != nil {
		go connect(ctx, srv)
	}

	logger.Info("starting paysay",
		"brand", cfg.Brand.Name,
		"model", cfg.Bedrock.ModelID,
		"region", cfg.AWS.Region,
		"paypal_live", liveMode,
		"locales", cfg.SupportedLocales,
	)

	return srv.Run(ctx)
}

// buildToolProvider returns the PayPal tool provider. With no client
// credentials it returns an in-memory mock, already connected. With credentials
// it returns a not-yet-connected DeferredProvider plus a connect function that
// mints a token, launches the MCP server and performs the handshake in the
// background with capped-backoff retries; when connect succeeds it attaches the
// live provider to the deferred one and flips the server's readiness gate.
//
// Return values: the provider to wire into the agent and server, whether this
// is live mode, whether PayPal is still connecting (true only in live mode),
// and the connect function (nil when there is nothing to connect).
func buildToolProvider(ctx context.Context, cfg *config.Config, logger *slog.Logger) (paypal.ToolProvider, bool, bool, func(context.Context, *server.Server)) {
	if err := cfg.RequireLiveCredentials(); err != nil {
		if errors.Is(err, config.ErrMissingPayPalCredentials) {
			logger.Warn("PayPal client credentials not set: running with in-memory mock PayPal provider (no real invoices will be created)")
			return paypal.NewMockProvider(cfg.PayPal.MerchantEmail), false, false, nil
		}
		// Any other credential problem: keep serving with a deferred provider
		// that reports paypal_connecting, rather than refusing to boot.
		logger.Error("PayPal credential check failed; serving without PayPal", "error", err)
		return paypal.NewDeferredProvider(), true, true, nil
	}

	opts := []paypal.Option{paypal.WithAllowedTools(paypal.InvoiceTools...)}
	opts = append(opts, paypal.WithLogger(logger))
	if cfg.PayPal.MerchantEmail != "" {
		opts = append(opts, paypal.WithMerchantEmail(cfg.PayPal.MerchantEmail))
	}
	if cfg.PayPal.MerchantName != "" {
		opts = append(opts, paypal.WithMerchantName(cfg.PayPal.MerchantName))
	}
	// launch starts the MCP server on the root context so the subprocess
	// outlives any single request.
	launch := func(accessToken string) (paypal.ToolProvider, error) {
		return paypal.NewService(ctx, paypal.LaunchConfig{
			Command:     cfg.PayPal.MCPCommand,
			Args:        cfg.PayPal.MCPArgs,
			AccessToken: accessToken,
			Environment: cfg.PayPal.Environment,
		}, opts...)
	}

	// Mint tokens from the client credentials and refresh before expiry, so a
	// long-running deployment never runs on an expired token.
	mint := func(ctx context.Context) (*paypal.TokenResponse, error) {
		return paypal.FetchToken(ctx, cfg.PayPal.ClientID, cfg.PayPal.ClientSecret, cfg.PayPal.Environment)
	}

	deferred := paypal.NewDeferredProvider()
	connect := func(ctx context.Context, srv *server.Server) {
		connectPayPal(ctx, deferred, srv, mint, launch, cfg.PayPal.Environment, logger)
	}
	return deferred, true, true, connect
}

// paypalConnectMaxBackoff caps the retry backoff for the background PayPal
// connect so a long outage keeps retrying at a steady, bounded interval.
const paypalConnectMaxBackoff = 60 * time.Second

// connectPayPal connects the live PayPal provider in the background, retrying
// with exponential backoff capped at paypalConnectMaxBackoff. On success it
// attaches the provider to the deferred wrapper (so the agent's tools work) and
// flips the server's readiness gate (so PayPal-backed routes stop returning
// 503). Invalid credentials never crash the process: the error is logged and
// the loop keeps retrying, so a later credential fix recovers without a
// restart. It returns when ctx is cancelled (shutdown).
func connectPayPal(
	ctx context.Context,
	deferred *paypal.DeferredProvider,
	srv *server.Server,
	mint func(ctx context.Context) (*paypal.TokenResponse, error),
	launch func(accessToken string) (paypal.ToolProvider, error),
	environment string,
	logger *slog.Logger,
) {
	backoff := time.Second
	for {
		provider, err := paypal.NewRefreshingProvider(ctx, mint, launch)
		if err == nil {
			deferred.Set(provider)
			srv.SetInvoices(server.NewInvoiceListerAdapter(invoiceShim{provider}))
			logger.Info("connected to PayPal MCP server", "environment", environment)
			return
		}
		logger.Error("PayPal connect failed; will retry", "error", err, "retry_in", backoff.String())

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > paypalConnectMaxBackoff {
			backoff = paypalConnectMaxBackoff
		}
	}
}

// buildSynthesizer returns a Polly synthesizer when TTS is enabled, otherwise a
// no-op. A Polly construction failure degrades gracefully to no-op so the app
// still runs without speech output.
func buildSynthesizer(ctx context.Context, cfg *config.Config, logger *slog.Logger) voice.Synthesizer {
	if !cfg.Voice.TTSEnabled {
		return voice.NoopSynthesizer{}
	}
	p, err := voice.NewPolly(ctx, cfg.AWS.Region)
	if err != nil {
		logger.Warn("polly unavailable, continuing without text-to-speech", "error", err)
		return voice.NoopSynthesizer{}
	}
	return p
}

// invoiceShim adapts a paypal.ToolProvider to the server's primitive-returning
// invoice provider, so the server package needs no dependency on paypal.
type invoiceShim struct {
	p paypal.ToolProvider
}

func (s invoiceShim) Invoke(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	res, err := s.p.Invoke(ctx, name, args)
	return res.Text, res.IsError, err
}
