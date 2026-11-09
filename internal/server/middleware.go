package server

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxBodyBytes caps request bodies to protect the server from oversized
// payloads. Chat messages and TTS text are small; 1 MiB is generous.
const maxBodyBytes = 1 << 20

// statusRecorder captures the response status code for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// withRecover converts panics into 500s so one bad request cannot crash the
// server process.
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic recovered", "panic", rec, "path", r.URL.Path)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withLogging emits a structured access log line per request. It deliberately
// logs only method, path, status, and duration, never request bodies, which may
// contain customer details.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.LogAttrs(r.Context(), slog.LevelInfo, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

// withSecurityHeaders applies conservative security headers. The CSP allows the
// page's own scripts/styles and the audio blobs the UI creates, and forbids
// framing to mitigate clickjacking. HSTS is set only on TLS requests so a plain
// HTTP local run is not pinned to HTTPS.
func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; "+
				"media-src 'self' blob:; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		// The microphone is used by the voice UI (same-origin only); camera and
		// geolocation are never used.
		h.Set("Permissions-Policy", "microphone=(self), camera=(), geolocation=()")
		// Only advertise HSTS when the request actually arrived over TLS, either
		// directly or via the proxy's X-Forwarded-Proto, so a plain-HTTP local
		// run is not pinned to HTTPS.
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// withCORS applies the configured CORS allow-list. When the allow-list is the
// wildcard, any origin is echoed (suitable for local demos); otherwise only
// listed origins are permitted.
func (s *Server) withCORS(next http.Handler) http.Handler {
	allowed := make(map[string]bool)
	wildcard := false
	for _, o := range s.cfg.Server.AllowedOrigins {
		if o == "*" {
			wildcard = true
		}
		allowed[o] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (wildcard || allowed[origin]) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withBodyLimit enforces the maximum request body size.
func (s *Server) withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// requireOrigin guards a state-changing POST against cross-site requests. A
// browser always sends an Origin header on a cross-origin POST, so:
//   - No Origin header: allowed. Non-browser clients (the collections job, curl)
//     do not send one, and this check is not the auth boundary for them.
//   - Origin present and an allow-list is configured: it must be in the list.
//   - Origin present and no allow-list (wildcard): it must be same-host as the
//     request, so another site cannot drive the API from a victim's browser.
//
// It is deliberately NOT applied to /api/internal/collections, which is
// authenticated with a bearer token instead.
func (s *Server) requireOrigin(next http.HandlerFunc) http.HandlerFunc {
	allowed := make(map[string]bool)
	wildcard := false
	for _, o := range s.cfg.Server.AllowedOrigins {
		if o == "*" {
			wildcard = true
		}
		allowed[o] = true
	}
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !s.originAllowed(origin, r, allowed, wildcard) {
			s.log.Warn("rejected cross-origin request", "origin", origin, "path", r.URL.Path)
			writeError(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		next(w, r)
	}
}

// originAllowed reports whether the given Origin is acceptable for a
// state-changing request. With an explicit allow-list, the origin must be
// listed. With only the wildcard, the origin's host must match the request's
// host (same-site).
func (s *Server) originAllowed(origin string, r *http.Request, allowed map[string]bool, wildcard bool) bool {
	if !wildcard {
		return allowed[origin]
	}
	if allowed[origin] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
