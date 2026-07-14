package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/translate"
)

// Every conforming FHIR server publishes a CapabilityStatement at /metadata with version-specific declarations.
func TestMetadata_R5_DeclaresVersion_ConceptMap_AndOperations(t *testing.T) {
	ts := setupMetadataServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/R5/metadata")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	cs := decodeCapabilityStatement(t, resp.Body)
	assert.Equal(t, "CapabilityStatement", cs["resourceType"])
	assert.Equal(t, "5.0.0", cs["fhirVersion"])
	assertRestResourceHasOperations(t, cs, "ConceptMap", []string{"translate", "translate-batch"})
	assertRestResourceHasSearchParam(t, cs, "ConceptMap", "url")
	assertRestResourceHasSearchParam(t, cs, "ConceptMap", "source-code")
	assertRestResourceHasSearchParam(t, cs, "ConceptMap", "target-code")
}

func TestMetadata_R4_DeclaresVersion_ConceptMap_AndOperations(t *testing.T) {
	ts := setupMetadataServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/R4/metadata")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	cs := decodeCapabilityStatement(t, resp.Body)
	assert.Equal(t, "CapabilityStatement", cs["resourceType"])
	assert.Equal(t, "4.0.1", cs["fhirVersion"])
	assertRestResourceHasOperations(t, cs, "ConceptMap", []string{"translate", "translate-batch"})
}

// Unprefixed /fhir tree aliases R5; /fhir/metadata must also be served so
// pre-M2b clients (and the legacy Bruno collection) keep working.
func TestMetadata_UnprefixedTreeServesR5Metadata(t *testing.T) {
	ts := setupMetadataServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/metadata")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cs := decodeCapabilityStatement(t, resp.Body)
	assert.Equal(t, "5.0.0", cs["fhirVersion"], "the unprefixed tree is the R5 alias")
}

// Per FHIR spec, CapabilityStatement must advertise version-specific $transform parameters via extension array (FHIR operation element lacks native parameter enumeration).
func TestMetadata_R5_TransformOperation_AdvertisesR5SpecParams(t *testing.T) {
	ts := setupMetadataServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/R5/metadata")
	require.NoError(t, err)
	defer resp.Body.Close()
	cs := decodeCapabilityStatement(t, resp.Body)

	params := transformOperationParams(t, cs)
	for _, want := range []string{"source", "sourceMap", "srcMap", "supportingMap", "content"} {
		assert.Containsf(t, params, want, "R5 $transform must advertise %q parameter", want)
	}
}

func TestMetadata_R4_TransformOperation_AdvertisesR4SpecParamsOnly(t *testing.T) {
	ts := setupMetadataServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/R4/metadata")
	require.NoError(t, err)
	defer resp.Body.Close()
	cs := decodeCapabilityStatement(t, resp.Body)

	params := transformOperationParams(t, cs)
	assert.Contains(t, params, "source")
	assert.Contains(t, params, "content")
	for _, r5Only := range []string{"sourceMap", "srcMap", "supportingMap"} {
		assert.NotContainsf(t, params, r5Only, "R4 $transform must NOT advertise %q (R5-only)", r5Only)
	}
}

// The system-level $transform routes mounted on the unprefixed /fhir,
// /fhir/R4, and /fhir/R5 trees are NOT in the FHIR spec — FHIR defines
// $transform only as a resource-scoped operation. We mount it system-
// level for HAPI compat; the CapabilityStatement must surface that with
// an extension so spec scanners and downstream tooling can see it as a
// non-standard alias rather than mistaking it for spec behaviour.
func TestMetadata_SystemLevelTransform_FlaggedAsHapiAlias(t *testing.T) {
	ts := setupMetadataServer(t)
	defer ts.Close()

	for _, tree := range []string{"/fhir/metadata", "/fhir/R5/metadata", "/fhir/R4/metadata"} {
		resp, err := http.Get(ts.URL + tree)
		require.NoError(t, err, tree)
		cs := decodeCapabilityStatement(t, resp.Body)
		resp.Body.Close()

		rest, _ := cs["rest"].([]any)
		require.NotEmpty(t, rest, "rest missing on %s", tree)
		sysOps, _ := rest[0].(map[string]any)["operation"].([]any)
		require.NotEmpty(t, sysOps, "rest.operation missing on %s — system-level $transform alias must be advertised", tree)

		var found bool
		for _, o := range sysOps {
			om, _ := o.(map[string]any)
			if om["name"] != "transform" {
				continue
			}
			found = true
			exts, _ := om["extension"].([]any)
			var hapiFlag bool
			for _, e := range exts {
				em, _ := e.(map[string]any)
				if em["url"] == "https://goodeworkers.github.io/fhir-map/fhir/extensions/operation-hapi-alias" {
					if v, ok := em["valueBoolean"].(bool); ok && v {
						hapiFlag = true
					}
				}
			}
			assert.Truef(t, hapiFlag, "system-level $transform on %s must carry the HAPI-alias extension", tree)
		}
		assert.Truef(t, found, "no `transform` entry under rest.operation on %s", tree)
	}
}

