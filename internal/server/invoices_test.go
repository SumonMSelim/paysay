package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SumonMSelim/paysay/internal/config"
)

// stubLister implements InvoiceLister with a canned list_invoices response.
type stubLister struct {
	resp    string
	isError bool
	gotArgs json.RawMessage
}

func (s *stubLister) Invoke(ctx context.Context, name string, args json.RawMessage) (toolResult, error) {
	s.gotArgs = args
	return toolResult{Text: s.resp, IsError: s.isError}, nil
}

func TestNormalizeInvoicesMockShape(t *testing.T) {
	raw := `{"items":[
		{"id":"INV2-A","status":"DRAFT","recipient":"a@x.com","currency_code":"EUR","total":"30.00","create_time":"2026-10-31T10:00:00Z"},
		{"id":"INV2-B","status":"SENT","recipient":"b@x.com","currency_code":"THB","total":"420","create_time":"2026-10-31T11:00:00Z"}
	],"total_items":2}`
	rows := normalizeInvoices(raw)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Customer != "a@x.com" || rows[0].Currency != "EUR" || rows[0].Amount != "30.00" {
		t.Errorf("row0 = %+v", rows[0])
	}
}

func TestHandleInvoiceQR(t *testing.T) {
	stub := &stubLister{resp: `{"qr_image_url":"https://example.com/qr.png"}`}
	cfg := &config.Config{Brand: config.Brand{Name: "PaySay"}}
	srv := New(Options{Config: cfg, Invoices: stub})

	req := httptest.NewRequest(http.MethodGet, "/api/invoices/INV2-123/qr", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res["invoice_id"] != "INV2-123" {
		t.Errorf("invoice_id = %v, want INV2-123", res["invoice_id"])
	}
}

func TestNormalizeInvoicesPayPalV2Shape(t *testing.T) {
	raw := `{"items":[
		{"id":"INV2-9","status":"UNPAID",
		 "detail":{"currency_code":"EUR","create_time":"2026-10-30T09:00:00Z","invoice_number":"0007"},
		 "amount":{"value":"99.00","currency_code":"EUR"},
		 "primary_recipients":[{"billing_info":{"email_address":"payer@x.com"}}]}
	]}`
	rows := normalizeInvoices(raw)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	r := rows[0]
	if r.ID != "INV2-9" || r.Status != "UNPAID" || r.Customer != "payer@x.com" ||
		r.Amount != "99.00" || r.Currency != "EUR" {
		t.Errorf("row = %+v", r)
	}
	if r.Number != "0007" {
		t.Errorf("number = %q, want 0007 from detail.invoice_number", r.Number)
	}
}

// TestNormalizeInvoicesLiveSandboxShape uses the list_invoices item shape
// returned by the live PayPal sandbox, where create_time sits under
// detail.metadata.
func TestNormalizeInvoicesLiveSandboxShape(t *testing.T) {
	raw := `{"total_pages":1,"total_items":1,"items":[
		{"id":"INV2-HPMV-U49W-SPT3-FDWN","status":"DRAFT",
		 "detail":{"currency_code":"EUR","invoice_number":"0001","invoice_date":"2026-11-06",
		           "metadata":{"create_time":"2026-11-07T01:05:44Z"}},
		 "amount":{"currency_code":"EUR","value":"45.00"},
		 "primary_recipients":[{"billing_info":{"email_address":"maria@example.com"}}]}
	]}`
	rows := normalizeInvoices(raw)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if got := rows[0].Created; got != "2026-11-07T01:05:44Z" {
		t.Errorf("created = %q, want detail.metadata.create_time", got)
	}
	if rows[0].Amount != "45.00" || rows[0].Customer != "maria@example.com" {
		t.Errorf("row = %+v", rows[0])
	}
	if rows[0].Number != "0001" {
		t.Errorf("number = %q, want 0001 from detail.invoice_number", rows[0].Number)
	}
}

func TestNormalizeInvoicesBadJSON(t *testing.T) {
	if rows := normalizeInvoices("not json"); len(rows) != 0 {
		t.Errorf("expected empty rows for bad JSON, got %d", len(rows))
	}
}

func testServerWithInvoices(t *testing.T, lister InvoiceLister) *Server {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return New(Options{Config: cfg, Agent: &stubAgent{}, Invoices: lister})
}

func TestInvoicesEndpointReturnsRows(t *testing.T) {
	lister := &stubLister{resp: `{"items":[{"id":"INV2-A","status":"SENT","recipient":"a@x.com","currency_code":"EUR","total":"30.00","create_time":"2026-10-31T10:00:00Z"}]}`}
	srv := testServerWithInvoices(t, lister)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/invoices?status=SENT", nil)
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var out struct {
		Invoices []InvoiceRow `json:"invoices"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Invoices) != 1 || out.Invoices[0].ID != "INV2-A" {
		t.Fatalf("unexpected invoices: %+v", out.Invoices)
	}
	// Status filter should have been forwarded to the tool.
	var args map[string]string
	_ = json.Unmarshal(lister.gotArgs, &args)
	if args["status"] != "SENT" {
		t.Errorf("forwarded args = %v, want status=SENT", args)
	}
}

func TestInvoicesEndpointNilListerReturnsEmpty(t *testing.T) {
	srv := testServerWithInvoices(t, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/invoices", nil)
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var out struct {
		Invoices []InvoiceRow `json:"invoices"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if len(out.Invoices) != 0 {
		t.Errorf("expected empty, got %+v", out.Invoices)
	}
}
