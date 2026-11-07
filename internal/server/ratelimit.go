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
// comes from X-Forwarded-For, which a caller can spoof; the global limit is
// the backstop that holds regardless. Zero disables a limit.
type rateLimiter struct {
	perClientPerMinute int
	globalPerHour      int

	mu          sync.Mutex
	minute      time.Time
	clients     map[string]int
	hour        time.Time
	globalCount int
}

func newRateLimiter(perClientPerMinute, globalPerHour int) *rateLimiter {
	return &rateLimiter{
		perClientPerMinute: perClientPerMinute,
		globalPerHour:      globalPerHour,
		clients:            make(map[string]int),
	}
}

// allow records a request from key at time t and reports whether it is within
// both limits. Fixed windows keep memory bounded: the client map is dropped
// every minute.
func (l *rateLimiter) allow(key string, t time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if m := t.Truncate(time.Minute); !m.Equal(l.minute) {
		l.minute = m
		l.clients = make(map[string]int)
	}
	if h := t.Truncate(time.Hour); !h.Equal(l.hour) {
		l.hour = h
		l.globalCount = 0
	}

	if l.perClientPerMinute > 0 && l.clients[key] >= l.perClientPerMinute {
		return false
	}
	if l.globalPerHour > 0 && l.globalCount >= l.globalPerHour {
		return false
	}
	l.clients[key]++
	l.globalCount++
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

// clientKey identifies the caller: the first X-Forwarded-For entry when behind
// a proxy (as on Render), otherwise the connection's remote IP.
func clientKey(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		if ip := strings.TrimSpace(first); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
