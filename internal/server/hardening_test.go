package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/paysay/internal/agent"
	"github.com/SumonMSelim/paysay/internal/config"
	"github.com/SumonMSelim/paysay/internal/i18n"
)

// --- item 4: error taxonomy HTTP mapping ---

func TestChatErrorTaxonomy(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"busy", fmt.Errorf("agent: converse: %w: x", agent.ErrModelBusy), http.StatusTooManyRequests, "assistant_busy"},
		{"unavailable", fmt.Errorf("agent: converse: %w: x", agent.ErrModelUnavailable), http.StatusServiceUnavailable, "assistant_unavailable"},
		{"error", fmt.Errorf("agent: converse: %w: x", agent.ErrModelError), http.StatusBadGateway, "assistant_error"},
		{"timeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "assistant_timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := testServer(t, &stubAgent{err: c.err}, nil)
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi"}`)))
			if rr.Code != c.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, c.wantCode, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), c.wantBody) {
				t.Errorf("body = %s, want code %s", rr.Body.String(), c.wantBody)
			}
		})
	}
}

func TestChatBusySetsRetryAfter(t *testing.T) {
	srv := testServer(t, &stubAgent{err: fmt.Errorf("x: %w", agent.ErrModelBusy)}, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi"}`)))
	if rr.Header().Get("Retry-After") == "" {
		t.Error("429 assistant_busy should set Retry-After")
	}
}

// --- item 7: chat handler timeout ---

// blockingAgent blocks until its context is cancelled, then returns the
// context error, simulating an agent turn that runs past the deadline.
type blockingAgent struct{}

func (blockingAgent) Respond(ctx context.Context, _ []agent.Turn, _ string, _ i18n.Locale) (*agent.Result, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestChatTimesOut(t *testing.T) {
	// Shrink the chat turn deadline for the test.
	orig := chatTurnTimeout
	chatTurnTimeout = 20 * time.Millisecond
	defer func() { chatTurnTimeout = orig }()

	srv := testServer(t, blockingAgent{}, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi"}`)))
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "assistant_timeout") {
		t.Errorf("body = %s, want assistant_timeout", rr.Body.String())
	}
}

// --- item 13: security headers ---

func TestSecurityHeadersHardened(t *testing.T) {
	srv := testServer(t, &stubAgent{}, nil)

	// Plain HTTP: no HSTS.
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := rr.Header().Get("Permissions-Policy"); got != "microphone=(self), camera=(), geolocation=()" {
		t.Errorf("Permissions-Policy = %q", got)
	}
	if rr.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS should not be set on a plain HTTP request")
	}

	// Forwarded HTTPS: HSTS present.
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	srv.Handler().ServeHTTP(rr, req)
	if !strings.Contains(rr.Header().Get("Strict-Transport-Security"), "max-age=") {
		t.Errorf("HSTS missing on forwarded-https request: %q", rr.Header().Get("Strict-Transport-Security"))
	}
}

// --- item 10: origin check ---

func TestOriginCheck(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Server.AllowedOrigins = []string{"https://paysay.onrender.com"}
	srv := New(Options{Config: cfg, Agent: &stubAgent{}})
	h := srv.Handler()

	send := func(origin string) int {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi"}`))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	if code := send("https://evil.example"); code != http.StatusForbidden {
		t.Errorf("disallowed origin status = %d, want 403", code)
	}
	if code := send("https://paysay.onrender.com"); code == http.StatusForbidden {
		t.Error("allowed origin should not be rejected")
	}
	if code := send(""); code == http.StatusForbidden {
		t.Error("no-origin request (non-browser) should be allowed")
	}
}

func TestOriginCheckSameHostWhenWildcard(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	// Default AllowedOrigins is the wildcard; a cross-host origin is rejected.
	srv := New(Options{Config: cfg, Agent: &stubAgent{}})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi"}`))
	req.Host = "paysay.onrender.com"
	req.Header.Set("Origin", "https://another-site.example")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-host origin under wildcard = %d, want 403", rr.Code)
	}
}

