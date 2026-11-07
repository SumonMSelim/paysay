package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

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
	Status   string `json:"status"`
	Customer string `json:"customer"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Created  string `json:"created"`
}

// handleInvoices returns the merchant's invoices as normalized rows for the
// AG Grid dashboard. It calls the list_invoices tool and tolerates both the
// mock shape and the PayPal Invoicing v2 shape. With ?scope=mine it returns
// only the invoices this browser session created.
func (s *Server) handleInvoices(w http.ResponseWriter, r *http.Request) {
	if s.invoices == nil {
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

	res, err := s.invoices.Invoke(r.Context(), "list_invoices", args)
	if err != nil || res.IsError {
		s.log.Error("list invoices failed", "error", err, "tool_error", res.IsError)
		writeError(w, http.StatusBadGateway, "could not load invoices")
		return
	}

	rows := normalizeInvoices(res.Text)
	if r.URL.Query().Get("scope") == "mine" {
		rows = s.sessions.filter(sessionID(w, r), rows)
	}
	// Most-recent first for a sensible default order.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Created > rows[j].Created })
	writeJSON(w, http.StatusOK, map[string]any{"invoices": rows})
}

// handleInvoiceQR generates a scannable payment QR code for an invoice.
func (s *Server) handleInvoiceQR(w http.ResponseWriter, r *http.Request) {
	if s.invoices == nil {
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

	res, err := s.invoices.Invoke(r.Context(), "generate_invoice_qr_code", args)
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

	// PayPal v2 (nested) shape.
	Detail struct {
		CurrencyCode string `json:"currency_code"`
		CreateTime   string `json:"create_time"`
		Metadata     struct {
			CreateTime string `json:"create_time"`
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
