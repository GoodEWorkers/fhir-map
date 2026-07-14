package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loggerToBuffer builds a slog.Logger writing JSON to buf at the given level.
func loggerToBuffer(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})).
		With("service", "fhir-map", "env", "test")
}

// newTestServer wires Middleware(RequestID + Logging(direct mode)) around h.
func newTestServer(t *testing.T, logger *slog.Logger, h http.Handler) *httptest.Server {
	t.Helper()
	return httptest.NewServer(Middleware(h, RequestIDMiddleware, LoggingMiddleware(logger, nil)))
}

// parseLogRecords splits JSONL buf into one map per line. Blank lines skipped.
func parseLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("invalid JSON log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func filterMsg(records []map[string]any, msg string) []map[string]any {
	out := []map[string]any{}
	for _, r := range records {
		if v, ok := r["msg"].(string); ok && v == msg {
			out = append(out, r)
		}
	}
	return out
}

// hit performs a GET against ts.URL+pathAndQuery and drains the body so the
// middleware's response_size counter sees the full payload.
func hit(t *testing.T, ts *httptest.Server, pathAndQuery string) {
	t.Helper()
	resp, err := http.Get(ts.URL + pathAndQuery)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func TestLogging_InfoLevel_AccessLogPresent(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := loggerToBuffer(buf, slog.LevelInfo)
	ts := newTestServer(t, logger, okHandler())
	defer ts.Close()

	hit(t, ts, "/ping")

	records := filterMsg(parseLogRecords(t, buf), "request")
	assert.Len(t, records, 1, "Info level must emit exactly one msg=request entry")
}

func TestLogging_InfoLevel_QueryStringAbsent(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := loggerToBuffer(buf, slog.LevelInfo)
	ts := newTestServer(t, logger, okHandler())
	defer ts.Close()

	hit(t, ts, "/?code=J12.89&system=http://hl7.org/fhir/sid/icd-10")

	rawLog := buf.String()
	assert.NotContains(t, rawLog, "J12.89", "PHI-adjacent code must not leak into Info logs")
	assert.NotContains(t, rawLog, "icd-10", "system URI must not leak into Info logs")

	records := filterMsg(parseLogRecords(t, buf), "request")
	require.Len(t, records, 1)
	path, _ := records[0]["path"].(string)
	assert.NotContains(t, path, "?", "path field must not contain query separator")
}

func TestLogging_InfoLevel_RequiredFields(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := loggerToBuffer(buf, slog.LevelInfo)
	ts := newTestServer(t, logger, okHandler())
	defer ts.Close()

	hit(t, ts, "/ping")

	records := filterMsg(parseLogRecords(t, buf), "request")
	require.Len(t, records, 1)
	rec := records[0]

	for _, key := range []string{"service", "env", "method", "path", "status", "duration_ms", "request_id", "client_ip"} {
		_, ok := rec[key]
		assert.True(t, ok, "Info record must include %q", key)
	}
	_, hasURI := rec["request_uri"]
	assert.False(t, hasURI, "Info record must NOT include request_uri (debug-only)")
	_, hasQuery := rec["query"]
	assert.False(t, hasQuery, "Info record must NOT include query (suppressed)")
}

func TestLogging_DebugLevel_DetailAlwaysPresent(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := loggerToBuffer(buf, slog.LevelDebug)
	ts := newTestServer(t, logger, okHandler())
	defer ts.Close()

	hit(t, ts, "/ping")

	records := parseLogRecords(t, buf)
	assert.Len(t, filterMsg(records, "request"), 1)
	assert.Len(t, filterMsg(records, "request_detail"), 1)
}

// Debug level: query strings appear in request_uri (explicit PHI-risk trade-off).
func TestLogging_DebugLevel_QueryStringPresent(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := loggerToBuffer(buf, slog.LevelDebug)
	ts := newTestServer(t, logger, okHandler())
	defer ts.Close()

	hit(t, ts, "/?code=J12.89")

	details := filterMsg(parseLogRecords(t, buf), "request_detail")
	require.Len(t, details, 1)
	uri, _ := details[0]["request_uri"].(string)
	assert.Contains(t, uri, "J12.89", "debug-level request_uri must include query string")
}

func TestLogging_ErrorLevel_NoAccessLog(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := loggerToBuffer(buf, slog.LevelError)
	ts := newTestServer(t, logger, okHandler())
	defer ts.Close()

	hit(t, ts, "/ping")

	records := filterMsg(parseLogRecords(t, buf), "request")
	assert.Len(t, records, 0, "Error level must suppress access log")
}

func TestLogging_WarnLevel_NoAccessLog(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := loggerToBuffer(buf, slog.LevelWarn)
	ts := newTestServer(t, logger, okHandler())
	defer ts.Close()

	hit(t, ts, "/ping")

	records := filterMsg(parseLogRecords(t, buf), "request")
	assert.Len(t, records, 0, "Warn level must suppress access log")
}

// The level gate never suppresses actual error-level events.
func TestLogging_ErrorLevel_ErrorsStillLogged(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := loggerToBuffer(buf, slog.LevelError)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Error("translate error", "code", "OPAQUE")
		w.WriteHeader(http.StatusInternalServerError)
	})
	ts := newTestServer(t, logger, inner)
	defer ts.Close()

	hit(t, ts, "/boom")

	records := parseLogRecords(t, buf)
	errs := filterMsg(records, "translate error")
	assert.Len(t, errs, 1, "error-level events must always be logged")
}

func TestExtractClientIP_DirectMode(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.5:54321"
	got := extractClientIP(r, nil)
	assert.Equal(t, "203.0.113.5", got)
}

func TestExtractClientIP_TrustedProxy_XForwardedFor(t *testing.T) {
	_, ipnet, err := net.ParseCIDR("10.0.0.0/8")
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:9999"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	got := extractClientIP(r, []net.IPNet{*ipnet})
	assert.Equal(t, "203.0.113.5", got)
}

func TestExtractClientIP_TrustedProxy_XRealIP(t *testing.T) {
	_, ipnet, err := net.ParseCIDR("10.0.0.0/8")
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:9999"
	r.Header.Set("X-Real-IP", "203.0.113.7")
	got := extractClientIP(r, []net.IPNet{*ipnet})
	assert.Equal(t, "203.0.113.7", got)
}

func TestExtractClientIP_UntrustedRemote(t *testing.T) {
	_, ipnet, err := net.ParseCIDR("10.0.0.0/8")
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "8.8.8.8:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.99")
	got := extractClientIP(r, []net.IPNet{*ipnet})
	assert.Equal(t, "8.8.8.8", got, "untrusted peer must not be able to spoof XFF")
}
