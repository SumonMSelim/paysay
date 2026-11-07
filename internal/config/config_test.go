package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Ensure a clean environment so defaults apply.
	t.Setenv("PAYSAY_BRAND_NAME", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Brand.Name != defaultBrandName {
		t.Errorf("brand name = %q, want %q", cfg.Brand.Name, defaultBrandName)
	}
	if cfg.PayPal.Environment != "SANDBOX" {
		t.Errorf("environment = %q, want SANDBOX", cfg.PayPal.Environment)
	}
	if cfg.Bedrock.ModelID != defaultModelID {
		t.Errorf("model id = %q, want %q", cfg.Bedrock.ModelID, defaultModelID)
	}
	if cfg.Bedrock.MaxTokens != defaultMaxTokens {
		t.Errorf("max tokens = %d, want %d", cfg.Bedrock.MaxTokens, defaultMaxTokens)
	}
	if cfg.Server.RateLimitPerMinute != defaultRatePerMin || cfg.Server.RateLimitPerHour != defaultRatePerHour {
		t.Errorf("rate limits = %d/min %d/h, want %d/min %d/h",
			cfg.Server.RateLimitPerMinute, cfg.Server.RateLimitPerHour, defaultRatePerMin, defaultRatePerHour)
	}
	if len(cfg.PayPal.MCPArgs) == 0 || cfg.PayPal.MCPArgs[1] != "@paypal/mcp" {
		t.Errorf("unexpected MCP args: %v", cfg.PayPal.MCPArgs)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("PAYSAY_BRAND_NAME", "TestPal")
	t.Setenv("PAYPAL_ENVIRONMENT", "sandbox") // lower-case should be normalized
	t.Setenv("BEDROCK_MODEL_ID", "us.amazon.nova-lite-v1:0")
	t.Setenv("BEDROCK_MAX_TOKENS", "1024")
	t.Setenv("BEDROCK_TEMPERATURE", "0.2")
	t.Setenv("SERVER_READ_TIMEOUT", "5s")
	t.Setenv("SERVER_ALLOWED_ORIGINS", "https://a.example, https://b.example")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Brand.Name != "TestPal" {
		t.Errorf("brand name = %q, want TestPal", cfg.Brand.Name)
	}
	if cfg.PayPal.Environment != "SANDBOX" {
		t.Errorf("environment = %q, want normalized SANDBOX", cfg.PayPal.Environment)
	}
	if cfg.Bedrock.ModelID != "us.amazon.nova-lite-v1:0" {
		t.Errorf("model id = %q", cfg.Bedrock.ModelID)
	}
	if cfg.Bedrock.Temperature == nil || *cfg.Bedrock.Temperature != 0.2 {
		t.Errorf("temperature not parsed: %v", cfg.Bedrock.Temperature)
	}
	if cfg.Server.ReadTimeout != 5*time.Second {
		t.Errorf("read timeout = %v, want 5s", cfg.Server.ReadTimeout)
	}
	if len(cfg.Server.AllowedOrigins) != 2 {
		t.Errorf("allowed origins = %v, want 2 entries", cfg.Server.AllowedOrigins)
	}
}

func TestValidateRejectsBadEnvironment(t *testing.T) {
	t.Setenv("PAYPAL_ENVIRONMENT", "STAGING")
	_, err := Load()
	if err == nil {
		t.Fatal("expected validation error for bad environment, got nil")
	}
}

func TestValidateRejectsUnsupportedDefaultLocale(t *testing.T) {
	t.Setenv("PAYSAY_DEFAULT_LOCALE", "fr-FR")
	t.Setenv("PAYSAY_SUPPORTED_LOCALES", "en-US,de-DE")
	_, err := Load()
	if err == nil {
		t.Fatal("expected validation error for unsupported default locale, got nil")
	}
}

func TestRequireLiveCredentials(t *testing.T) {
	t.Setenv("PAYPAL_CLIENT_ID", "")
	t.Setenv("PAYPAL_SECRET", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if err := cfg.RequireLiveCredentials(); err == nil {
		t.Error("expected ErrMissingPayPalCredentials when credentials absent")
	}

	t.Setenv("PAYPAL_CLIENT_ID", "sandbox-client")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if err := cfg.RequireLiveCredentials(); err == nil {
		t.Error("expected an error when only the client ID is set")
	}

	t.Setenv("PAYPAL_SECRET", "sandbox-secret")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if err := cfg.RequireLiveCredentials(); err != nil {
		t.Errorf("unexpected error with credentials present: %v", err)
	}
}

func TestValidateRejectsNegativeRateLimit(t *testing.T) {
	t.Setenv("RATE_LIMIT_PER_HOUR", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for negative rate limit")
	}
}
