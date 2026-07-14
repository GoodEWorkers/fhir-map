package handler

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddleware_RequestIDMiddleware_UsesExistingHeader(t *testing.T) {
	h := RequestIDMiddleware(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "my-request-id")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, "my-request-id", w.Header().Get("X-Request-ID"))
}

func TestMiddleware_RequestIDMiddleware_GeneratesID(t *testing.T) {
	h := RequestIDMiddleware(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.NotEmpty(t, w.Header().Get("X-Request-ID"))
}

func TestMiddleware_LoggingMiddleware_RecordsStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	h := LoggingMiddleware(logger, nil)(inner)
	req := httptest.NewRequest(http.MethodPost, "/fhir/ConceptMap", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestMiddleware_CORSMiddleware_Options(t *testing.T) {
	h := NewCORSMiddleware("")(okHandler())
	req := httptest.NewRequest(http.MethodOptions, "/fhir/ConceptMap", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestMiddleware_CORSMiddleware_Regular(t *testing.T) {
	h := NewCORSMiddleware("")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/fhir/ConceptMap", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.NotEmpty(t, w.Header().Get("Access-Control-Allow-Methods"))
}

func TestCORSMiddleware_WhitelistMatch(t *testing.T) {
	h := NewCORSMiddleware("https://app.hospital.org")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/fhir/ConceptMap", nil)
	req.Header.Set("Origin", "https://app.hospital.org")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "https://app.hospital.org", w.Header().Get("Access-Control-Allow-Origin"))
	assert.NotEmpty(t, w.Header().Get("Access-Control-Allow-Methods"))
}

func TestCORSMiddleware_WhitelistMiss(t *testing.T) {
	h := NewCORSMiddleware("https://app.hospital.org")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/fhir/ConceptMap", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Methods"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Headers"))
	assert.Empty(t, w.Header().Get("Access-Control-Expose-Headers"))
}

func TestCORSMiddleware_WhitelistMultipleOrigins(t *testing.T) {
	mw := NewCORSMiddleware("https://app.hospital.org, https://admin.hospital.org")
	for _, origin := range []string{"https://app.hospital.org", "https://admin.hospital.org"} {
		req := httptest.NewRequest(http.MethodGet, "/fhir/ConceptMap", nil)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		mw(okHandler()).ServeHTTP(w, req)
		assert.Equal(t, origin, w.Header().Get("Access-Control-Allow-Origin"),
			"origin %q should be echoed", origin)
	}
}

func TestCORSMiddleware_Preflight_WhitelistMatch(t *testing.T) {
	h := NewCORSMiddleware("https://app.hospital.org")(okHandler())
	req := httptest.NewRequest(http.MethodOptions, "/fhir/ConceptMap", nil)
	req.Header.Set("Origin", "https://app.hospital.org")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "https://app.hospital.org", w.Header().Get("Access-Control-Allow-Origin"))
}

// Blocked preflights must not advertise allowed methods.
func TestCORSMiddleware_Preflight_WhitelistMiss(t *testing.T) {
	h := NewCORSMiddleware("https://app.hospital.org")(okHandler())
	req := httptest.NewRequest(http.MethodOptions, "/fhir/ConceptMap", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Methods"))
}

func TestMiddleware_SecurityHeadersMiddleware(t *testing.T) {
	h := SecurityHeadersMiddleware(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Header().Get("Strict-Transport-Security"), "max-age=")
}

func TestResponseWriter_Flush_ImplementsFlusher(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec, statusCode: http.StatusOK}
	f, ok := (http.ResponseWriter)(rw).(http.Flusher)
	if !ok {
		t.Fatal("*responseWriter must implement http.Flusher")
	}
	f.Flush()
	if !rec.Flushed {
		t.Error("Flush() must delegate to the underlying ResponseWriter's Flusher")
	}
}

func TestMiddleware_Chain(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := Middleware(mux,
		RequestIDMiddleware,
		LoggingMiddleware(logger, nil),
		NewCORSMiddleware(""),
		SecurityHeadersMiddleware,
	)
	ts := httptest.NewServer(wrapped)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/ping")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("X-Request-ID"))
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
}
