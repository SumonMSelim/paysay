package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/paysay/internal/paypal/mcp"
)

// createResult returns a fake MCP create_invoice result carrying an invoice ID,
// as the real tool does.
func createResult(id string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.ContentBlock{{Type: "text", Text: `{"id":"` + id + `","status":"DRAFT"}`}}}
}

// serviceWithInvoicing builds a Service whose MCP create returns the given id and
// whose Invoicing REST calls go to baseURL via client.
func serviceWithInvoicing(id, baseURL string, client *http.Client) (*Service, *fakeMCP) {
	fm := &fakeMCP{callResult: createResult(id)}
	svc := newServiceWithClient(fm)
	svc.invoicing = newInvoicingClient(baseURL, "test-token", client)
	return svc, fm
}

func TestInvoicingSetDueDateGetsThenPuts(t *testing.T) {
	withClock(t, fixedNow)
	const id = "INV2-ABCD-EFGH-IJKL-MNOP"

	var gotAuth string
	var putBody map[string]any
	var putQuery string
	var getCalled, putCalled bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.Method {
		case http.MethodGet:
			getCalled = true
			// Echo a representative invoice with fields the PUT must preserve.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": id,
				"detail": map[string]any{
					"currency_code": "EUR",
					"invoice_date":  "2026-03-01",
					"note":          "Thanks",
				},
				"items": []any{map[string]any{"name": "Coffee"}},
			})
		case http.MethodPut:
			putCalled = true
			putQuery = r.URL.RawQuery
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &putBody)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(putBody)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer ts.Close()

	svc, _ := serviceWithInvoicing(id, ts.URL, ts.Client())
	res, err := svc.Invoke(context.Background(), "create_invoice", json.RawMessage(`{"currency_code":"EUR","due_date":"2026-03-20"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !getCalled || !putCalled {
		t.Fatalf("expected GET then PUT, got get=%v put=%v", getCalled, putCalled)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization header = %q, want Bearer test-token", gotAuth)
	}
	// Both notifications must be suppressed via query parameters.
	if !strings.Contains(putQuery, "send_to_recipient=false") || !strings.Contains(putQuery, "send_to_invoicer=false") {
		t.Errorf("PUT query = %q, want send_to_recipient=false and send_to_invoicer=false", putQuery)
	}
	// payment_term must be set to the specified due date, and nothing else in
	// the invoice changed.
	detail, ok := putBody["detail"].(map[string]any)
	if !ok {
		t.Fatalf("PUT body missing detail: %v", putBody)
	}
	term, ok := detail["payment_term"].(map[string]any)
	if !ok {
		t.Fatalf("detail.payment_term missing: %v", detail)
	}
	if term["term_type"] != "DUE_ON_DATE_SPECIFIED" || term["due_date"] != "2026-03-20" {
		t.Errorf("payment_term = %v, want DUE_ON_DATE_SPECIFIED + 2026-03-20", term)
	}
	if detail["currency_code"] != "EUR" || detail["note"] != "Thanks" {
		t.Errorf("unrelated detail fields changed: %v", detail)
	}
	if _, ok := putBody["items"]; !ok {
		t.Error("items dropped from the full update body")
	}
	// The tool output must report the due date as set.
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out["due_date"] != "2026-03-20" || out["due_date_set"] != true {
		t.Errorf("output = %v, want due_date 2026-03-20 and due_date_set true", out)
	}
}

func TestInvoicingGetFailureFlagsOutputButCreateSucceeds(t *testing.T) {
	withClock(t, fixedNow)
	const id = "INV2-ABCD-EFGH-IJKL-MNOP"

	putCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusForbidden) // e.g. feature not enabled
			return
		}
		putCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	svc, _ := serviceWithInvoicing(id, ts.URL, ts.Client())
	res, err := svc.Invoke(context.Background(), "create_invoice", json.RawMessage(`{"currency_code":"EUR"}`))
	if err != nil {
		t.Fatalf("Invoke must not fail when the due-date update fails: %v", err)
	}
	if res.IsError {
		t.Error("create should still count as success")
	}
	if putCalled {
		t.Error("PUT must not run after a failed GET")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out["due_date_set"] != false {
		t.Errorf("due_date_set = %v, want false", out["due_date_set"])
	}
	if out["id"] != id {
		t.Errorf("output lost the invoice id: %v", out)
	}
	if _, ok := out["due_date_note"]; !ok {
		t.Error("output should explain the invoice has no due date yet")
	}
}

func TestInvoicingPutFailureFlagsOutputButCreateSucceeds(t *testing.T) {
	withClock(t, fixedNow)
	const id = "INV2-ABCD-EFGH-IJKL-MNOP"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "detail": map[string]any{"currency_code": "EUR"}})
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity) // PUT rejected
	}))
	defer ts.Close()

	svc, _ := serviceWithInvoicing(id, ts.URL, ts.Client())
	res, err := svc.Invoke(context.Background(), "create_invoice", json.RawMessage(`{"currency_code":"EUR"}`))
	if err != nil {
		t.Fatalf("Invoke must not fail when the PUT fails: %v", err)
	}
	if res.IsError {
		t.Error("create should still count as success")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out["due_date_set"] != false {
		t.Errorf("due_date_set = %v, want false after a PUT failure", out["due_date_set"])
	}
}

func TestInvoicingSucceedsWithDefaultDueDate(t *testing.T) {
	withClock(t, fixedNow)
	const id = "INV2-ABCD-EFGH-IJKL-MNOP"

	var putTerm map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "detail": map[string]any{"currency_code": "EUR"}})
			return
		}
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		if d, ok := body["detail"].(map[string]any); ok {
			putTerm, _ = d["payment_term"].(map[string]any)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	svc, _ := serviceWithInvoicing(id, ts.URL, ts.Client())
	// No due date supplied: the 14-day default applies (2026-03-01 + 14).
	res, err := svc.Invoke(context.Background(), "create_invoice", json.RawMessage(`{"currency_code":"EUR"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if putTerm["due_date"] != "2026-03-15" {
		t.Errorf("due_date sent = %v, want the 14-day default 2026-03-15", putTerm["due_date"])
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(res.Text), &out)
	if out["due_date"] != "2026-03-15" || out["due_date_set"] != true {
		t.Errorf("output = %v, want due_date 2026-03-15 set true", out)
	}
}

// secretBody is a recognizable response body the failing PayPal endpoint returns,
// so the logging tests can assert it never leaks into the log.
const secretBody = "SECRET-RESPONSE-BODY-should-never-be-logged"

func TestInvoicingPutFailureLogsStatusWithoutBody(t *testing.T) {
	withClock(t, fixedNow)
	const id = "INV2-ABCD-EFGH-IJKL-MNOP"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "detail": map[string]any{"currency_code": "EUR"}})
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(secretBody))
	}))
	defer ts.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	fm := &fakeMCP{callResult: createResult(id)}
	svc := newServiceWithClient(fm, WithLogger(logger))
	svc.invoicing = newInvoicingClient(ts.URL, "test-token", ts.Client())

	if _, err := svc.Invoke(context.Background(), "create_invoice", json.RawMessage(`{"currency_code":"EUR"}`)); err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "due date not set") {
		t.Errorf("expected a WARN about the due date, got: %q", logged)
	}
	if !strings.Contains(logged, "step=put") {
		t.Errorf("log should name the failed step, got: %q", logged)
	}
	if !strings.Contains(logged, "status=422") {
		t.Errorf("log should include the HTTP status 422, got: %q", logged)
	}
	if !strings.Contains(logged, id) {
		t.Errorf("log should include the invoice id, got: %q", logged)
	}
	if strings.Contains(logged, secretBody) {
		t.Errorf("log leaked the response body: %q", logged)
	}
	if strings.Contains(logged, "test-token") {
		t.Errorf("log leaked the access token: %q", logged)
	}
}

func TestInvoicingMissingInvoiceIDLogsWarn(t *testing.T) {
	withClock(t, fixedNow)

	// MCP returns output with no INV2 id, so the due date cannot be addressed.
	fm := &fakeMCP{callResult: &mcp.CallToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: `{"status":"DRAFT"}`}},
	}}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc := newServiceWithClient(fm, WithLogger(logger))
	// A non-nil invoicing client that must never be reached on the missing-id path.
	svc.invoicing = newInvoicingClient("http://127.0.0.1:0", "test-token", &http.Client{})

	res, err := svc.Invoke(context.Background(), "create_invoice", json.RawMessage(`{"currency_code":"EUR"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "no invoice id") {
		t.Errorf("expected a WARN about the missing invoice id, got: %q", logged)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(res.Text), &out)
	if out["due_date_set"] != false {
		t.Errorf("due_date_set = %v, want false when no id was found", out["due_date_set"])
	}
}