// CapabilityStatement.date must be captured at process startup, not per-request, to avoid busting client-side caches.
func TestMetadata_DateIsConstantAcrossCalls(t *testing.T) {
	ts := setupMetadataServer(t)
	defer ts.Close()

	get := func() string {
		resp, err := http.Get(ts.URL + "/fhir/R5/metadata")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		cs := decodeCapabilityStatement(t, resp.Body)
		d, _ := cs["date"].(string)
		require.NotEmpty(t, d, "CapabilityStatement.date must not be empty")
		return d
	}

	first := get()
	// RFC3339 has second-level resolution; 1ms sleep ensures deterministic test while validating the startup-capture fix.
	time.Sleep(1 * time.Millisecond)
	second := get()
	assert.Equal(t, first, second, "CapabilityStatement.date must be identical across calls (captured at startup, not per-request)")
}

func transformOperationParams(t *testing.T, cs map[string]any) []string {
	t.Helper()
	rest, _ := cs["rest"].([]any)
	require.NotEmpty(t, rest)
	resources, _ := rest[0].(map[string]any)["resource"].([]any)
	for _, r := range resources {
		rm, _ := r.(map[string]any)
		if rm["type"] != "StructureMap" {
			continue
		}
		ops, _ := rm["operation"].([]any)
		for _, o := range ops {
			om, _ := o.(map[string]any)
			if om["name"] != "transform" {
				continue
			}
			exts, _ := om["extension"].([]any)
			var names []string
			for _, e := range exts {
				em, _ := e.(map[string]any)
				if em["url"] == "https://goodeworkers.github.io/fhir-map/fhir/extensions/transform-supported-parameter" {
					if v, ok := em["valueString"].(string); ok {
						names = append(names, v)
					}
				}
			}
			return names
		}
	}
	t.Fatalf("StructureMap.operation.transform not found in CapabilityStatement")
	return nil
}

func setupMetadataServer(t *testing.T) *httptest.Server {
	t.Helper()
	repo := newMockRepo()
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mux := http.NewServeMux()
	r5 := NewConceptMapHandler(service, engine, "http://localhost", logger)
	r5.RegisterRoutes(mux)               // /fhir
	r5.RegisterRoutesAtPrefix(mux, "R5") // /fhir/R5
	r4 := NewR4ConceptMapHandler(service, engine, "http://localhost", logger)
	r4.RegisterRoutes(mux) // /fhir/R4
	return httptest.NewServer(mux)
}

func decodeCapabilityStatement(t *testing.T, r io.Reader) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.NewDecoder(r).Decode(&out))
	return out
}

func assertRestResourceHasOperations(t *testing.T, cs map[string]any, resourceType string, opNames []string) {
	t.Helper()
	rest, _ := cs["rest"].([]any)
	require.NotEmpty(t, rest, "CapabilityStatement.rest is missing")
	resources, _ := rest[0].(map[string]any)["resource"].([]any)
	for _, r := range resources {
		rm, _ := r.(map[string]any)
		if rm["type"] != resourceType {
			continue
		}
		ops, _ := rm["operation"].([]any)
		seen := map[string]bool{}
		for _, o := range ops {
			om, _ := o.(map[string]any)
			if n, ok := om["name"].(string); ok {
				seen[n] = true
			}
		}
		for _, want := range opNames {
			assert.Truef(t, seen[want], "expected operation %q advertised on resource %q; saw %v", want, resourceType, seen)
		}
		return
	}
	t.Fatalf("resource %q not found in CapabilityStatement.rest[0].resource", resourceType)
}

func assertRestResourceHasSearchParam(t *testing.T, cs map[string]any, resourceType, paramName string) {
	t.Helper()
	rest, _ := cs["rest"].([]any)
	require.NotEmpty(t, rest)
	resources, _ := rest[0].(map[string]any)["resource"].([]any)
	for _, r := range resources {
		rm, _ := r.(map[string]any)
		if rm["type"] != resourceType {
			continue
		}
		params, _ := rm["searchParam"].([]any)
		for _, p := range params {
			pm, _ := p.(map[string]any)
			if pm["name"] == paramName {
				return
			}
		}
		t.Fatalf("search param %q not found on %q; saw %d params", paramName, resourceType, len(params))
	}
	t.Fatalf("resource %q not found", resourceType)
}
