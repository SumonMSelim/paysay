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

	// PayPal tools: live MCP when credentials exist, otherwise mock.
	tools, liveMode, err := buildToolProvider(ctx, cfg, logger)
	if err != nil {
		return err
	}
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
		Config:      cfg,
		Agent:       ag,
		Synthesizer: synth,
		Logger:      logger,
		WebFS:       web.FS(),
		LiveMode:    liveMode,
		Invoices:    server.NewInvoiceListerAdapter(invoiceShim{tools}),
	})

	logger.Info("starting paysay",
		"brand", cfg.Brand.Name,
		"model", cfg.Bedrock.ModelID,
		"region", cfg.AWS.Region,
		"paypal_live", liveMode,
		"locales", cfg.SupportedLocales,
	)

	return srv.Run(ctx)
}

// buildToolProvider returns a live MCP-backed PayPal provider when client
// credentials are configured, otherwise a mock provider. The boolean reports live mode.
func buildToolProvider(ctx context.Context, cfg *config.Config, logger *slog.Logger) (paypal.ToolProvider, bool, error) {
	if err := cfg.RequireLiveCredentials(); err != nil {
		if errors.Is(err, config.ErrMissingPayPalCredentials) {
			logger.Warn("PayPal client credentials not set: running with in-memory mock PayPal provider (no real invoices will be created)")
			return paypal.NewMockProvider(cfg.PayPal.MerchantEmail), false, nil
		}
		return nil, false, err
	}

	opts := []paypal.Option{paypal.WithAllowedTools(paypal.InvoiceTools...)}
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
	svc, err := paypal.NewRefreshingProvider(ctx, mint, launch)
	if err != nil {
		return nil, false, err
	}
	logger.Info("connected to PayPal MCP server", "environment", cfg.PayPal.Environment)
	return svc, true, nil
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