func TestOriginCheckNotAppliedToInternalCollections(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Server.AllowedOrigins = []string{"https://paysay.onrender.com"}
	cfg.CollectionsToken = "a-sufficiently-long-secret"
	srv := New(Options{Config: cfg, Agent: &stubAgent{}})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/internal/collections", strings.NewReader(`{"chase":[],"skipped":[]}`))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Authorization", "Bearer a-sufficiently-long-secret")
	srv.Handler().ServeHTTP(rr, req)
	// A foreign Origin must NOT cause a 403 here; the bearer token governs it.
	if rr.Code == http.StatusForbidden {
		t.Errorf("internal collections should not be origin-checked, got 403")
	}
}

// --- item 2: invoice rate limit, shared global cap, and cache ---

func TestInvoicesRateLimited(t *testing.T) {
	t.Setenv("RATE_LIMIT_PER_MINUTE", "100") // high so the 30/min invoice cap bites first
	lister := &stubLister{resp: `{"items":[]}`}
	srv := testServerWithInvoices(t, lister)
	h := srv.Handler()

	status := func() int {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/invoices", nil)
		req.Header.Set("CF-Connecting-IP", "203.0.113.50")
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	for i := 0; i < invoiceRateLimitPerMinute; i++ {
		if code := status(); code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i, code)
		}
	}
	if code := status(); code != http.StatusTooManyRequests {
		t.Errorf("over-limit request = %d, want 429", code)
	}
}

func TestSharedGlobalHourlyCap(t *testing.T) {
	g := newGlobalWindow(2)
	chat := newRateLimiterShared(100, g)
	inv := newRateLimiterShared(100, g)
	tm := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	if !chat.allow("a", tm) {
		t.Fatal("first request should pass")
	}
	if !inv.allow("b", tm) {
		t.Fatal("second request (different limiter, same global) should pass")
	}
	// The shared global cap of 2 is now exhausted across both limiters.
	if chat.allow("c", tm) {
		t.Error("third request should be blocked by the shared global cap")
	}
}

func TestInvoiceCacheServesWithoutHittingProvider(t *testing.T) {
	lister := &countingLister{resp: `{"items":[{"id":"INV2-A","status":"SENT","recipient":"a@x.com","currency_code":"EUR","total":"1.00","create_time":"2026-11-01T00:00:00Z"}]}`}
	srv := testServerWithInvoices(t, lister)
	h := srv.Handler()
	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/invoices", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
	}
	if lister.calls != 1 {
		t.Errorf("provider called %d times, want 1 (cache should serve the rest)", lister.calls)
	}
}

func TestInvoiceCacheInvalidate(t *testing.T) {
	c := newInvoiceCache(5 * time.Second)
	t0 := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	c.put("", "payload", t0)
	if v, ok := c.get("", t0.Add(time.Second)); !ok || v != "payload" {
		t.Fatalf("cache miss within TTL: %q %v", v, ok)
	}
	if _, ok := c.get("", t0.Add(6*time.Second)); ok {
		t.Error("entry should expire after TTL")
	}
	c.put("", "payload", t0)
	c.invalidate()
	if _, ok := c.get("", t0.Add(time.Second)); ok {
		t.Error("invalidate should drop cached entries")
	}
}

// countingLister records how many times it was invoked.
type countingLister struct {
	resp  string
	calls int
}

func (c *countingLister) Invoke(_ context.Context, _ string, _ json.RawMessage) (toolResult, error) {
	c.calls++
	return toolResult{Text: c.resp}, nil
}

// --- item 3: readiness gate (paypal_connecting) and config readiness ---

func TestPayPalConnectingReturns503(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	srv := New(Options{Config: cfg, Agent: &stubAgent{}, PayPalConnecting: true})
	h := srv.Handler()

	for _, path := range []string{"/api/invoices", "/api/invoices/INV2-1/qr"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "paypal_connecting") {
			t.Errorf("%s body = %s, want paypal_connecting", path, rr.Body.String())
		}
	}

	// /api/config reports not-ready while connecting.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var cfgResp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &cfgResp)
	if ready, _ := cfgResp["paypal_ready"].(bool); ready {
		t.Error("paypal_ready should be false while connecting")
	}

	// Healthz is 200 regardless.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want 200 while PayPal connects", rr.Code)
	}
}

