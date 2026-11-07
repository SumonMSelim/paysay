package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

func TestRateLimiterPerClient(t *testing.T) {
	l := newRateLimiter(2, 0)

	if !l.allow("a", t0) || !l.allow("a", t0.Add(time.Second)) {
		t.Fatal("first two requests should pass")
	}
	if l.allow("a", t0.Add(2*time.Second)) {
		t.Error("third request in the same minute should be limited")
	}
	if !l.allow("b", t0.Add(2*time.Second)) {
		t.Error("another client should not share the limit")
	}
	if !l.allow("a", t0.Add(time.Minute)) {
		t.Error("limit should reset in the next minute")
	}
}

func TestRateLimiterGlobalCapHoldsAcrossClients(t *testing.T) {
	l := newRateLimiter(0, 3)

	for i, key := range []string{"a", "b", "c"} {
		if !l.allow(key, t0.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("request %d should pass", i)
		}
	}
	if l.allow("d", t0.Add(10*time.Minute)) {
		t.Error("global cap should block a new client within the hour")
	}
	if !l.allow("d", t0.Add(time.Hour)) {
		t.Error("global cap should reset in the next hour")
	}
}

func TestRateLimiterZeroDisables(t *testing.T) {
	l := newRateLimiter(0, 0)
	for i := 0; i < 1000; i++ {
		if !l.allow("a", t0) {
			t.Fatalf("request %d limited with limits disabled", i)
		}
	}
}

func TestClientKey(t *testing.T) {
	cases := []struct {
		name, xff, remote, want string
	}{
		{"forwarded", "203.0.113.7, 10.0.0.1", "10.0.0.2:5000", "203.0.113.7"},
		{"direct", "", "198.51.100.4:5000", "198.51.100.4"},
		{"blank forwarded", " ", "198.51.100.4:5000", "198.51.100.4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
			r.RemoteAddr = c.remote
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := clientKey(r); got != c.want {
				t.Errorf("clientKey = %q, want %q", got, c.want)
			}
		})
	}
}

func TestChatEndpointReturns429OverLimit(t *testing.T) {
	t.Setenv("RATE_LIMIT_PER_MINUTE", "1")
	srv := testServer(t, &stubAgent{}, nil)
	h := srv.Handler()

	send := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi"}`))
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
		h.ServeHTTP(rr, req)
		return rr
	}

	if rr := send(); rr.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rr.Code)
	}
	rr := send()
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("429 response should set Retry-After")
	}
}
