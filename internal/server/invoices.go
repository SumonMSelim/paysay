package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tuning for the PayPal-backed read routes. invoiceRateLimitPerMinute caps the
// list/QR endpoints per client, separate from the chat budget but counting
// toward the same global hourly cap. invoiceCacheTTL memoizes the list for a
// few seconds so refreshes and multiple tabs do not each hit PayPal.
const (
	invoiceRateLimitPerMinute = 30
	invoiceCacheTTL           = 5 * time.Second
)

// invoiceCache holds the most recent list_invoices result keyed by its status
// filter for a short TTL. The collections job polls once a day, so it is never
// served stale in a way that matters; interactive refreshes are.
type invoiceCache struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]invoiceCacheEntry
}

type invoiceCacheEntry struct {
	text    string
	expires time.Time
}

func newInvoiceCache(ttl time.Duration) *invoiceCache {
	return &invoiceCache{ttl: ttl, entries: make(map[string]invoiceCacheEntry)}
}

// get returns the cached list text for the given status filter when it is still
// fresh.
func (c *invoiceCache) get(status string, t time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[status]
	if !ok || t.After(e.expires) {
		return "", false
	}
	return e.text, true
}

// put stores a list result for the given status filter.
func (c *invoiceCache) put(status, text string, t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[status] = invoiceCacheEntry{text: text, expires: t.Add(c.ttl)}
}

// invalidate drops every cached entry, so the next read hits PayPal. Called
// after a successful create, send or remind.
func (c *invoiceCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]invoiceCacheEntry)
}

// writePayPalConnecting reports that PayPal is still connecting in the
// background (live mode startup) so the caller should retry shortly. It is used
// by every PayPal-backed route while the readiness gate is not yet ready.
func writePayPalConnecting(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error": "PayPal is still connecting, please try again in a moment",
		"code":  "paypal_connecting",
	})
}

// InvoiceLister is the read-only capability the server needs to populate the
// invoice dashboard. It is satisfied by the PayPal ToolProvider (live or mock)
// by calling the list_invoices tool. It is optional: when nil, the dashboard
// endpoint returns an empty list rather than erroring.
type InvoiceLister interface {
	Invoke(ctx context.Context, name string, args json.RawMessage) (toolResult, error)
}

// toolResult mirrors paypal.ToolResult without importing the paypal package,
// avoiding a dependency cycle risk and keeping the server layer decoupled. The
// adapter in wiring code converts between the two.
type toolResult struct {
	Text    string
	IsError bool
}

