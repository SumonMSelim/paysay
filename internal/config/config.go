// Package config loads and validates PaySay runtime configuration from the
// environment. All tunable behavior (branding, model IDs, PayPal credentials,
// AWS region) is centralized here so the rest of the codebase depends on a
// single typed Config value rather than reading os.Getenv ad hoc.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Brand holds display/branding values. Kept in config so renaming the product
// is a one-line change rather than a code-wide search-and-replace.
type Brand struct {
	Name    string // product name, e.g. "PaySay"
	Tagline string
}

// PayPal holds the configuration needed to launch and authenticate the PayPal
// MCP server subprocess. AccessToken is a sandbox OAuth2 token; it is never
// logged.
type PayPal struct {
	AccessToken   string   // PAYPAL_ACCESS_TOKEN (sandbox bearer token)
	Environment   string   // SANDBOX or PRODUCTION
	MCPCommand    string   // command to launch the MCP server (default: npx)
	MCPArgs       []string // args for the MCP server launch
	MerchantEmail string   // sandbox business email used as the invoicer
}

// Bedrock holds Amazon Bedrock model selection and inference parameters. ModelID
// is intentionally a plain string (not an enum) so operators can switch between
// Nova Pro/Lite/Micro or an inference-profile ARN without a code change.
type Bedrock struct {
	ModelID   string
	MaxTokens int32
	// Temperature controls determinism of the agent. Lower is safer for a
	// money-moving agent. Pointer so "unset" is distinguishable from 0.
	Temperature *float32
}

// Voice holds speech-in/speech-out configuration. Speech is optional: when
// disabled, PaySay operates as a text chat agent, which keeps the demo
// runnable without microphone access.
type Voice struct {
	Enabled      bool
	TTSEnabled   bool
	SampleRateHz int32
}

// AWS holds shared AWS SDK configuration.
type AWS struct {
	Region string
}

// Server holds HTTP server configuration.
type Server struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
	// AllowedOrigins is the CORS allow-list for the browser demo UI.
	AllowedOrigins []string
}

// Config is the fully-resolved application configuration.
type Config struct {
	Brand            Brand
	PayPal           PayPal
	Bedrock          Bedrock
	Voice            Voice
	AWS              AWS
	Server           Server
	DefaultLocale    string
	SupportedLocales []string
}

// Defaults applied when the corresponding environment variable is unset.
const (
	defaultBrandName    = "PaySay"
	defaultBrandTagline = "Run your business by voice."
	defaultEnvironment  = "SANDBOX"
	defaultMCPCommand   = "npx"
	defaultModelID      = "us.amazon.nova-pro-v1:0"
	defaultMaxTokens    = 2048
	defaultRegion       = "us-east-1"
	defaultAddr         = ":8080"
	defaultLocale       = "en-US"
	defaultSampleRateHz = 16000
)

// defaultMCPArgs launches the official PayPal MCP server with all tools enabled.
// See https://github.com/paypal/paypal-mcp-server.
var defaultMCPArgs = []string{"-y", "@paypal/mcp", "--tools=all"}

// defaultSupportedLocales reflects the hackathon demo scope: one European and
// one Asian language, plus English as a neutral fallback.
var defaultSupportedLocales = []string{"en-US", "de-DE", "th-TH"}

// Load reads configuration from the environment, applies defaults, and
// validates. It returns an error describing every problem found rather than
// failing on the first, so misconfiguration is quick to fix.
func Load() (*Config, error) {
	cfg := &Config{
		Brand: Brand{
			Name:    getEnv("PAYSAY_BRAND_NAME", defaultBrandName),
			Tagline: getEnv("PAYSAY_BRAND_TAGLINE", defaultBrandTagline),
		},
		PayPal: PayPal{
			AccessToken:   os.Getenv("PAYPAL_ACCESS_TOKEN"),
			Environment:   strings.ToUpper(getEnv("PAYPAL_ENVIRONMENT", defaultEnvironment)),
			MCPCommand:    getEnv("PAYPAL_MCP_COMMAND", defaultMCPCommand),
			MCPArgs:       defaultMCPArgs,
			MerchantEmail: os.Getenv("PAYPAL_MERCHANT_EMAIL"),
		},
		Bedrock: Bedrock{
			ModelID:   getEnv("BEDROCK_MODEL_ID", defaultModelID),
			MaxTokens: int32(getEnvInt("BEDROCK_MAX_TOKENS", defaultMaxTokens)),
		},
		Voice: Voice{
			Enabled:      getEnvBool("VOICE_ENABLED", true),
			TTSEnabled:   getEnvBool("VOICE_TTS_ENABLED", true),
			SampleRateHz: int32(getEnvInt("VOICE_SAMPLE_RATE_HZ", defaultSampleRateHz)),
		},
		AWS: AWS{
			Region: getEnv("AWS_REGION", defaultRegion),
		},
		Server: Server{
			Addr:            getEnv("SERVER_ADDR", defaultAddr),
			ReadTimeout:     getEnvDuration("SERVER_READ_TIMEOUT", 30*time.Second),
			WriteTimeout:    getEnvDuration("SERVER_WRITE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: getEnvDuration("SERVER_SHUTDOWN_TIMEOUT", 15*time.Second),
			AllowedOrigins:  getEnvList("SERVER_ALLOWED_ORIGINS", []string{"*"}),
		},
		DefaultLocale:    getEnv("PAYSAY_DEFAULT_LOCALE", defaultLocale),
		SupportedLocales: getEnvList("PAYSAY_SUPPORTED_LOCALES", defaultSupportedLocales),
	}

	if temp, ok := getEnvFloatOK("BEDROCK_TEMPERATURE"); ok {
		cfg.Bedrock.Temperature = &temp
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks invariants that must hold regardless of whether live
// credentials are present. Credential presence is validated separately via
// RequireLiveCredentials so the app can boot in a mock/demo mode.
func (c *Config) Validate() error {
	var errs []string

	if c.Brand.Name == "" {
		errs = append(errs, "brand name must not be empty")
	}
	if c.PayPal.Environment != "SANDBOX" && c.PayPal.Environment != "PRODUCTION" {
		errs = append(errs, fmt.Sprintf("PAYPAL_ENVIRONMENT must be SANDBOX or PRODUCTION, got %q", c.PayPal.Environment))
	}
	if c.Bedrock.ModelID == "" {
		errs = append(errs, "BEDROCK_MODEL_ID must not be empty")
	}
	if c.Bedrock.MaxTokens <= 0 {
		errs = append(errs, "BEDROCK_MAX_TOKENS must be positive")
	}
	if c.AWS.Region == "" {
		errs = append(errs, "AWS_REGION must not be empty")
	}
	if !contains(c.SupportedLocales, c.DefaultLocale) {
		errs = append(errs, fmt.Sprintf("default locale %q is not in supported locales %v", c.DefaultLocale, c.SupportedLocales))
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// ErrMissingPayPalToken indicates live PayPal calls are not possible.
var ErrMissingPayPalToken = errors.New("PAYPAL_ACCESS_TOKEN is not set")

// RequireLiveCredentials returns an error if credentials needed for live PayPal
// calls are missing. Callers use this to decide between live and mock modes.
func (c *Config) RequireLiveCredentials() error {
	if strings.TrimSpace(c.PayPal.AccessToken) == "" {
		return ErrMissingPayPalToken
	}
	return nil
}

// --- env helpers ---

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func getEnvFloatOK(key string) (float32, bool) {
	if v, ok := os.LookupEnv(key); ok {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			return float32(f), true
		}
	}
	return 0, false
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func getEnvList(key string, def []string) []string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				out = append(out, t)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return def
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
