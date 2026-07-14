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

// strictCoercionMap maps src.value through toDateTime, which fails on non-date values.
func strictCoercionMap() map[string]any {
	return map[string]any{
		"resourceType": "StructureMap", "url": "http://example.org/sm/strict-coercion",
		"name": "StrictCoercion", "status": "active",
		"group": []any{map[string]any{
			"name":  "g",
			"input": []any{map[string]any{"name": "src", "mode": "source"}, map[string]any{"name": "tgt", "mode": "target"}},
			"rule": []any{map[string]any{
				"name":   "r",
				"source": []any{map[string]any{"context": "src", "element": "value", "variable": "v"}},
				"target": []any{map[string]any{"context": "tgt", "element": "out", "transform": "toDateTime", "parameter": []any{map[string]any{"valueId": "v"}}}},
			}},
		}},
	}
}

// TestHandler_Transform_Strict_CoercionFailure_Returns422Value verifies that a strict engine surfaces coercion failures as 422 OperationOutcome with PHI-safe diagnostics.
func TestHandler_Transform_Strict_CoercionFailure_Returns422Value(t *testing.T) {
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
	raw, _ := io.ReadAll(resp.Body)

	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, "strict coercion failure must be 422; body=%s", raw)
	var oo map[string]any
	require.NoError(t, json.Unmarshal(raw, &oo))
	assert.Equal(t, "OperationOutcome", oo["resourceType"])
	issue := oo["issue"].([]any)[0].(map[string]any)
	assert.Equal(t, "value", issue["code"])
	assert.False(t, strings.Contains(string(raw), "not-a-date"), "OperationOutcome must not leak the source value (PHI-safe)")
}
