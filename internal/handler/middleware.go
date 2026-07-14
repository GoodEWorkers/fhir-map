package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Middleware chains multiple HTTP middleware functions.
func Middleware(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

// MaxBodyBytesMiddleware enforces a global request body size limit on every
// endpoint. It uses http.MaxBytesReader, which is idiomatic Go: the reader
// tells the HTTP server to stop draining the TCP socket once the limit is
// hit, rather than buffering the whole body first.
//
// When any handler subsequently reads r.Body and the limit is exceeded,
// the read returns *http.MaxBytesError. Handlers that use json.NewDecoder
// will surface this as a decode error — those handlers detect *http.MaxBytesError
// and respond with 413 instead of 400. Handlers that use io.ReadAll (the
// StructureMap family) no longer need their own LimitReader because this
// middleware covers them uniformly. Configure via SERVER_MAX_BODY_BYTES.
func MaxBodyBytesMiddleware(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// IsBodyTooLarge reports whether err originated from http.MaxBytesReader
// exceeding its limit. Use this in handlers that need to distinguish a
// body-size violation from other decode errors.
func IsBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

// WriteBodyTooLargeResponse writes a 413 FHIR OperationOutcome. Extracted
// here so all handlers use the same response shape.
func WriteBodyTooLargeResponse(w http.ResponseWriter) {
	outcome := map[string]any{
		"resourceType": "OperationOutcome",
		"issue": []map[string]any{{
			"severity":    "error",
			"code":        "too-costly",
			"diagnostics": "Request body exceeds the maximum allowed size. Split the request into smaller payloads or contact the server operator to raise the limit.",
		}},
	}
	w.Header().Set("Content-Type", "application/fhir+json")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	_ = json.NewEncoder(w).Encode(outcome)
}

// RequestIDMiddleware adds a unique request ID to each request for traceability.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
	})
}

// LoggingMiddleware logs HTTP requests while protecting PHI: info log carries path only (never query string);
// debug emits request_uri (PHI-unsafe, must be off in production); client_ip via extractClientIP
// validates against trustedProxies to prevent X-Forwarded-For spoofing.
// Pass nil for trustedProxies in tests or direct-mode deployments.
func LoggingMiddleware(logger *slog.Logger, trustedProxies []net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)
			// PHI boundary: log path only; query string suppresses GET params like ?code= which carry medical codes.
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", wrapped.statusCode,
				"duration_ms", duration.Milliseconds(),
				"request_id", w.Header().Get("X-Request-ID"),
				"client_ip", extractClientIP(r, trustedProxies),
			)
			// Debug level includes request_uri (query string) and user_agent (may contain MRNs/tokens) — PHI-unsafe.
			logger.Debug("request_detail",
				"request_uri", r.URL.RequestURI(),
				"user_agent", r.Header.Get("User-Agent"),
				"content_length", r.ContentLength,
				"response_size", wrapped.bytesWritten,
			)
		})
	}
}

// extractClientIP resolves the request's originating client IP under the
// proxy-trust model documented on LoggingMiddleware. Returns the host portion
// of r.RemoteAddr when trusted is empty OR when the immediate peer is NOT in
// the trusted list (prevents X-Forwarded-For spoofing from untrusted clients).
// When the peer IS trusted, it consults X-Forwarded-For (leftmost entry) then
// X-Real-IP, falling back to the peer host.
func extractClientIP(r *http.Request, trusted []net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if len(trusted) == 0 {
		return host
	}
	ip := net.ParseIP(host)
	if ip == nil || !ipInCIDRList(ip, trusted) {
		return host
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	return host
}

func ipInCIDRList(ip net.IP, list []net.IPNet) bool {
	for _, cidr := range list {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// NewCORSMiddleware returns CORS middleware configured against an allow-list.
//
// allowedOrigins is the raw value of CORS_ALLOWED_ORIGINS:
//   - "" (empty) — emit `Access-Control-Allow-Origin: *` on every response
//     (legacy development behaviour; not suitable for HIPAA production).
//   - non-empty — comma-separated allow-list. The request `Origin` header
//     is matched against the list; on hit, ACAO echoes the specific origin;
//     on miss, NO CORS headers are emitted (a blocked preflight must not
//     advertise allowed methods or headers).
//
// Preflight (OPTIONS) requests short-circuit with 204 in both arms.
func NewCORSMiddleware(allowedOrigins string) func(http.Handler) http.Handler {
	allowed := parseCORSOrigins(allowedOrigins)
	if len(allowed) == 0 && allowedOrigins != "" {
		slog.Warn("CORS_ALLOWED_ORIGINS is non-empty but yielded no valid origins; wildcard mode active", "value", allowedOrigins)
	}
	if len(allowed) == 1 && allowed[0] == "*" {
		allowed = nil
	}
	wildcard := len(allowed) == 0
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			switch {
			case wildcard:
				writeCORSHeaders(w, "*")
			case isCORSOriginAllowed(origin, allowed):
				writeCORSHeaders(w, origin)
			}
			// On whitelist miss, no CORS headers (a blocked preflight never reaches handler anyway).

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeCORSHeaders sets the four CORS headers as a group. Called only when
// the request's origin is allowed (wildcard or matched whitelist entry);
// the headers are co-emitted to keep the response coherent.
func writeCORSHeaders(w http.ResponseWriter, allowOrigin string) {
	w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, X-Request-ID")
	w.Header().Set("Access-Control-Expose-Headers", "Location, ETag, X-Request-ID")
	if allowOrigin != "*" {
		w.Header().Add("Vary", "Origin")
	}
}

// parseCORSOrigins splits a comma-separated origin list, trimming whitespace
// and dropping empty entries. Returns nil for an empty input (signals
// wildcard mode to the caller).
func parseCORSOrigins(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isCORSOriginAllowed reports whether origin matches an entry in allowed.
// An empty request origin never matches (browsers always send Origin on
// cross-origin requests; an empty Origin means same-origin or non-CORS).
func isCORSOriginAllowed(origin string, allowed []string) bool {
	if origin == "" {
		return false
	}
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	return false
}

// SecurityHeadersMiddleware adds security-related HTTP headers for defense-in-depth.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code and
// the number of bytes written to the body (used by LoggingMiddleware's debug
// `response_size` field).
type responseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += n
	return n, err
}

// Flush forwards to the underlying http.Flusher when supported, so that
// type assertions on the wrapped writer (e.g. w.(http.Flusher)) succeed and
// streaming/SSE handlers can flush without bypassing this wrapper.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
