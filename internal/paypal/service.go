// Package paypal provides a domain-oriented view over the PayPal MCP server.
// The agent layer depends on the ToolProvider interface defined here, not on
// the concrete MCP client, which keeps the agent testable and lets us swap in a
// mock provider when no sandbox token is configured.
package paypal

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SumonMSelim/paysay/internal/paypal/mcp"
)

// ToolDescriptor is the transport-agnostic description of a callable PayPal
// tool. It is produced from the MCP server's tools/list and consumed by the
// agent to build Bedrock tool specifications.
type ToolDescriptor struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object (as raw JSON) describing arguments.
	InputSchema json.RawMessage
}

// ToolResult is the outcome of invoking a tool.
type ToolResult struct {
	// Text is the tool's textual output, typically JSON returned by PayPal.
	Text string
	// IsError indicates a tool-level failure (e.g. validation rejected by
	// PayPal). The agent surfaces this to the model rather than aborting.
	IsError bool
}

// ToolProvider is the capability the agent needs from PayPal: discover tools
// and invoke them. Implemented by *Service (live, over MCP) and by mocks.
type ToolProvider interface {
	// Tools returns the available PayPal tools.
	Tools(ctx context.Context) ([]ToolDescriptor, error)
	// Invoke calls a tool by name with JSON arguments.
	Invoke(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)
	// Close releases resources (e.g. the MCP subprocess).
	Close() error
}

// mcpClient is the subset of *mcp.Client the Service depends on, declared as an
// interface so Service itself can be unit-tested with a fake.
type mcpClient interface {
	Initialize(ctx context.Context) (*mcp.InitializeResult, error)
	ListTools(ctx context.Context) ([]mcp.Tool, error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error)
	Close() error
}

// Service is the live ToolProvider backed by the PayPal MCP server.
type Service struct {
	client        mcpClient
	closers       []func() error
	allowSet      map[string]bool // optional allow-list of tool names
	merchantEmail string          // optional default merchant email for invoicing
	merchantName  string          // optional business name printed on invoices
}

// Option configures a Service.
type Option func(*Service)

// WithAllowedTools restricts the tools the agent may see and call. This is a
// safety control: for the merchant-invoicing use case we expose only the
// invoice/order tools and hide, say, dispute acceptance unless intended.
func WithAllowedTools(names ...string) Option {
	return func(s *Service) {
		s.allowSet = make(map[string]bool, len(names))
		for _, n := range names {
			s.allowSet[n] = true
		}
	}
}

// WithMerchantEmail configures the fallback invoicer/merchant email.
func WithMerchantEmail(email string) Option {
	return func(s *Service) {
		s.merchantEmail = email
	}
}

// WithMerchantName configures the business name printed on invoices.
func WithMerchantName(name string) Option {
	return func(s *Service) {
		s.merchantName = name
	}
}

// LaunchConfig holds the parameters needed to start the MCP server subprocess.
type LaunchConfig struct {
	Command     string
	Args        []string
	AccessToken string
	Environment string // SANDBOX or PRODUCTION
	ExtraEnv    []string
}

// NewService launches the PayPal MCP server, performs the MCP handshake, and
// returns a ready Service. The caller must Close it.
func NewService(ctx context.Context, lc LaunchConfig, opts ...Option) (*Service, error) {
	if lc.AccessToken == "" {
		return nil, fmt.Errorf("paypal: access token required to launch MCP server")
	}
	env := append([]string{
		"PAYPAL_ACCESS_TOKEN=" + lc.AccessToken,
		"PAYPAL_ENVIRONMENT=" + lc.Environment,
	}, lc.ExtraEnv...)

	client, transport, err := mcp.Launch(ctx, lc.Command, lc.Args, env)
	if err != nil {
		return nil, fmt.Errorf("paypal: launch MCP server: %w", err)
	}
	s := &Service{client: client}
	s.closers = append(s.closers, client.Close, transport.Close)
	for _, o := range opts {
		o(s)
	}

	if _, err := client.Initialize(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("paypal: initialize MCP: %w", err)
	}
	return s, nil
}

// newServiceWithClient builds a Service around an existing client (used in
// tests). It does not perform Initialize.
func newServiceWithClient(c mcpClient, opts ...Option) *Service {
	s := &Service{client: c, closers: []func() error{c.Close}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Tools lists available tools, applying the allow-list if configured.
func (s *Service) Tools(ctx context.Context) ([]ToolDescriptor, error) {
	tools, err := s.client.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("paypal: list tools: %w", err)
	}
	out := make([]ToolDescriptor, 0, len(tools))
	for _, t := range tools {
		if s.allowSet != nil && !s.allowSet[t.Name] {
			continue
		}
		out = append(out, ToolDescriptor{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return out, nil
}

// Invoke calls a tool, enforcing the allow-list as a defense-in-depth check so
// the model cannot call a disallowed tool even if it hallucinates one.
func (s *Service) Invoke(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	if s.allowSet != nil && !s.allowSet[name] {
		return ToolResult{}, fmt.Errorf("paypal: tool %q is not allowed", name)
	}

	shapedArgs := args
	if name == "create_invoice" {
		shapedArgs = ScrubInvoicer(ShapeCreateInvoiceArgs(args, s.merchantEmail), s.merchantEmail, s.merchantName)
	}

	res, err := s.client.CallTool(ctx, name, shapedArgs)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Text: res.Text(), IsError: res.IsError}, nil
}

// Close shuts down the MCP client and subprocess.
func (s *Service) Close() error {
	var firstErr error
	for _, c := range s.closers {
		if err := c(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ApprovalTools create invoices or contact customers. The agent never runs
// them on its own; each one needs the merchant's explicit approval.
var ApprovalTools = []string{
	"create_invoice",
	"send_invoice",
	"send_invoice_reminder",
}

// InvoiceTools is the default safe allow-list for the merchant-invoicing use
// case: everything needed to create, send, inspect, and remind on invoices,
// plus read-only order lookups. Money-sensitive tools like dispute acceptance
// are intentionally excluded by default.
var InvoiceTools = []string{
	"create_invoice",
	"send_invoice",
	"list_invoices",
	"get_invoice",
	"send_invoice_reminder",
	"generate_invoice_qr_code",
	"get_order",
}