// InvoiceRow is the normalized, UI-facing shape of an invoice, stable across the
// mock provider and the real PayPal Invoicing v2 response.
type InvoiceRow struct {
	ID       string `json:"id"`
	Number   string `json:"number"`
	Status   string `json:"status"`
	Customer string `json:"customer"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Created  string `json:"created"`
	Due      string `json:"due"`
	Paid     string `json:"paid,omitempty"`
}

// handleInvoices returns the merchant's invoices as normalized rows for the
// AG Grid dashboard. It calls the list_invoices tool and tolerates both the
// mock shape and the PayPal Invoicing v2 shape. With ?scope=mine it returns
// only the invoices this browser session created.
func (s *Server) handleInvoices(w http.ResponseWriter, r *http.Request) {
	invoices, ready := s.gate.lister()
	if !ready {
		writePayPalConnecting(w)
		return
	}
	if invoices == nil {
		writeJSON(w, http.StatusOK, map[string]any{"invoices": []InvoiceRow{}})
		return
	}

	// Optional status filter passed through to the tool.
	status := r.URL.Query().Get("status")
	args := json.RawMessage(`{}`)
	if status != "" {
		b, _ := json.Marshal(map[string]string{"status": status})
		args = b
	}

	// Serve the raw list from the short-lived cache when fresh, so refreshes
	// and multiple tabs do not each hit PayPal. The cache holds the unfiltered
	// tool output keyed by status; scope=mine filtering is applied afterwards.
	text, cached := s.invoiceCache.get(status, now())
	if !cached {
		res, err := invoices.Invoke(r.Context(), "list_invoices", args)
		if err != nil || res.IsError {
			s.log.Error("list invoices failed", "error", err, "tool_error", res.IsError)
			writeError(w, http.StatusBadGateway, "could not load invoices")
			return
		}
		text = res.Text
		s.invoiceCache.put(status, text, now())
	}

	rows := normalizeInvoices(text)
	if r.URL.Query().Get("scope") == "mine" {
		rows = s.sessions.filter(sessionID(w, r), rows)
	}
	// Most-recent first for a sensible default order.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Created > rows[j].Created })
	writeJSON(w, http.StatusOK, map[string]any{"invoices": rows})
}

// handleInvoiceQR generates a scannable payment QR code for an invoice.
func (s *Server) handleInvoiceQR(w http.ResponseWriter, r *http.Request) {
	invoices, ready := s.gate.lister()
	if !ready {
		writePayPalConnecting(w)
		return
	}
	if invoices == nil {
		writeError(w, http.StatusNotFound, "invoice service not available")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "invoice ID is required")
		return
	}

	args, _ := json.Marshal(map[string]any{
		"invoice_id": id,
		"width":      300,
		"height":     300,
	})

	res, err := invoices.Invoke(r.Context(), "generate_invoice_qr_code", args)
	if err != nil || res.IsError {
		s.log.Error("generate qr code failed", "invoice_id", id, "error", err, "tool_error", res.IsError)
		writeError(w, http.StatusBadGateway, "could not generate QR code")
		return
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(res.Text), &data); err != nil {
		data = map[string]any{}
	}

	// Clean up base64 image if wrapped in multipart text by PayPal MCP
	if img, ok := extractBase64Image(res.Text); ok {
		data["image"] = img
	}

	host := "www.sandbox.paypal.com"
	if s.cfg.PayPal.Environment == "PRODUCTION" {
		host = "www.paypal.com"
	}
	payURL := fmt.Sprintf("https://%s/invoice/p/#%s", host, id)
	data["invoice_id"] = id
	data["payment_url"] = payURL
	writeJSON(w, http.StatusOK, data)
}

func extractBase64Image(raw string) (string, bool) {
	idx := strings.Index(raw, "iVBORw0KGgo")
	if idx == -1 {
		return "", false
	}
	end := strings.IndexAny(raw[idx:], "\r\n\"\\")
	if end == -1 {
		return raw[idx:], true
	}
	return raw[idx : idx+end], true
}

// rawInvoiceList models the union of fields we read from either provider shape.
// Unknown fields are ignored by the JSON decoder.
type rawInvoiceList struct {
	Items []rawInvoice `json:"items"`
}

type rawInvoice struct {
	ID     string `json:"id"`
	Status string `json:"status"`

	// Mock (flat) shape.
	Recipient    string `json:"recipient"`
	CurrencyFlat string `json:"currency_code"`
	Total        string `json:"total"`
	CreateTime   string `json:"create_time"`
	NumberFlat   string `json:"invoice_number"`
	DueDateFlat  string `json:"due_date"`
	PaidDateFlat string `json:"paid_date"`

	// PayPal v2 (nested) shape.
	Detail struct {
		CurrencyCode  string `json:"currency_code"`
		CreateTime    string `json:"create_time"`
		InvoiceNumber string `json:"invoice_number"`
		PaymentTerm   struct {
			DueDate string `json:"due_date"`
		} `json:"payment_term"`
		Metadata struct {
			CreateTime     string `json:"create_time"`
			LastUpdateTime string `json:"last_update_time"`
		} `json:"metadata"`
	} `json:"detail"`
	Amount struct {
		Value        string `json:"value"`
		CurrencyCode string `json:"currency_code"`
	} `json:"amount"`
	PrimaryRecipients []struct {
		BillingInfo struct {
			Email string `json:"email_address"`
		} `json:"billing_info"`
	} `json:"primary_recipients"`
	Payments struct {
		Transactions []struct {
			PaymentDate string `json:"payment_date"`
			PaymentTime string `json:"payment_time"`
		} `json:"transactions"`
	} `json:"payments"`
}

// normalizeInvoices parses a list_invoices tool result (either shape) into rows.
func normalizeInvoices(raw string) []InvoiceRow {
	var list rawInvoiceList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return []InvoiceRow{}
	}
	rows := make([]InvoiceRow, 0, len(list.Items))
	for _, it := range list.Items {
		row := InvoiceRow{ID: it.ID, Status: it.Status}

		// Number: the merchant-facing invoice number (e.g. "0007"), distinct
		// from the resource ID. Live PayPal puts it under detail.invoice_number.
		row.Number = firstNonEmpty(it.NumberFlat, it.Detail.InvoiceNumber)

		// Customer: mock flat field, else first primary recipient email.
		row.Customer = it.Recipient
		if row.Customer == "" && len(it.PrimaryRecipients) > 0 {
			row.Customer = it.PrimaryRecipients[0].BillingInfo.Email
		}

		// Currency: flat field, else amount, else detail.
		row.Currency = firstNonEmpty(it.CurrencyFlat, it.Amount.CurrencyCode, it.Detail.CurrencyCode)

		// Amount: flat total, else nested amount value.
		row.Amount = firstNonEmpty(it.Total, it.Amount.Value)

		// Created: flat, else nested detail. Live PayPal responses put it under
		// detail.metadata.
		row.Created = firstNonEmpty(it.CreateTime, it.Detail.CreateTime, it.Detail.Metadata.CreateTime)

		// Due: flat field, else nested payment_term.due_date.
		row.Due = firstNonEmpty(it.DueDateFlat, it.Detail.PaymentTerm.DueDate)

		// Paid: flat, else payment transaction date, else last update time if paid.
		var paymentDate string
		if len(it.Payments.Transactions) > 0 {
			paymentDate = firstNonEmpty(it.Payments.Transactions[0].PaymentDate, it.Payments.Transactions[0].PaymentTime)
		}
		if strings.EqualFold(it.Status, "PAID") {
			row.Paid = firstNonEmpty(it.PaidDateFlat, paymentDate, it.Detail.Metadata.LastUpdateTime)
		}

		rows = append(rows, row)
	}
	return rows
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// invoiceListerAdapter adapts a primitive-returning provider into InvoiceLister.
type invoiceListerAdapter struct {
	invoke func(ctx context.Context, name string, args json.RawMessage) (toolResult, error)
}

func (a invoiceListerAdapter) Invoke(ctx context.Context, name string, args json.RawMessage) (toolResult, error) {
	return a.invoke(ctx, name, args)
}

// invoiceResultProvider is the minimal capability needed to list invoices,
// expressed with primitive return types so the server package does not import
// the paypal package (keeping the HTTP layer decoupled from the provider).
// The wiring layer passes a tiny shim over paypal.ToolProvider.
type invoiceResultProvider interface {
	Invoke(ctx context.Context, name string, args json.RawMessage) (text string, isError bool, err error)
}

// NewInvoiceListerAdapter wraps an invoiceResultProvider as an InvoiceLister.
func NewInvoiceListerAdapter(p invoiceResultProvider) InvoiceLister {
	return invoiceListerAdapter{
		invoke: func(ctx context.Context, name string, args json.RawMessage) (toolResult, error) {
			text, isErr, err := p.Invoke(ctx, name, args)
			return toolResult{Text: text, IsError: isErr}, err
		},
	}
}
