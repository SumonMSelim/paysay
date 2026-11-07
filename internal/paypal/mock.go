package paypal

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MockProvider is an in-memory ToolProvider that simulates the PayPal invoicing
// tools. It lets PaySay run end-to-end (voice -> agent -> "PayPal") without a
// sandbox token, which is essential for development and for judges who want to
// try the app before wiring credentials. It is NOT a PayPal client; it mimics
// the shape of the MCP tools' inputs and outputs.
type MockProvider struct {
	mu       sync.Mutex
	invoices map[string]*mockInvoice
	merchant string
}

type mockInvoice struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	Recipient  string          `json:"recipient"`
	Currency   string          `json:"currency_code"`
	Total      string          `json:"total"`
	Items      json.RawMessage `json:"items,omitempty"`
	CreateTime string          `json:"create_time"`
}

// NewMockProvider creates a mock provider attributed to the given merchant
// email (used only for display).
func NewMockProvider(merchantEmail string) *MockProvider {
	if merchantEmail == "" {
		merchantEmail = "merchant@paysay.example"
	}
	return &MockProvider{
		invoices: make(map[string]*mockInvoice),
		merchant: merchantEmail,
	}
}

// Tools returns descriptors mirroring the real PayPal invoice tool set so the
// agent builds identical Bedrock tool specs in mock and live modes.
func (m *MockProvider) Tools(ctx context.Context) ([]ToolDescriptor, error) {
	schema := func(s string) json.RawMessage { return json.RawMessage(s) }
	return []ToolDescriptor{
		{
			Name:        "create_invoice",
			Description: "Create a new draft invoice for a customer with line items.",
			InputSchema: schema(`{"type":"object","properties":{"recipient_email":{"type":"string"},"currency_code":{"type":"string"},"items":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"quantity":{"type":"string"},"unit_amount":{"type":"string"}}}},"note":{"type":"string"}},"required":["currency_code","items"]}`),
		},
		{
			Name:        "send_invoice",
			Description: "Send a previously created draft invoice to its recipient.",
			InputSchema: schema(`{"type":"object","properties":{"invoice_id":{"type":"string"}},"required":["invoice_id"]}`),
		},
		{
			Name:        "list_invoices",
			Description: "List invoices, optionally filtered by status (DRAFT, SENT, PAID, UNPAID).",
			InputSchema: schema(`{"type":"object","properties":{"status":{"type":"string"}}}`),
		},
		{
			Name:        "get_invoice",
			Description: "Retrieve the details of a specific invoice by id.",
			InputSchema: schema(`{"type":"object","properties":{"invoice_id":{"type":"string"}},"required":["invoice_id"]}`),
		},
		{
			Name:        "send_invoice_reminder",
			Description: "Send a payment reminder for a sent, unpaid invoice.",
			InputSchema: schema(`{"type":"object","properties":{"invoice_id":{"type":"string"}},"required":["invoice_id"]}`),
		},
		{
			Name:        "generate_invoice_qr_code",
			Description: "Generate a scannable QR code for customer mobile payment of an invoice.",
			InputSchema: schema(`{"type":"object","properties":{"invoice_id":{"type":"string"},"width":{"type":"number"},"height":{"type":"number"}},"required":["invoice_id"]}`),
		},
	}, nil
}

// Invoke dispatches to the simulated tool implementations.
func (m *MockProvider) Invoke(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch name {
	case "create_invoice":
		return m.createInvoice(args)
	case "send_invoice":
		return m.mutateStatus(args, "SENT")
	case "send_invoice_reminder":
		return m.reminder(args)
	case "list_invoices":
		return m.listInvoices(args)
	case "get_invoice":
		return m.getInvoice(args)
	case "generate_invoice_qr_code":
		return m.generateQRCode(args)
	default:
		return ToolResult{IsError: true, Text: fmt.Sprintf(`{"error":"unknown tool %q"}`, name)}, nil
	}
}

// Close is a no-op for the mock.
func (m *MockProvider) Close() error { return nil }

