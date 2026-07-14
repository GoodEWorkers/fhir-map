package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/goodeworkers/fhir-map/internal/transform"
)

// Prometheus metrics for the $transform component: throughput, latency, error classification.
// Labels are bounded (result ∈ {success,error}; code ∈ the fixed FHIR issue-code set).
var (
	transformTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "fhirmap_transform_total",
		Help: "Total StructureMap $transform operations, labelled by outcome (result, FHIR issue code).",
	}, []string{"result", "code"})

	transformDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "fhirmap_transform_duration_seconds",
		Help:    "StructureMap $transform execution latency in seconds.",
		Buckets: prometheus.DefBuckets,
	})

	// HTTP RED metrics by route template (r.Pattern), never concrete path: PHI-safe,
	// low-cardinality, bounded by registered routes. Unmatched requests bucket under "unmatched".
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "fhirmap_http_requests_total",
		Help: "Total HTTP requests by method, matched route template, and status.",
	}, []string{"method", "route", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "fhirmap_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds by method and matched route template.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
)

// MetricsMiddleware records RED metrics for every route;
// reads r.Pattern after mux sets the matched template, falls back to "unmatched" for 404s.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		httpRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(wrapped.statusCode)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

// MetricsHandler is the Prometheus scrape handler (default registry); mount at
// GET /metrics.
func MetricsHandler() http.Handler { return promhttp.Handler() }

// recordTransform observes one $transform operation: latency and success/error counter labelled by FHIR issue code.
func recordTransform(seconds float64, err error) {
	transformDuration.Observe(seconds)
	if err == nil {
		transformTotal.WithLabelValues("success", "ok").Inc()
		return
	}
	transformTotal.WithLabelValues("error", transformErrorCode(err)).Inc()
}

// transformErrorCode maps a transform error to its FHIR issue code (bounded label set, mirrors handleTransformError).
func transformErrorCode(err error) string {
	switch {
	case errors.Is(err, transform.ErrInputTypeMismatch), errors.Is(err, transform.ErrInputInvalid):
		return "invalid"
	case errors.Is(err, transform.ErrMapNotFound), errors.Is(err, transform.ErrTranslateNoMatch):
		return "not-found"
	case errors.Is(err, transform.ErrRecursionLimit):
		return "too-costly"
	case errors.Is(err, transform.ErrTransformCanceled):
		return "timeout"
	case errors.Is(err, transform.ErrCheckFailed), errors.Is(err, transform.ErrTargetListSingle):
		return "invariant"
	case errors.Is(err, transform.ErrNonConformantCoercion):
		return "value"
	default:
		return "exception"
	}
}
