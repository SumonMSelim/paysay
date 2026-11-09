package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateLimiter caps calls to the endpoints that cost money per request (Bedrock
// and Polly), so a public demo URL cannot run up the AWS bill. It combines a
// per-client limit per minute with a global limit per hour. The client key
// comes from clientKey, which prefers Cloudflare's CF-Connecting-IP (which a
// browser cannot forge); the global limit is the backstop that holds
// regardless. Zero disables a limit.
//
// The per-hour global counter lives in a shared globalWindow so several
// limiters (chat, speech, PayPal-backed reads) can all count toward one hourly
// budget rather than each keeping an independent cap.
type rateLimiter struct {
	perClientPerMinute int

	mu      sync.Mutex
	minute  time.Time
	clients map[string]int

	global *globalWindow
}

// globalWindow is a fixed one-hour window counting requests across all clients
// and across every limiter that shares it. A zero limit disables it.
type globalWindow struct {
	mu    sync.Mutex
	limit int
	hour  time.Time
	count int
}

func newGlobalWindow(perHour int) *globalWindow {
	return &globalWindow{limit: perHour}
}

// allow records one request at t and reports whether the hourly cap still has
// room. It resets at each hour boundary.
func (g *globalWindow) allow(t time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if h := t.Truncate(time.Hour); !h.Equal(g.hour) {
		g.hour = h
		g.count = 0
	}
	if g.limit > 0 && g.count >= g.limit {
		return false
	}
	g.count++
	return true
}

func newRateLimiter(perClientPerMinute, globalPerHour int) *rateLimiter {
	return newRateLimiterShared(perClientPerMinute, newGlobalWindow(globalPerHour))
}

// newRateLimiterShared builds a limiter that counts toward the given shared
// global hourly window.
func newRateLimiterShared(perClientPerMinute int, global *globalWindow) *rateLimiter {
	return &rateLimiter{
		perClientPerMinute: perClientPerMinute,
		clients:            make(map[string]int),
		global:             global,
	}
}

// allow records a request from key at time t and reports whether it is within
// both the per-client-per-minute and the shared global-per-hour limits. Fixed
// windows keep memory bounded: the client map is dropped every minute. The
// per-client reservation is taken under the lock and rolled back if the shared
// global cap denies the request, so the two counters stay consistent.
func (l *rateLimiter) allow(key string, t time.Time) bool {
	l.mu.Lock()
	if m := t.Truncate(time.Minute); !m.Equal(l.minute) {
		l.minute = m
		l.clients = make(map[string]int)
	}
	if l.perClientPerMinute > 0 && l.clients[key] >= l.perClientPerMinute {
		l.mu.Unlock()
		return false
	}
	l.clients[key]++
	l.mu.Unlock()

	if !l.global.allow(t) {
		// Roll back the per-client reservation so a global rejection does not
		// permanently consume this client's minute budget.
		l.mu.Lock()
		if l.clients[key] > 0 {
			l.clients[key]--
		}
		l.mu.Unlock()
		return false
	}
	return true
}

// limit wraps a handler so requests over the limit get 429.
func (l *rateLimiter) limit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientKey(r), now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "too many requests, please wait a minute and try again")
			return
		}
		next(w, r)
	}
}

// clientKey identifies the caller for rate limiting. Behind Render (fronted by
// Cloudflare), the trustworthy client IP is CF-Connecting-IP, which Cloudflare
// sets at its edge and a browser cannot forge; it carries a single IP. When
// that header is absent (for example a local run without the proxy), the key
// falls back to the RIGHTMOST X-Forwarded-For hop, which is the address the
// nearest trusted proxy observed and is far harder to spoof than the leftmost
// (client-controlled) entry. Finally it falls back to the connection's remote
// IP.
//
// Source: Cloudflare sets CF-Connecting-IP to the connecting client's address
// on the leg from its edge to the origin, and recommends reading it (or
// True-Client-IP) over X-Forwarded-For because it holds a single, consistent
// value: https://developers.cloudflare.com/fundamentals/reference/http-headers/
// Render routes all web-service traffic through Cloudflare:
// https://render.com/articles/how-render-handles-ddos-attacks
func clientKey(r *http.Request) string {
	if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
		return cf
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip := rightmostForwarded(xff); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rightmostForwarded returns the last non-empty, comma-separated entry of an
// X-Forwarded-For header, trimmed. The rightmost hop is the one appended by the
// proxy closest to this server, so a client cannot control it by sending its
// own X-Forwarded-For.
func rightmostForwarded(xff string) string {
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if ip := strings.TrimSpace(parts[i]); ip != "" {
			return ip
		}
	}
	return ""
}