func (m *MockProvider) createInvoice(args json.RawMessage) (ToolResult, error) {
	var in struct {
		RecipientEmail string          `json:"recipient_email"`
		Currency       string          `json:"currency_code"`
		Items          json.RawMessage `json:"items"`
		Total          string          `json:"total"`
	}
	_ = json.Unmarshal(args, &in)
	if in.Currency == "" {
		in.Currency = "USD"
	}
	// Prefer an explicit total; otherwise derive it from the line items, which
	// is how the model typically supplies amounts (items[].unit_amount x
	// quantity). This mirrors real invoicing, where the total is computed.
	total := in.Total
	if total == "" || total == "0" || total == "0.00" {
		if derived, ok := sumItems(in.Items); ok {
			total = derived
		}
	}
	id := "INV2-" + strings.ToUpper(uuid.NewString()[:8])
	inv := &mockInvoice{
		ID:         id,
		Status:     "DRAFT",
		Recipient:  in.RecipientEmail,
		Currency:   in.Currency,
		Total:      firstNonEmpty(total, "0.00"),
		Items:      in.Items,
		CreateTime: time.Now().UTC().Format(time.RFC3339),
	}
	m.invoices[id] = inv
	return marshalResult(inv)
}

// sumItems computes a total from an items array of the shape
// [{"unit_amount": "...", "quantity": "..."}]. unit_amount may be a string or a
// nested {"value": "..."} object (PayPal's shape). Quantity defaults to 1.
// It returns the formatted total and whether any amount was found.
func sumItems(items json.RawMessage) (string, bool) {
	if len(items) == 0 {
		return "", false
	}
	var rows []struct {
		UnitAmount json.RawMessage `json:"unit_amount"`
		Quantity   json.RawMessage `json:"quantity"`
	}
	if err := json.Unmarshal(items, &rows); err != nil {
		return "", false
	}
	var total float64
	found := false
	for _, r := range rows {
		unit, ok := parseAmount(r.UnitAmount)
		if !ok {
			continue
		}
		qty, ok := parseAmount(r.Quantity)
		if !ok || qty == 0 {
			qty = 1
		}
		total += unit * qty
		found = true
	}
	if !found {
		return "", false
	}
	return strconv.FormatFloat(total, 'f', 2, 64), true
}

// parseAmount reads a numeric amount that may be a bare JSON number, a quoted
// numeric string, or a nested {"value": "..."} object.
func parseAmount(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	// Try number or quoted string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, true
	}
	// Try nested {"value": "..."}.
	var obj struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Value != "" {
		if v, err := strconv.ParseFloat(obj.Value, 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

func (m *MockProvider) mutateStatus(args json.RawMessage, status string) (ToolResult, error) {
	id := invoiceID(args)
	inv, ok := m.invoices[id]
	if !ok {
		return notFound(id)
	}
	inv.Status = status
	return marshalResult(inv)
}

func (m *MockProvider) reminder(args json.RawMessage) (ToolResult, error) {
	id := invoiceID(args)
	inv, ok := m.invoices[id]
	if !ok {
		return notFound(id)
	}
	return marshalResult(map[string]any{"invoice_id": inv.ID, "reminder_sent": true, "status": inv.Status})
}

func (m *MockProvider) listInvoices(args json.RawMessage) (ToolResult, error) {
	var in struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(args, &in)
	filter := strings.ToUpper(in.Status)
	list := make([]*mockInvoice, 0, len(m.invoices))
	for _, inv := range m.invoices {
		if filter == "" || inv.Status == filter {
			list = append(list, inv)
		}
	}
	return marshalResult(map[string]any{"items": list, "total_items": len(list)})
}

func (m *MockProvider) getInvoice(args json.RawMessage) (ToolResult, error) {
	id := invoiceID(args)
	inv, ok := m.invoices[id]
	if !ok {
		return notFound(id)
	}
	return marshalResult(inv)
}

func (m *MockProvider) generateQRCode(args json.RawMessage) (ToolResult, error) {
	id := invoiceID(args)
	inv, ok := m.invoices[id]
	if !ok {
		return notFound(id)
	}
	// Return a standard payment URI and demo SVG payload for instant scanning
	payURL := fmt.Sprintf("https://www.sandbox.paypal.com/invoice/p/#%s", inv.ID)
	return marshalResult(map[string]any{
		"invoice_id":   inv.ID,
		"qr_image_url": fmt.Sprintf("https://api.qrserver.com/v1/create-qr-code/?size=250x250&data=%s", payURL),
		"payment_url":  payURL,
		"status":       inv.Status,
		"total":        inv.Total,
		"currency":     inv.Currency,
	})
}

// --- helpers ---

func invoiceID(args json.RawMessage) string {
	var in struct {
		InvoiceID string `json:"invoice_id"`
	}
	_ = json.Unmarshal(args, &in)
	return in.InvoiceID
}

func notFound(id string) (ToolResult, error) {
	return ToolResult{IsError: true, Text: fmt.Sprintf(`{"error":"invoice %q not found"}`, id)}, nil
}

func marshalResult(v any) (ToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Text: string(b)}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