func TestSetInvoicesFlipsGate(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	srv := New(Options{Config: cfg, Agent: &stubAgent{}, PayPalConnecting: true})
	srv.SetInvoices(&stubLister{resp: `{"items":[]}`})

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/invoices", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("after SetInvoices status = %d, want 200", rr.Code)
	}
}

func TestConfirmReturnsConnectingWhileNotReady(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	srv := New(Options{Config: cfg, Agent: pendingAgent(), PayPalConnecting: true, ApprovalTools: []string{"send_invoice"}})
	h := srv.Handler()
	id, cookie := proposeSend(t, h)
	rr := confirm(h, id, cookie)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "paypal_connecting") {
		t.Errorf("confirm while connecting = %d body %s, want 503 paypal_connecting", rr.Code, rr.Body.String())
	}
}

// --- item 14: confirm allow-list (defence in depth) ---

func TestConfirmRefusesToolOutsideAllowList(t *testing.T) {
	// The agent proposes a tool that is not in the server's approval allow-list.
	ag := &stubAgent{result: &agent.Result{Pending: []agent.ToolCall{
		{Name: "accept_dispute", Input: json.RawMessage(`{"id":"X"}`)},
	}}}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	inv := &recordingInvoker{}
	srv := New(Options{Config: cfg, Agent: ag, Invoices: inv, ApprovalTools: []string{"send_invoice"}})
	h := srv.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"go"}`)))
	var resp chatResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Pending == nil {
		t.Fatal("expected a pending action")
	}
	rr = confirm(h, resp.Pending.ID, rr.Result().Cookies()[0])
	if rr.Code != http.StatusOK {
		t.Fatalf("confirm status = %d", rr.Code)
	}
	if len(inv.ran) != 0 {
		t.Errorf("a non-allow-list tool ran: %v", inv.ran)
	}
	if !strings.Contains(rr.Body.String(), "not permitted") {
		t.Errorf("body = %s, want a not-permitted error", rr.Body.String())
	}
}

// --- item 12: memory sweep and session cap ---

func TestPendingActionsSweepExpired(t *testing.T) {
	p := newPendingActions()
	start := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	p.add("s1", []agent.ToolCall{{Name: "send_invoice"}}, start)
	p.add("s2", []agent.ToolCall{{Name: "send_invoice"}}, start)

	p.sweepExpired(start.Add(pendingTTL + time.Minute))

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.bySession) != 0 {
		t.Errorf("sweep left %d sessions, want 0", len(p.bySession))
	}
}

func TestCollectionsStoreSweepExpired(t *testing.T) {
	s := newCollectionsStore()
	s.set(&collectionsPlan{Chase: []collectionsChaseInvoice{{ID: "INV2-A", Customer: "c", Amount: "1", Currency: "EUR"}}})
	start := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	s.approve("sess", func([]string) string { return "pid" }, start)

	s.mu.Lock()
	claims := len(s.claims)
	s.mu.Unlock()
	if claims == 0 {
		t.Fatal("expected a claim after approve")
	}

	s.sweepExpired(start.Add(pendingTTL + time.Minute))
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.claims) != 0 {
		t.Errorf("sweep left %d claims, want 0", len(s.claims))
	}
}

func TestSessionInvoicesEvictsOldest(t *testing.T) {
	s := newSessionInvoices()
	call := func(id string) []agent.ToolCall {
		return []agent.ToolCall{{Name: "create_invoice", Output: `{"id":"` + id + `"}`}}
	}
	// Insert one more than the cap.
	for i := 0; i <= maxTrackedSessions; i++ {
		s.recordCreated(fmt.Sprintf("session-%d", i), call(fmt.Sprintf("INV2-%d", i)))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.byID) > maxTrackedSessions {
		t.Errorf("tracked sessions = %d, want <= %d", len(s.byID), maxTrackedSessions)
	}
	if _, ok := s.byID["session-0"]; ok {
		t.Error("oldest session should have been evicted")
	}
}
