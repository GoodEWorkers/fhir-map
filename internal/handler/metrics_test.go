package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/internal/transform"
)

// TestMetrics_Transform_IncrementsSuccessCounter uses delta-based assertions for determinism across tests sharing the default registry.
func TestMetrics_Transform_IncrementsSuccessCounter(t *testing.T) {
	before := testutil.ToFloat64(transformTotal.WithLabelValues("success", "ok"))

	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()
	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "sourceMap", "resource": qrToPatientInlineMap()},
			map[string]any{"name": "content", "resource": qrToPatientContent()},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	after := testutil.ToFloat64(transformTotal.WithLabelValues("success", "ok"))
	assert.Equal(t, before+1, after, "a successful transform must increment fhirmap_transform_total{success,ok}")
}

// TestMetrics_HTTPMiddleware_RecordsByRouteTemplate records metrics by route template (PHI-safe, low-cardinality) not concrete path/IDs.
func TestMetrics_HTTPMiddleware_RecordsByRouteTemplate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fhir/R5/StructureMap/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Middleware(mux, MetricsMiddleware)

	before := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", "GET /fhir/R5/StructureMap/{id}", "200"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/fhir/R5/StructureMap/abc123", nil))
	after := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", "GET /fhir/R5/StructureMap/{id}", "200"))
	assert.Equal(t, before+1, after, "labelled by the route template, not the concrete id")
}

// TestMetrics_HTTPMiddleware_UnmatchedBucketed prevents 404 spam from blowing up cardinality by bucketing under "unmatched".
func TestMetrics_HTTPMiddleware_UnmatchedBucketed(t *testing.T) {
	mux := http.NewServeMux()
	h := Middleware(mux, MetricsMiddleware)
	before := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", "unmatched", "404"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/no/such/route/xyz", nil))
	after := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", "unmatched", "404"))
	assert.Equal(t, before+1, after)
}

func TestMetrics_Endpoint_ServesTransformMetrics(t *testing.T) {
	recordTransform(0.01, nil)

	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.True(t, strings.Contains(body, "fhirmap_transform_total"), "metrics must expose fhirmap_transform_total")
	assert.True(t, strings.Contains(body, "fhirmap_transform_duration_seconds"), "metrics must expose the duration histogram")
}

// TestMetrics_Transform_RecordsErrorCode ensures errors are captured during Transform execution, not pre-execution request/map resolution.
func TestMetrics_Transform_RecordsErrorCode(t *testing.T) {
	before := testutil.ToFloat64(transformTotal.WithLabelValues("error", "value"))

	repo := newSMInMemoryRepo()
	service := structuremap.NewService(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := transform.New(transform.WithStrictTransform(true))
	mux := http.NewServeMux()
	h := NewStructureMapHandler(service, "http://localhost", logger).WithTransformEngine(eng)
	h.RegisterRoutes(mux)
	h.RegisterRoutesAtPrefix(mux, "R5")
	ts := httptest.NewServer(Middleware(mux, MaxBodyBytesMiddleware(10<<20)))
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "sourceMap", "resource": strictCoercionMap()},
			map[string]any{"name": "content", "resource": map[string]any{"resourceType": "Basic", "value": "not-a-date"}},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

	after := testutil.ToFloat64(transformTotal.WithLabelValues("error", "value"))
	assert.Equal(t, before+1, after, "a strict coercion failure must increment fhirmap_transform_total{error,value}")
}
