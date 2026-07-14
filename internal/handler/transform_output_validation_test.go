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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/internal/transform"
)

// Unit: the structural validator

func TestStructuralOutputValidator(t *testing.T) {
	v := NewStructuralOutputValidator()

	t.Run("valid resource passes", func(t *testing.T) {
		assert.Empty(t, v.ValidateOutput(map[string]any{"resourceType": "Patient", "id": "1"}))
	})

	t.Run("missing resourceType is an error", func(t *testing.T) {
		issues := v.ValidateOutput(map[string]any{"x": "hello"})
		require.Len(t, issues, 1)
		assert.Equal(t, "structure", issues[0].Code)
		assert.Equal(t, "error", issues[0].Severity)
		assert.Contains(t, issues[0].Detail, "missing a resourceType")
	})

	t.Run("non-object output is an error", func(t *testing.T) {
		issues := v.ValidateOutput("not-a-resource")
		require.Len(t, issues, 1)
		assert.Equal(t, "structure", issues[0].Code)
	})

	t.Run("HL7v2 shape is exempt", func(t *testing.T) {
		assert.Empty(t, v.ValidateOutput(map[string]any{"resourceType": "HL7v2", "MSH-9": "ORU^R01"}))
		assert.Empty(t, v.ValidateOutput(map[string]any{"MSH-9": "ORU^R01"}), "detected via MSH- key")
	})

	t.Run("valid Bundle entries pass", func(t *testing.T) {
		bundle := map[string]any{"resourceType": "Bundle", "entry": []any{
			map[string]any{"resource": map[string]any{"resourceType": "Patient"}},
			map[string]any{"resource": map[string]any{"resourceType": "Observation"}},
			map[string]any{"request": map[string]any{"method": "GET"}}, // request-only entry: legal
		}}
		assert.Empty(t, v.ValidateOutput(bundle))
	})

	t.Run("Bundle entry resource missing resourceType is flagged by index", func(t *testing.T) {
		bundle := map[string]any{"resourceType": "Bundle", "entry": []any{
			map[string]any{"resource": map[string]any{"resourceType": "Patient"}},
			map[string]any{"resource": map[string]any{"id": "no-type"}},
		}}
		issues := v.ValidateOutput(bundle)
		require.Len(t, issues, 1)
		assert.Contains(t, issues[0].Detail, "entry[1]")
		assert.Contains(t, issues[0].Detail, "missing a resourceType")
	})
}

// ── End-to-end: the gate via $transform ──────────────────────────────────────

// noResourceTypeMap produces a StructureMap that yields output with no resourceType to test the validator.
func noResourceTypeMap() map[string]any {
	return map[string]any{
		"resourceType": "StructureMap", "url": "http://example.org/sm/no-rt",
		"name": "NoRT", "status": "active",
		"group": []any{map[string]any{
			"name": "g",
			"input": []any{
				map[string]any{"name": "src", "mode": "source"},
				map[string]any{"name": "tgt", "mode": "target"},
			},
			"rule": []any{map[string]any{
				"name":   "r",
				"source": []any{map[string]any{"context": "src", "element": "value", "variable": "v"}},
				"target": []any{map[string]any{"context": "tgt", "element": "x", "transform": "copy", "parameter": []any{map[string]any{"valueId": "v"}}}},
			}},
		}},
	}
}

func transformOutputValidationServer(t *testing.T, strict bool) *httptest.Server {
	t.Helper()
	repo := newSMInMemoryRepo()
	service := structuremap.NewService(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := transform.New()
	mux := http.NewServeMux()
	h := NewStructureMapHandler(service, "http://localhost", logger).
		WithTransformEngine(eng).
		WithTransformOutputValidation(NewStructuralOutputValidator(), strict)
	h.RegisterRoutes(mux)
	h.RegisterRoutesAtPrefix(mux, "R5")
	return httptest.NewServer(Middleware(mux, MaxBodyBytesMiddleware(10<<20)))
}

func postNoRTTransform(t *testing.T, ts *httptest.Server) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "sourceMap", "resource": noResourceTypeMap()},
			map[string]any{"name": "content", "resource": map[string]any{"resourceType": "Basic", "value": "hello"}},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	return resp
}

func TestHandler_Transform_OutputValidation_Strict_Returns422(t *testing.T) {
	ts := transformOutputValidationServer(t, true)
	defer ts.Close()

	resp := postNoRTTransform(t, ts)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, "invalid output must be rejected; body=%s", raw)
	var oo map[string]any
	require.NoError(t, json.Unmarshal(raw, &oo))
	assert.Equal(t, "OperationOutcome", oo["resourceType"])
	issue := oo["issue"].([]any)[0].(map[string]any)
	assert.Equal(t, "structure", issue["code"])
	assert.Contains(t, issue["diagnostics"], "resourceType")
}

func TestHandler_Transform_OutputValidation_Lenient_FlagsButReturns(t *testing.T) {
	ts := transformOutputValidationServer(t, false)
	defer ts.Close()

	resp := postNoRTTransform(t, ts)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	require.Equal(t, http.StatusOK, resp.StatusCode, "lenient mode still returns the output; body=%s", raw)
	assert.Contains(t, resp.Header.Get("Warning"), "transform output validation", "lenient flags via Warning header")

	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, "hello", out["x"], "the (invalid-but-best-effort) output is still emitted unchanged")
}

func TestHandler_Transform_OutputValidation_Off_NoWarning(t *testing.T) {
	repo := newSMInMemoryRepo()
	service := structuremap.NewService(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := transform.New()
	mux := http.NewServeMux()
	// No WithTransformOutputValidation → gate disabled (default).
	h := NewStructureMapHandler(service, "http://localhost", logger).WithTransformEngine(eng)
	h.RegisterRoutes(mux)
	h.RegisterRoutesAtPrefix(mux, "R5")
	ts := httptest.NewServer(Middleware(mux, MaxBodyBytesMiddleware(10<<20)))
	defer ts.Close()

	resp := postNoRTTransform(t, ts)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Warning"), "gate off → no Warning header")
	assert.False(t, strings.Contains(string(raw), "OperationOutcome"))
}
