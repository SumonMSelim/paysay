package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/paysay/internal/agent"
	"github.com/SumonMSelim/paysay/internal/config"
)

func TestRecordCreatedKeepsOnlySuccessfulCreates(t *testing.T) {
	s := newSessionInvoices()
	s.recordCreated("s1", []agent.ToolCall{
		{Name: "create_invoice", Output: `{"id":"INV2-AAAA-1111","status":"DRAFT"}`},
		{Name: "create_invoice", Output: `{"error":"bad INV2-ERR"}`, IsError: true},
		{Name: "send_invoice", Output: `{"id":"INV2-BBBB-2222"}`},
		// Live MCP may return a link rather than an id field.
		{Name: "create_invoice", Output: `{"rel":"self","href":"https://api-m.sandbox.paypal.com/v2/invoicing/invoices/INV2-CCCC-3333"}`},
	})

	rows := []InvoiceRow{{ID: "INV2-AAAA-1111"}, {ID: "INV2-ERR"}, {ID: "INV2-BBBB-2222"}, {ID: "INV2-CCCC-3333"}}
	got := s.filter("s1", rows)
	if len(got) != 2 || got[0].ID != "INV2-AAAA-1111" || got[1].ID != "INV2-CCCC-3333" {
		t.Errorf("filter = %+v, want the two created invoices", got)
	}
	if other := s.filter("s2", rows); len(other) != 0 {
		t.Errorf("another session sees %+v, want none", other)
	}
}

func TestScopeMineShowsOnlyThisSessionsInvoices(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	ag := &stubAgent{result: &agent.Result{
		Reply:     "created",
		ToolCalls: []agent.ToolCall{{Name: "create_invoice", Output: `{"id":"INV2-MINE"}`}},
	}}
	lister := &stubLister{resp: `{"items":[{"id":"INV2-MINE","status":"DRAFT"},{"id":"INV2-OTHER","status":"SENT"}]}`}
	h := New(Options{Config: cfg, Agent: ag, Invoices: lister}).Handler()

	// Chat once to create an invoice; the response issues the session cookie.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"invoice"}`)))
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie || !cookies[0].HttpOnly {
		t.Fatalf("expected one HttpOnly session cookie, got %+v", cookies)
	}

	list := func(withCookie bool) []InvoiceRow {
		req := httptest.NewRequest(http.MethodGet, "/api/invoices?scope=mine", nil)
		if withCookie {
			req.AddCookie(cookies[0])
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		var out struct {
			Invoices []InvoiceRow `json:"invoices"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.Invoices
	}

	if mine := list(true); len(mine) != 1 || mine[0].ID != "INV2-MINE" {
		t.Errorf("scope=mine = %+v, want only INV2-MINE", mine)
	}
	if fresh := list(false); len(fresh) != 0 {
		t.Errorf("a new visitor sees %+v, want none", fresh)
	}
}

func TestSessionCookieIgnoresForgedValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "not-a-uuid"})
	rr := httptest.NewRecorder()

	id := sessionID(rr, req)
	if id == "not-a-uuid" {
		t.Fatal("non-UUID cookie value was accepted")
	}
	if len(rr.Result().Cookies()) != 1 {
		t.Error("expected a fresh cookie to be issued")
	}
}
