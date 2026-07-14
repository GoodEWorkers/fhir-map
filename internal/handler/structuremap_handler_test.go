package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuredefinition"
	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/internal/transform"
	"github.com/goodeworkers/fhir-map/internal/transform/resolver"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// smInMemoryRepo is a minimal in-memory structuremap.Repository for handler
// tests that don't need a Postgres container.
//
// Tombstone tracking parallels the Postgres repo:
//   - `deleted` flags a soft-deleted id; Read/Update/Delete return ErrGone
//     instead of ErrNotFound when the id is in the set.
//   - `deletedURL` does the same for canonical URL lookup (FindByURL).
//   - `history` carries create/update/delete entries newest-first so the
//     Vread handler can distinguish the delete version from prior snapshots.
type smInMemoryRepo struct {
	store      map[string]*structuremap.StructureMap
	byURL      map[string]*structuremap.StructureMap
	deleted    map[string]struct{}
	deletedURL map[string]struct{}
	history    map[string][]structuremap.HistoryEntry
}

func newSMInMemoryRepo() *smInMemoryRepo {
	return &smInMemoryRepo{
		store:      map[string]*structuremap.StructureMap{},
		byURL:      map[string]*structuremap.StructureMap{},
		deleted:    map[string]struct{}{},
		deletedURL: map[string]struct{}{},
		history:    map[string][]structuremap.HistoryEntry{},
	}
}

func (m *smInMemoryRepo) appendHistory(id, op string, sm *structuremap.StructureMap) {
	var snapshot *structuremap.StructureMap
	if sm != nil {
		copy := *sm
		snapshot = &copy
	}
	vid := len(m.history[id]) + 1
	m.history[id] = append([]structuremap.HistoryEntry{{
		VersionID: vid,
		Operation: op,
		Resource:  snapshot,
	}}, m.history[id]...)
}

func (m *smInMemoryRepo) Create(_ context.Context, sm *structuremap.StructureMap) (*structuremap.StructureMap, error) {
	m.store[sm.ID] = sm
	if sm.URL != "" {
		m.byURL[sm.URL] = sm
	}
	m.appendHistory(sm.ID, "create", sm)
	return sm, nil
}
func (m *smInMemoryRepo) Read(_ context.Context, id string) (*structuremap.StructureMap, error) {
	if _, gone := m.deleted[id]; gone {
		return nil, structuremap.ErrGone
	}
	sm, ok := m.store[id]
	if !ok {
		return nil, structuremap.ErrNotFound
	}
	return sm, nil
}
func (m *smInMemoryRepo) Update(_ context.Context, id string, sm *structuremap.StructureMap) (*structuremap.StructureMap, error) {
	if _, gone := m.deleted[id]; gone {
		return nil, structuremap.ErrGone
	}
	if _, ok := m.store[id]; !ok {
		return nil, structuremap.ErrNotFound
	}
	sm.ID = id
	m.store[id] = sm
	m.appendHistory(id, "update", sm)
	return sm, nil
}
func (m *smInMemoryRepo) Delete(_ context.Context, id string) error {
	if _, gone := m.deleted[id]; gone {
		return structuremap.ErrGone
	}
	sm, ok := m.store[id]
	if !ok {
		return structuremap.ErrNotFound
	}
	m.deleted[id] = struct{}{}
	if sm.URL != "" {
		m.deletedURL[sm.URL] = struct{}{}
		delete(m.byURL, sm.URL)
	}
	m.appendHistory(id, "delete", sm)
	delete(m.store, id)
	return nil
}
func (m *smInMemoryRepo) Search(_ context.Context, p structuremap.SearchParams) (*structuremap.SearchResult, error) {
	var out []structuremap.StructureMap
	for _, sm := range m.store {
		if p.Status != "" && sm.Status != p.Status {
			continue
		}
		out = append(out, *sm)
	}
	return &structuremap.SearchResult{StructureMaps: out, Total: len(out)}, nil
}
func (m *smInMemoryRepo) FindByURL(_ context.Context, url, _ string) (*structuremap.StructureMap, error) {
	if sm, ok := m.byURL[url]; ok {
		return sm, nil
	}
	if _, gone := m.deletedURL[url]; gone {
		return nil, structuremap.ErrGone
	}
	return nil, structuremap.ErrNotFound
}
func (m *smInMemoryRepo) History(_ context.Context, id string) ([]structuremap.HistoryEntry, error) {
	entries, ok := m.history[id]
	if !ok || len(entries) == 0 {
		return nil, structuremap.ErrNotFound
	}
	return entries, nil
}
func (m *smInMemoryRepo) ReadVersion(_ context.Context, id string, vid int) (*structuremap.StructureMap, error) {
	for _, e := range m.history[id] {
		if e.VersionID == vid && e.Resource != nil {
			return e.Resource, nil
		}
	}
	return nil, structuremap.ErrNotFound
}

func setupStructureMapServer(t *testing.T) (*httptest.Server, *smInMemoryRepo) {
	t.Helper()
	repo := newSMInMemoryRepo()
	service := structuremap.NewService(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	h := NewStructureMapHandler(service, "http://localhost", logger).WithHistory(repo)
	h.RegisterRoutes(mux)
	h.RegisterRoutesAtPrefix(mux, "R5")
	r4 := NewR4StructureMapHandler(service, "http://localhost", logger).WithHistory(repo)
	r4.RegisterRoutes(mux)
	// Mirror production: apply the global body-size middleware so tests are faithful.
	wrapped := Middleware(mux, MaxBodyBytesMiddleware(10<<20))
	return httptest.NewServer(wrapped), repo
}

// setupStructureMapServerWithEngine wires handlers plus a real transform.Engine so $transform runs end-to-end.
func setupStructureMapServerWithEngine(t *testing.T) (*httptest.Server, *smInMemoryRepo) {
	t.Helper()
	repo := newSMInMemoryRepo()
	service := structuremap.NewService(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := transform.NewEngine(nil)
	mux := http.NewServeMux()
	h := NewStructureMapHandler(service, "http://localhost", logger).WithHistory(repo).WithTransformEngine(eng)
	h.RegisterRoutes(mux)
	h.RegisterRoutesAtPrefix(mux, "R5")
	r4 := NewR4StructureMapHandler(service, "http://localhost", logger).WithHistory(repo).WithTransformEngine(eng)
	r4.RegisterRoutes(mux)
	// Mirror production: apply the global body-size middleware so tests are faithful.
	wrapped := Middleware(mux, MaxBodyBytesMiddleware(10<<20))
	return httptest.NewServer(wrapped), repo
}

// qrToPatientInlineMap is the JSON form of the same `copy`-based map used
// by the executor tests. Kept as a function so each test gets a fresh
// instance.
func qrToPatientInlineMap() map[string]any {
	return map[string]any{
		"resourceType": "StructureMap",
		"url":          "http://example.org/sm/m6-0-3-qr2pat",
		"name":         "QRToPatientM603",
		"status":       "active",
		"group": []any{
			map[string]any{
				"name": "MapQRtoPatient",
				"input": []any{
					map[string]any{"name": "src", "type": "QuestionnaireResponse", "mode": "source"},
					map[string]any{"name": "tgt", "type": "Patient", "mode": "target"},
				},
				"rule": []any{
					map[string]any{
						"name": "firstName",
						"source": []any{map[string]any{
							"context":  "src",
							"element":  "item.where(linkId = 'first').answer.valueString",
							"variable": "v",
						}},
						"target": []any{map[string]any{
							"context":   "tgt",
							"element":   "firstName",
							"transform": "copy",
							"parameter": []any{map[string]any{"valueId": "v"}},
						}},
					},
				},
			},
		},
	}
}

func qrToPatientContent() map[string]any {
	return map[string]any{
		"resourceType": "QuestionnaireResponse",
		"item": []any{
			map[string]any{"linkId": "first", "answer": []any{map[string]any{"valueString": "Ada"}}},
		},
	}
}

// M6.0.3 — R5 spec parameter `sourceMap` carries an inline StructureMap as
// a Parameters part (alongside the spec name `content` for the input
// resource). Today only HAPI's synonyms (`structureMap`, `source`/`input`)
// are recognised. The R5 spec form must work.
//
// Spec: https://hl7.org/fhir/R5/operation-structuremap-transform.html
func TestHandler_Transform_R5_AcceptsSourceMapAndContent(t *testing.T) {
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
	require.Equal(t, http.StatusOK, resp.StatusCode, "R5 spec params sourceMap+content must run")

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "Ada", result["firstName"], "transform applied via R5 spec param names")
}

// TestHandler_Transform_R5_AcceptsSrcMapFML verifies srcMap (R5 spec) and fml (HAPI) both work for inline FML text.
func TestHandler_Transform_R5_AcceptsSrcMapFML(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	fmlText := `
		map "http://example.org/sm/m6-0-3-fml" = "FMLM603"

		group MapIt(source src, target tgt) {
		  src.value as v -> tgt.out = copy(v);
		}
	`
	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "srcMap", "valueString": fmlText},
			map[string]any{"name": "content", "resource": map[string]any{"value": "hello"}},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "hello", result["out"])
}

// TestHandler_Transform_R5_AcceptsSourceAsCanonicalURL verifies both R5 spec (source) and HAPI (map) resolve canonical URLs.
func TestHandler_Transform_R5_AcceptsSourceAsCanonicalURL(t *testing.T) {
	ts, repo := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	sm := qrToPatientInlineMap()
	canonical, _ := sm["url"].(string)
	// Persist via the repo so FindByURL resolves.
	smJSON, _ := json.Marshal(sm)
	var parsed structuremap.StructureMap
	require.NoError(t, json.Unmarshal(smJSON, &parsed))
	parsed.ID = "persisted-1"
	repo.store[parsed.ID] = &parsed
	repo.byURL[canonical] = &parsed

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "source", "valueUri": canonical},
			map[string]any{"name": "content", "resource": qrToPatientContent()},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "R5 spec: source=<canonical> must resolve a persisted map")

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "Ada", result["firstName"])
}

// TestHandler_Transform_R5_AcceptsSupportingMap verifies supportingMap parameter is accepted (integration deferred).
func TestHandler_Transform_R5_AcceptsSupportingMap(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	supporting := map[string]any{
		"resourceType": "StructureMap",
		"url":          "http://example.org/sm/m6-0-3-supporting",
		"name":         "SupportingM603",
		"status":       "active",
		"group": []any{map[string]any{
			"name":  "helper",
			"input": []any{map[string]any{"name": "x", "mode": "source"}},
			"rule":  []any{map[string]any{"name": "noop", "source": []any{map[string]any{"context": "x"}}}},
		}},
	}

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "sourceMap", "resource": qrToPatientInlineMap()},
			map[string]any{"name": "supportingMap", "resource": supporting},
			map[string]any{"name": "content", "resource": qrToPatientContent()},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "supportingMap must be accepted (not rejected as unknown param)")
}

// TestHandler_Transform_R4_RejectsSourceMap verifies R5-only parameters are rejected with 400 on R4 tree.
func TestHandler_Transform_R4_RejectsSourceMap(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "sourceMap", "resource": qrToPatientInlineMap()},
			map[string]any{"name": "content", "resource": qrToPatientContent()},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R4/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "R4 must reject sourceMap (R5-only)")

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, _ := outcome["issue"].([]any)
	require.NotEmpty(t, issues)
	issue, _ := issues[0].(map[string]any)
	assert.Equal(t, "not-supported", issue["code"])
	diag, _ := issue["diagnostics"].(string)
	assert.Contains(t, diag, "sourceMap", "diagnostics must name the offending R5-only parameter")
}

func TestHandler_Transform_R4_RejectsSrcMap(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "srcMap", "valueString": "map \"x\" = \"y\"\ngroup g(source s, target t) { s.v as v -> t.v = copy(v); }\n"},
			map[string]any{"name": "content", "resource": map[string]any{"v": "x"}},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R4/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "R4 must reject srcMap (R5-only)")
}

func TestHandler_Transform_R4_RejectsSupportingMap(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "source", "valueUri": "http://example.org/x"},
			map[string]any{"name": "supportingMap", "resource": map[string]any{"resourceType": "StructureMap"}},
			map[string]any{"name": "content", "resource": map[string]any{}},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R4/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "R4 must reject supportingMap (R5-only)")
}

// R4 must still accept the R4-spec parameters (`source` for canonical URL,
// `content` for input resource) and the long-standing HAPI extensions
// (`map`, `structureMap`, `fml`) that the Postman corpus uses. Only the
// R5-only names are rejected.
func TestHandler_Transform_R4_AcceptsR4SpecAndHapiExtensions(t *testing.T) {
	ts, repo := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	sm := qrToPatientInlineMap()
	canonical, _ := sm["url"].(string)
	smJSON, _ := json.Marshal(sm)
	var parsed structuremap.StructureMap
	require.NoError(t, json.Unmarshal(smJSON, &parsed))
	parsed.ID = "persisted-r4"
	repo.store[parsed.ID] = &parsed
	repo.byURL[canonical] = &parsed

	// R4 spec form: `source` (canonical URL) + `content` (input resource).
	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "source", "valueUri": canonical},
			map[string]any{"name": "content", "resource": qrToPatientContent()},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R4/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "R4 spec form must work")

	// HAPI extension form: `structureMap` (inline) + `input` (nested resource).
	body2, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "structureMap", "resource": qrToPatientInlineMap()},
			map[string]any{"name": "input", "part": []any{
				map[string]any{"name": "source", "resource": qrToPatientContent()},
			}},
		},
	})
	resp2, err := http.Post(ts.URL+"/fhir/R4/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body2))
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode, "HAPI extension form must still work on R4")
}

// TestHandler_StructureMap_R4_StripsCopyrightLabel verifies R5-only fields are stripped on R4 wire projection.
func TestHandler_StructureMap_R4_StripsCopyrightLabel(t *testing.T) {
	ts, repo := setupStructureMapServer(t)
	defer ts.Close()

	repo.store["sm-r4-strip"] = &structuremap.StructureMap{
		ResourceType:   "StructureMap",
		ID:             "sm-r4-strip",
		URL:            "http://example.org/sm/r4-strip",
		Name:           "R4StripTest",
		Status:         "active",
		Copyright:      "© ACME",
		CopyrightLabel: "ACME License v1",
		Group: []structuremap.Group{{
			Name:  "g",
			Input: []structuremap.Input{{Name: "src", Mode: "source"}, {Name: "tgt", Mode: "target"}},
			Rule:  []structuremap.Rule{{Name: "r1"}},
		}},
	}

	resp, err := http.Get(ts.URL + "/fhir/R4/StructureMap/sm-r4-strip")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var wire map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&wire))
	_, hasLabel := wire["copyrightLabel"]
	assert.False(t, hasLabel, "R4 wire response must not contain copyrightLabel (R5-only); got: %v", wire["copyrightLabel"])
	assert.Equal(t, "© ACME", wire["copyright"], "R4-compatible `copyright` must survive")
	assert.Equal(t, "R4StripTest", wire["name"])

	// Same resource on the R5 tree keeps the field — storage is canonical.
	r5Resp, err := http.Get(ts.URL + "/fhir/R5/StructureMap/sm-r4-strip")
	require.NoError(t, err)
	defer r5Resp.Body.Close()
	var r5wire map[string]any
	require.NoError(t, json.NewDecoder(r5Resp.Body).Decode(&r5wire))
	assert.Equal(t, "ACME License v1", r5wire["copyrightLabel"], "R5 wire MUST keep copyrightLabel")
}

// TestHandler_StructureMap_R4_StripsVersionAlgorithm verifies versionAlgorithm fields are stripped on R4.
func TestHandler_StructureMap_R4_StripsVersionAlgorithm(t *testing.T) {
	ts, repo := setupStructureMapServer(t)
	defer ts.Close()

	repo.store["sm-ver-alg"] = &structuremap.StructureMap{
		ResourceType:           "StructureMap",
		ID:                     "sm-ver-alg",
		URL:                    "http://example.org/sm/ver-alg",
		Name:                   "VerAlgStrip",
		Status:                 "active",
		Copyright:              "© R4-survives",
		VersionAlgorithmString: "semver",
		Group: []structuremap.Group{{
			Name:  "g",
			Input: []structuremap.Input{{Name: "src", Mode: "source"}, {Name: "tgt", Mode: "target"}},
			Rule:  []structuremap.Rule{{Name: "r1"}},
		}},
	}

	// R4 wire: versionAlgorithmString must be absent.
	r4Resp, err := http.Get(ts.URL + "/fhir/R4/StructureMap/sm-ver-alg")
	require.NoError(t, err)
	defer r4Resp.Body.Close()
	require.Equal(t, http.StatusOK, r4Resp.StatusCode)

	var r4Wire map[string]any
	require.NoError(t, json.NewDecoder(r4Resp.Body).Decode(&r4Wire))
	_, hasVAS := r4Wire["versionAlgorithmString"]
	assert.False(t, hasVAS, "R4 wire must NOT contain versionAlgorithmString (R5-only)")
	_, hasVAC := r4Wire["versionAlgorithmCoding"]
	assert.False(t, hasVAC, "R4 wire must NOT contain versionAlgorithmCoding (R5-only)")
	// AC-2: R4-compatible fields (name, status, copyright, group) survive intact.
	assert.Equal(t, "VerAlgStrip", r4Wire["name"])
	assert.Equal(t, "active", r4Wire["status"])
	assert.Equal(t, "© R4-survives", r4Wire["copyright"])
	r4Groups, ok := r4Wire["group"].([]any)
	require.True(t, ok, "R4 wire must keep group[]")
	require.Len(t, r4Groups, 1)

	// R5 wire: versionAlgorithmString must be present.
	r5Resp, err := http.Get(ts.URL + "/fhir/R5/StructureMap/sm-ver-alg")
	require.NoError(t, err)
	defer r5Resp.Body.Close()
	require.Equal(t, http.StatusOK, r5Resp.StatusCode)

	var r5Wire map[string]any
	require.NoError(t, json.NewDecoder(r5Resp.Body).Decode(&r5Wire))
	assert.Equal(t, "semver", r5Wire["versionAlgorithmString"], "R5 wire MUST keep versionAlgorithmString")
}

// TestHandler_StructureMap_R4_StripsVersionAlgorithmCoding verifies versionAlgorithmCoding is stripped on R4.
func TestHandler_StructureMap_R4_StripsVersionAlgorithmCoding(t *testing.T) {
	ts, repo := setupStructureMapServer(t)
	defer ts.Close()

	repo.store["sm-ver-alg-coding"] = &structuremap.StructureMap{
		ResourceType: "StructureMap",
		ID:           "sm-ver-alg-coding",
		URL:          "http://example.org/sm/ver-alg-coding",
		Name:         "VerAlgCodingStrip",
		Status:       "active",
		VersionAlgorithmCoding: &fhir.Coding{
			System: "http://example.org/algo",
			Code:   "calver",
		},
		Group: []structuremap.Group{{
			Name:  "g",
			Input: []structuremap.Input{{Name: "src", Mode: "source"}, {Name: "tgt", Mode: "target"}},
			Rule:  []structuremap.Rule{{Name: "r1"}},
		}},
	}

	// R4 wire: both versionAlgorithm flavours absent.
	r4Resp, err := http.Get(ts.URL + "/fhir/R4/StructureMap/sm-ver-alg-coding")
	require.NoError(t, err)
	defer r4Resp.Body.Close()
	require.Equal(t, http.StatusOK, r4Resp.StatusCode)
	var r4Wire map[string]any
	require.NoError(t, json.NewDecoder(r4Resp.Body).Decode(&r4Wire))
	_, hasVAC := r4Wire["versionAlgorithmCoding"]
	assert.False(t, hasVAC, "R4 wire must NOT contain versionAlgorithmCoding (R5-only)")
	_, hasVAS := r4Wire["versionAlgorithmString"]
	assert.False(t, hasVAS, "R4 wire must NOT contain versionAlgorithmString either")

	// R5 wire: versionAlgorithmCoding present with full structure.
	r5Resp, err := http.Get(ts.URL + "/fhir/R5/StructureMap/sm-ver-alg-coding")
	require.NoError(t, err)
	defer r5Resp.Body.Close()
	require.Equal(t, http.StatusOK, r5Resp.StatusCode)
	var r5Wire map[string]any
	require.NoError(t, json.NewDecoder(r5Resp.Body).Decode(&r5Wire))
	coding, ok := r5Wire["versionAlgorithmCoding"].(map[string]any)
	require.True(t, ok, "R5 wire MUST keep versionAlgorithmCoding as an object")
	assert.Equal(t, "http://example.org/algo", coding["system"])
	assert.Equal(t, "calver", coding["code"])
}

func validStructureMapJSON() []byte {
	body, _ := json.Marshal(map[string]any{
		"resourceType": "StructureMap",
		"url":          "http://example.org/sm/handler-test",
		"name":         "HandlerTest",
		"status":       "active",
		"group": []any{
			map[string]any{
				"name": "g",
				"input": []any{
					map[string]any{"name": "src", "mode": "source"},
					map[string]any{"name": "tgt", "mode": "target"},
				},
				"rule": []any{
					map[string]any{
						"name": "r1",
						"source": []any{
							map[string]any{"context": "src"},
						},
						"target": []any{
							map[string]any{"context": "tgt", "transform": "copy"},
						},
					},
				},
			},
		},
	})
	return body
}

func TestHandler_StructureMap_CRUD_R5(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	createResp, err := http.Post(ts.URL+"/fhir/R5/StructureMap", "application/fhir+json", bytes.NewReader(validStructureMapJSON()))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	var created structuremap.StructureMap
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))
	require.NotEmpty(t, created.ID)

	readResp, err := http.Get(ts.URL + "/fhir/R5/StructureMap/" + created.ID)
	require.NoError(t, err)
	defer readResp.Body.Close()
	require.Equal(t, http.StatusOK, readResp.StatusCode)
	var fetched structuremap.StructureMap
	require.NoError(t, json.NewDecoder(readResp.Body).Decode(&fetched))
	assert.Equal(t, "HandlerTest", fetched.Name)
	assert.Equal(t, "copy", fetched.Group[0].Rule[0].Target[0].Transform)

	searchResp, err := http.Get(ts.URL + "/fhir/R5/StructureMap?status=active")
	require.NoError(t, err)
	defer searchResp.Body.Close()
	require.Equal(t, http.StatusOK, searchResp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(searchResp.Body).Decode(&bundle))
	assert.Equal(t, "Bundle", bundle["resourceType"])
	assert.Equal(t, "searchset", bundle["type"])

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/R5/StructureMap/"+created.ID, nil)
	delResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer delResp.Body.Close()
	assert.Equal(t, http.StatusNoContent, delResp.StatusCode)
}

func TestHandler_StructureMap_CRUD_R4(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/fhir/R4/StructureMap", "application/fhir+json", bytes.NewReader(validStructureMapJSON()))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestHandler_StructureMap_Read_AfterDelete_Returns410(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	// Create a resource.
	createResp, err := http.Post(ts.URL+"/fhir/R5/StructureMap", "application/fhir+json", bytes.NewReader(validStructureMapJSON()))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	var created structuremap.StructureMap
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))

	// Delete it.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/R5/StructureMap/"+created.ID, nil)
	delResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer delResp.Body.Close()
	require.Equal(t, http.StatusNoContent, delResp.StatusCode)

	// GET /fhir/R5 → 410 Gone.
	r5Resp, err := http.Get(ts.URL + "/fhir/R5/StructureMap/" + created.ID)
	require.NoError(t, err)
	defer r5Resp.Body.Close()
	assert.Equal(t, http.StatusGone, r5Resp.StatusCode, "R5 GET after DELETE must return 410")
	var r5Outcome map[string]any
	require.NoError(t, json.NewDecoder(r5Resp.Body).Decode(&r5Outcome))
	assert.Equal(t, "OperationOutcome", r5Outcome["resourceType"])
	r5Issues, ok := r5Outcome["issue"].([]any)
	require.True(t, ok, "R5 issue must be an array")
	require.NotEmpty(t, r5Issues)
	r5Issue, ok := r5Issues[0].(map[string]any)
	require.True(t, ok, "R5 issue[0] must be an object")
	assert.Equal(t, "gone", r5Issue["code"])
	assert.Equal(t, "error", r5Issue["severity"])

	// GET /fhir/R4 → 410 Gone with same shape.
	r4Resp, err := http.Get(ts.URL + "/fhir/R4/StructureMap/" + created.ID)
	require.NoError(t, err)
	defer r4Resp.Body.Close()
	assert.Equal(t, http.StatusGone, r4Resp.StatusCode, "R4 GET after DELETE must return 410")
	var r4Outcome map[string]any
	require.NoError(t, json.NewDecoder(r4Resp.Body).Decode(&r4Outcome))
	assert.Equal(t, "OperationOutcome", r4Outcome["resourceType"])
	r4Issues, ok := r4Outcome["issue"].([]any)
	require.True(t, ok, "R4 issue must be an array")
	require.NotEmpty(t, r4Issues)
	r4Issue, ok := r4Issues[0].(map[string]any)
	require.True(t, ok, "R4 issue[0] must be an object")
	assert.Equal(t, "gone", r4Issue["code"])
	assert.Equal(t, "error", r4Issue["severity"])
}

func TestHandler_StructureMap_Strict_RejectsBogusTransform(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"resourceType": "StructureMap",
		"url":          "http://example.org/sm/bogus",
		"name":         "Bogus",
		"status":       "active",
		"group": []any{
			map[string]any{
				"name": "g",
				"input": []any{
					map[string]any{"name": "src", "mode": "source"},
					map[string]any{"name": "tgt", "mode": "target"},
				},
				"rule": []any{
					map[string]any{
						"name":   "r1",
						"source": []any{map[string]any{"context": "src"}},
						"target": []any{map[string]any{"context": "tgt", "transform": "not-a-real-transform"}},
					},
				},
			},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	// AC-4: strict validation now returns 422 (was 400).
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

	// AC-4: response must be an OperationOutcome with code "invalid" and the
	// generic [SYM-GR-0013] diagnostic — not raw validator output.
	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue must be an array")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok, "issue[0] must be an object")
	assert.Equal(t, "invalid", issue["code"])
	assert.Equal(t, "error", issue["severity"])
	diag, _ := issue["diagnostics"].(string)
	assert.Equal(t, "The resource failed validation. Correct the resource and resubmit.", diag)

	// Lenient variant must accept the same body.
	resp2, err := http.Post(ts.URL+"/fhir/R5/StructureMap?_validate=lenient", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusCreated, resp2.StatusCode)
}

func TestHandler_StructureMap_Create_MissingName_Returns422(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	// AC-4 post-condition: capture the pre-POST search total so we can prove
	// the failed validation stored nothing.
	preTotal := searchTotal(t, ts.URL+"/fhir/R5/StructureMap")

	body, _ := json.Marshal(map[string]any{
		"resourceType": "StructureMap",
		"status":       "active",
		"group": []any{
			map[string]any{
				"name":  "g",
				"input": []any{map[string]any{"name": "src", "mode": "source"}},
				"rule":  []any{map[string]any{"name": "r"}},
			},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, "missing name must return 422")

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue must be an array")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok, "issue[0] must be an object")
	assert.Equal(t, "invalid", issue["code"])
	assert.Equal(t, "error", issue["severity"])
	// [SYM-GR-0013]: diagnostic must NOT echo validator internals; use generic message.
	diag, _ := issue["diagnostics"].(string)
	assert.Equal(t, "The resource failed validation. Correct the resource and resubmit.", diag)

	// AC-4 post-condition: failed validation must NOT have stored a row.
	postTotal := searchTotal(t, ts.URL+"/fhir/R5/StructureMap")
	assert.Equal(t, preTotal, postTotal, "422 must not have stored a resource (search total must be unchanged)")
}

// searchTotal returns Bundle.total from a StructureMap search, used to verify no resource was stored.
func searchTotal(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&bundle))
	switch v := bundle["total"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func TestHandler_StructureMap_Create_MissingGroupRule_Returns422(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"resourceType": "StructureMap",
		"name":         "NoRule",
		"status":       "active",
		"group": []any{
			map[string]any{
				"name":  "g",
				"input": []any{map[string]any{"name": "src", "mode": "source"}},
				// rule is absent
			},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, "missing group.rule must return 422")

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue must be an array")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok, "issue[0] must be an object")
	assert.Equal(t, "invalid", issue["code"])
	assert.Equal(t, "error", issue["severity"])
	diag, _ := issue["diagnostics"].(string)
	assert.Equal(t, "The resource failed validation. Correct the resource and resubmit.", diag)
}

// TestHandler_StructureMap_PostR4_GetR5_Equivalent verifies R4→R5 round-trip preserves all fields.
func TestHandler_StructureMap_PostR4_GetR5_Equivalent(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	r4Body, _ := json.Marshal(map[string]any{
		"resourceType": "StructureMap",
		"url":          "http://example.org/sm/r4-to-r5",
		"name":         "R4toR5Test",
		"status":       "active",
		"copyright":    "© Round-trip Test",
		"group": []any{
			map[string]any{
				"name": "g",
				"input": []any{
					map[string]any{"name": "src", "type": "Patient", "mode": "source"},
					map[string]any{"name": "tgt", "type": "Bundle", "mode": "target"},
				},
				"rule": []any{
					map[string]any{
						"name":   "idCopy",
						"source": []any{map[string]any{"context": "src", "element": "id", "variable": "v"}},
						"target": []any{map[string]any{"context": "tgt", "element": "id", "transform": "copy", "parameter": []any{map[string]any{"valueId": "v"}}}},
					},
				},
			},
		},
	})

	// POST to R4 → 201.
	r4Resp, err := http.Post(ts.URL+"/fhir/R4/StructureMap", "application/fhir+json", bytes.NewReader(r4Body))
	require.NoError(t, err)
	defer r4Resp.Body.Close()
	require.Equal(t, http.StatusCreated, r4Resp.StatusCode)
	var r4Created map[string]any
	require.NoError(t, json.NewDecoder(r4Resp.Body).Decode(&r4Created))
	id, _ := r4Created["id"].(string)
	require.NotEmpty(t, id)

	// GET from R5 → 200.
	r5Resp, err := http.Get(ts.URL + "/fhir/R5/StructureMap/" + id)
	require.NoError(t, err)
	defer r5Resp.Body.Close()
	require.Equal(t, http.StatusOK, r5Resp.StatusCode)
	var r5Got map[string]any
	require.NoError(t, json.NewDecoder(r5Resp.Body).Decode(&r5Got))

	// Semantic equivalence assertions.
	assert.Equal(t, "R4toR5Test", r5Got["name"], "name must survive R4→R5 round-trip")
	assert.Equal(t, "active", r5Got["status"], "status must survive R4→R5 round-trip")
	assert.Equal(t, "http://example.org/sm/r4-to-r5", r5Got["url"], "url must survive R4→R5 round-trip")

	groups, _ := r5Got["group"].([]any)
	require.Len(t, groups, 1, "group slice must survive R4→R5 round-trip")
	g0, _ := groups[0].(map[string]any)
	assert.Equal(t, "g", g0["name"])
	inputs, _ := g0["input"].([]any)
	require.Len(t, inputs, 2)
	rules, _ := g0["rule"].([]any)
	require.Len(t, rules, 1)
	r0, _ := rules[0].(map[string]any)
	targets, _ := r0["target"].([]any)
	require.Len(t, targets, 1)
	t0, _ := targets[0].(map[string]any)
	assert.Equal(t, "copy", t0["transform"], "transform must survive R4→R5 round-trip")

	// Task 6.2 — no silent loss: copyright from R4 input survives on R5 GET.
	assert.Equal(t, "© Round-trip Test", r5Got["copyright"], "copyright must survive R4→R5 (no silent loss)")
}

func TestHandler_StructureMap_CRUD_R5_AC1Assertions(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	createResp, err := http.Post(ts.URL+"/fhir/R5/StructureMap", "application/fhir+json", bytes.NewReader(validStructureMapJSON()))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	var created structuremap.StructureMap
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))
	require.NotEmpty(t, created.ID)

	location := createResp.Header.Get("Location")
	assert.Regexp(t, `^http://[^/]+/fhir/StructureMap/[a-f0-9-]+$`, location,
		"Location header must be present and match /fhir/StructureMap/{uuid}")

	etag := createResp.Header.Get("ETag")
	assert.Equal(t, `W/"1"`, etag, "ETag must be W/\"1\" on first create")

	require.NotNil(t, created.Meta)
	require.NotEmpty(t, created.Meta.LastUpdated, "meta.lastUpdated must be set on create")
	_, err = time.Parse(time.RFC3339, created.Meta.LastUpdated)
	assert.NoError(t, err, "meta.lastUpdated must be valid RFC3339")
	assert.Equal(t, "1", created.Meta.VersionID, "meta.versionId must be \"1\" on first create")
}

func TestHandler_StructureMap_CRUD_R4_AC1Assertions(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/fhir/R4/StructureMap", "application/fhir+json", bytes.NewReader(validStructureMapJSON()))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created structuremap.StructureMap
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	require.NotEmpty(t, created.ID)

	location := resp.Header.Get("Location")
	assert.Regexp(t, `^http://[^/]+/fhir/R4/StructureMap/[a-f0-9-]+$`, location,
		"R4 Location header must match /fhir/R4/StructureMap/{uuid}")

	etag := resp.Header.Get("ETag")
	assert.Equal(t, `W/"1"`, etag, "ETag must be W/\"1\" on first create via R4")

	require.NotNil(t, created.Meta)
	require.NotEmpty(t, created.Meta.LastUpdated)
	_, err = time.Parse(time.RFC3339, created.Meta.LastUpdated)
	assert.NoError(t, err, "meta.lastUpdated must be valid RFC3339 (R4 create)")
	assert.Equal(t, "1", created.Meta.VersionID, "meta.versionId must be \"1\" on first create (R4)")
}

func TestHandler_StructureMap_Transform_Returns501(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader([]byte("{}")))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
}

func TestMetadata_AdvertisesStructureMap(t *testing.T) {
	ts := setupMetadataServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/R5/metadata")
	require.NoError(t, err)
	defer resp.Body.Close()
	var cs map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cs))

	rest, _ := cs["rest"].([]any)
	resources, _ := rest[0].(map[string]any)["resource"].([]any)
	seen := make([]string, 0, len(resources))
	for _, r := range resources {
		seen = append(seen, r.(map[string]any)["type"].(string))
	}
	assert.Contains(t, seen, "ConceptMap")
	assert.Contains(t, seen, "StructureMap")
}

// ─── SYM_GO_0089 non-regression: $transform body-size limit ─────────────────

// TestTransformStub_BodyTooLarge_Returns413 prevents OOM/DoS by rejecting bodies exceeding 10 MiB.
// See SYM_GO_0089.
func TestTransformStub_BodyTooLarge_Returns413(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	// Build a body that exceeds the 10 MiB limit (10*1024*1024 + 1 bytes).
	// Use a simple JSON prefix followed by padding so it is structurally
	// plausible but still exceeds the limit.
	const limit = 10 << 20 // 10 MiB — must match maxTransformBodyBytes
	oversized := make([]byte, limit+1)
	copy(oversized, []byte(`{"resourceType":"Parameters","parameter":[],"_pad":"`))
	for i := 50; i < len(oversized)-2; i++ {
		oversized[i] = 'x'
	}
	oversized[len(oversized)-2] = '"'
	oversized[len(oversized)-1] = '}'

	resp, err := http.Post(
		ts.URL+"/fhir/StructureMap/$transform",
		"application/fhir+json",
		bytes.NewReader(oversized),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode,
		"bodies exceeding 10 MiB must be rejected with 413, not buffered")

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
}

// TestTransformStub_BodyAtLimit_NotRejected guards against off-by-one errors in size limit checking.
func TestTransformStub_BodyAtLimit_NotRejected(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	// A body exactly 1 byte under the limit must NOT return 413.
	// We use a valid-looking Parameters JSON followed by padding inside a
	// string value — the transform will fail (bad input) but must NOT 413.
	const limit = 10 << 20
	body := make([]byte, limit-1)
	prefix := []byte(`{"resourceType":"Parameters","parameter":[{"name":"source","valueString":"`)
	suffix := []byte(`"}]}`)
	copy(body, prefix)
	for i := len(prefix); i < len(body)-len(suffix); i++ {
		body[i] = 'x'
	}
	copy(body[len(body)-len(suffix):], suffix)

	resp, err := http.Post(
		ts.URL+"/fhir/StructureMap/$transform",
		"application/fhir+json",
		bytes.NewReader(body),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Must NOT be 413 — any other status (400, 422, 501, etc.) is acceptable
	// since the body content is intentionally garbage; what matters is the
	// size limit is not incorrectly triggered.
	assert.NotEqual(t, http.StatusRequestEntityTooLarge, resp.StatusCode,
		"a body 1 byte under the limit must not be rejected as too large")
}

// TestTransformStub_SmallBody_Accepted is a sanity-check that normal-sized requests work.
func TestTransformStub_SmallBody_Accepted(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	// Minimal valid Parameters with no recognised source — transform engine
	// will return 400/422, but must NOT return 413.
	body := []byte(`{"resourceType":"Parameters","parameter":[{"name":"source","valueString":"http://example.org/sm/not-found"}]}`)
	resp, err := http.Post(
		ts.URL+"/fhir/StructureMap/$transform",
		"application/fhir+json",
		bytes.NewReader(body),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.NotEqual(t, http.StatusRequestEntityTooLarge, resp.StatusCode,
		"a small valid body must never be rejected with 413")
	assert.NotEqual(t, http.StatusInternalServerError, resp.StatusCode,
		"a small body must not cause a server panic or 500")
}

func TestHandler_StructureMap_Update_OnDeletedResource_Returns410(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	// Create.
	createResp, err := http.Post(ts.URL+"/fhir/R5/StructureMap", "application/fhir+json", bytes.NewReader(validStructureMapJSON()))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	var created structuremap.StructureMap
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))

	// Delete.
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/R5/StructureMap/"+created.ID, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	defer delResp.Body.Close()
	require.Equal(t, http.StatusNoContent, delResp.StatusCode)

	// PUT against the tombstoned id with a valid body → 410 Gone.
	putReq, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/R5/StructureMap/"+created.ID, bytes.NewReader(validStructureMapJSON()))
	putReq.Header.Set("Content-Type", "application/fhir+json")
	putResp, err := http.DefaultClient.Do(putReq)
	require.NoError(t, err)
	defer putResp.Body.Close()
	assert.Equal(t, http.StatusGone, putResp.StatusCode, "PUT to tombstoned id must return 410 (not upsert)")

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(putResp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue must be an array")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "gone", issue["code"])
}

func TestHandler_StructureMap_Delete_AlreadyDeleted_Returns204(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	// Create.
	createResp, err := http.Post(ts.URL+"/fhir/R5/StructureMap", "application/fhir+json", bytes.NewReader(validStructureMapJSON()))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	var created structuremap.StructureMap
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))

	// First DELETE → 204.
	req1, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/R5/StructureMap/"+created.ID, nil)
	resp1, err := http.DefaultClient.Do(req1)
	require.NoError(t, err)
	defer resp1.Body.Close()
	require.Equal(t, http.StatusNoContent, resp1.StatusCode)

	// Second DELETE → 204 (idempotent).
	req2, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/R5/StructureMap/"+created.ID, nil)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp2.StatusCode, "repeat DELETE must be idempotent (204, not 410 or 404)")
}

func TestHandler_StructureMap_Vread_DeleteVersion_Returns410(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	// Create (vid=1) then Delete (vid=2 via mock's history append).
	createResp, err := http.Post(ts.URL+"/fhir/R5/StructureMap", "application/fhir+json", bytes.NewReader(validStructureMapJSON()))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	var created structuremap.StructureMap
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))

	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/R5/StructureMap/"+created.ID, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	defer delResp.Body.Close()
	require.Equal(t, http.StatusNoContent, delResp.StatusCode)

	// Vread of the create snapshot (vid=1) — 200 with body.
	vrCreate, err := http.Get(ts.URL + "/fhir/R5/StructureMap/" + created.ID + "/_history/1")
	require.NoError(t, err)
	defer vrCreate.Body.Close()
	assert.Equal(t, http.StatusOK, vrCreate.StatusCode, "vread of create version returns the snapshot")

	// Vread of the delete version (vid=2) — 410 Gone, no resource body, OperationOutcome.
	vrDelete, err := http.Get(ts.URL + "/fhir/R5/StructureMap/" + created.ID + "/_history/2")
	require.NoError(t, err)
	defer vrDelete.Body.Close()
	assert.Equal(t, http.StatusGone, vrDelete.StatusCode, "vread of the delete version must be 410")

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(vrDelete.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"], "vread/delete must return OperationOutcome, not a StructureMap")
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "gone", issue["code"])
}

func TestHandler_StructureMap_R4_RejectsR5OnlyFields_OnCreate(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	cases := []struct {
		name  string
		field string
		body  map[string]any
	}{
		{
			name:  "copyrightLabel",
			field: "copyrightLabel",
			body: map[string]any{
				"resourceType":   "StructureMap",
				"name":           "R4CL",
				"status":         "active",
				"copyrightLabel": "Some Label",
				"group": []any{map[string]any{
					"name":  "g",
					"input": []any{map[string]any{"name": "src", "mode": "source"}, map[string]any{"name": "tgt", "mode": "target"}},
					"rule":  []any{map[string]any{"name": "r"}},
				}},
			},
		},
		{
			name:  "versionAlgorithmString",
			field: "versionAlgorithmString",
			body: map[string]any{
				"resourceType":           "StructureMap",
				"name":                   "R4VAS",
				"status":                 "active",
				"versionAlgorithmString": "semver",
				"group": []any{map[string]any{
					"name":  "g",
					"input": []any{map[string]any{"name": "src", "mode": "source"}, map[string]any{"name": "tgt", "mode": "target"}},
					"rule":  []any{map[string]any{"name": "r"}},
				}},
			},
		},
		{
			name:  "versionAlgorithmCoding",
			field: "versionAlgorithmCoding",
			body: map[string]any{
				"resourceType":           "StructureMap",
				"name":                   "R4VAC",
				"status":                 "active",
				"versionAlgorithmCoding": map[string]any{"system": "http://example.org/a", "code": "x"},
				"group": []any{map[string]any{
					"name":  "g",
					"input": []any{map[string]any{"name": "src", "mode": "source"}, map[string]any{"name": "tgt", "mode": "target"}},
					"rule":  []any{map[string]any{"name": "r"}},
				}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.body)
			resp, err := http.Post(ts.URL+"/fhir/R4/StructureMap", "application/fhir+json", bytes.NewReader(body))
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "R4 must reject R5-only field %q on Create", tc.field)

			var outcome map[string]any
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
			assert.Equal(t, "OperationOutcome", outcome["resourceType"])
			issues, ok := outcome["issue"].([]any)
			require.True(t, ok)
			require.NotEmpty(t, issues)
			issue, ok := issues[0].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "not-supported", issue["code"])
			diag, _ := issue["diagnostics"].(string)
			assert.Contains(t, diag, tc.field, "diagnostics must name the offending R5-only field")
		})
	}

	r5Body, _ := json.Marshal(map[string]any{
		"resourceType":   "StructureMap",
		"name":           "R5OK",
		"status":         "active",
		"copyrightLabel": "Allowed on R5",
		"group": []any{map[string]any{
			"name":  "g",
			"input": []any{map[string]any{"name": "src", "mode": "source"}, map[string]any{"name": "tgt", "mode": "target"}},
			"rule":  []any{map[string]any{"name": "r"}},
		}},
	})
	r5Resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap", "application/fhir+json", bytes.NewReader(r5Body))
	require.NoError(t, err)
	defer r5Resp.Body.Close()
	assert.Equal(t, http.StatusCreated, r5Resp.StatusCode, "R5 must still accept R5-only fields")
}

// patientToPatientInlineMap returns an inline StructureMap that maps Patient by copying id and active fields.
func patientToPatientInlineMap() map[string]any {
	return map[string]any{
		"resourceType": "StructureMap",
		"url":          "http://example.org/sm/pat-r4-to-r5",
		"name":         "PatientR4ToR5",
		"status":       "active",
		"group": []any{
			map[string]any{
				"name": "MapPatient",
				"input": []any{
					map[string]any{"name": "src", "type": "Patient", "mode": "source"},
					map[string]any{"name": "tgt", "type": "Patient", "mode": "target"},
				},
				"rule": []any{
					map[string]any{
						"name": "copyId",
						"source": []any{map[string]any{
							"context":  "src",
							"element":  "id",
							"variable": "vid",
						}},
						"target": []any{map[string]any{
							"context":   "tgt",
							"element":   "id",
							"transform": "copy",
							"parameter": []any{map[string]any{"valueId": "vid"}},
						}},
					},
					map[string]any{
						"name": "copyActive",
						"source": []any{map[string]any{
							"context":  "src",
							"element":  "active",
							"variable": "vact",
						}},
						"target": []any{map[string]any{
							"context":   "tgt",
							"element":   "active",
							"transform": "copy",
							"parameter": []any{map[string]any{"valueId": "vact"}},
						}},
					},
				},
			},
		},
	}
}

// TestHandler_Transform_PatientR4ToR5_Happy_Returns200 verifies successful transform returns 200 with resourceType injected.
func TestHandler_Transform_PatientR4ToR5_Happy_Returns200(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	// R5 spec form: sourceMap (inline StructureMap) + content (input resource).
	r5Body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "sourceMap", "resource": patientToPatientInlineMap()},
			map[string]any{"name": "content", "resource": map[string]any{
				"resourceType": "Patient",
				"id":           "pat-r4-1",
				"active":       true,
				"name": []any{map[string]any{
					"use":    "official",
					"family": "Lovelace",
				}},
			}},
		},
	})
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(r5Body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/fhir+json", resp.Header.Get("Content-Type"))

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	// AC-1: response resourceType must be "Patient"
	assert.Equal(t, "Patient", result["resourceType"],
		"AC-1: response resourceType must match StructureMap declared target type")
	// AC-2: resourceType matches StructureMap's first target-mode input type
	assert.Equal(t, "pat-r4-1", result["id"], "AC-1: id must be copied")
	assert.Equal(t, true, result["active"], "AC-1: active must be copied")

	r4Body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "structureMap", "resource": patientToPatientInlineMap()},
			map[string]any{"name": "input", "part": []any{
				map[string]any{"name": "source", "resource": map[string]any{
					"resourceType": "Patient",
					"id":           "pat-r4-2",
					"active":       false,
				}},
			}},
		},
	})
	resp2, err := http.Post(ts.URL+"/fhir/R4/StructureMap/$transform", "application/fhir+json", bytes.NewReader(r4Body))
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode, "R4 HAPI extension form must work for Patient→Patient map")

	var result2 map[string]any
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&result2))
	assert.Equal(t, "Patient", result2["resourceType"],
		"AC-2: R4 path must also inject resourceType from StructureMap target type")
}

// TestHandler_Transform_InputMissingRequiredFields_Returns422 verifies input validation failure returns 422.
func TestHandler_Transform_InputMissingRequiredFields_Returns422(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	// Only "resourceType" — no actual fields.
	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "sourceMap", "resource": patientToPatientInlineMap()},
			map[string]any{"name": "content", "resource": map[string]any{
				"resourceType": "Patient",
			}},
		},
	})

	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue array must be present")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok, "issue[0] must be a map")
	assert.Equal(t, "invalid", issue["code"])
	diag, _ := issue["diagnostics"].(string)
	assert.Contains(t, diag, "The input resource failed validation")
	assert.NotContains(t, diag, "input resource missing required fields",
		"[SYM-GR-0013]: raw sentinel text must not appear in response")
}

// TestHandler_Transform_InputTypeMismatch_Returns422 verifies type mismatch returns 422 with both types named.
func TestHandler_Transform_InputTypeMismatch_Returns422(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	// StructureMap declares "Patient" source; we send "Observation".
	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "sourceMap", "resource": patientToPatientInlineMap()},
			map[string]any{"name": "content", "resource": map[string]any{
				"resourceType": "Observation",
				"id":           "obs-1",
				"status":       "final",
				"code":         map[string]any{"text": "Blood pressure"},
			}},
		},
	})

	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	assert.Equal(t, "application/fhir+json", resp.Header.Get("Content-Type"),
		"AC-4: 422 OperationOutcome must carry FHIR content type")

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue array must be present")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok, "issue[0] must be a map")
	assert.Equal(t, "invalid", issue["code"])
	diag, _ := issue["diagnostics"].(string)
	assert.Contains(t, diag, "Patient", "diagnostic must name expected type")
	assert.Contains(t, diag, "Observation", "diagnostic must name received type")
}

// TestHandler_Transform_NonExistentMapId_Returns404 verifies missing map returns 404 with generic diagnostic.
func TestHandler_Transform_NonExistentMapId_Returns404(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "content", "resource": map[string]any{
				"resourceType": "Patient",
				"id":           "p1",
				"active":       true,
			}},
		},
	})

	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/does-not-exist/$transform",
		"application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue array must be present")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok, "issue[0] must be a map")
	assert.Equal(t, "not-found", issue["code"])
	diag, _ := issue["diagnostics"].(string)
	assert.Equal(t, "Resource not found", diag, "diagnostic must be generic")
}

// TestHandler_Transform_UnresolvedOtherMap_Returns422NotFound verifies unresolved dependent group returns 422.
func TestHandler_Transform_UnresolvedOtherMap_Returns422NotFound(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	// StructureMap with a dependent referencing "NonExistentGroup".
	smWithUnresolved := map[string]any{
		"resourceType": "StructureMap",
		"url":          "http://example.org/sm/unresolved-dep-handler",
		"name":         "UnresolvedDepHandler",
		"status":       "active",
		"group": []any{
			map[string]any{
				"name": "Main",
				"input": []any{
					map[string]any{"name": "src", "type": "Patient", "mode": "source"},
					map[string]any{"name": "tgt", "type": "Patient", "mode": "target"},
				},
				"rule": []any{
					map[string]any{
						"name":   "callMissing",
						"source": []any{map[string]any{"context": "src"}},
						"target": []any{map[string]any{"context": "tgt"}},
						"dependent": []any{map[string]any{
							"name":      "NonExistentGroup",
							"parameter": []any{map[string]any{"valueId": "src"}, map[string]any{"valueId": "tgt"}},
						}},
					},
				},
			},
		},
	}

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "sourceMap", "resource": smWithUnresolved},
			map[string]any{"name": "content", "resource": map[string]any{
				"resourceType": "Patient",
				"id":           "pat-ac6",
				"active":       true,
			}},
		},
	})

	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue array must be present")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok, "issue[0] must be a map")
	assert.Equal(t, "not-found", issue["code"])
	diag, _ := issue["diagnostics"].(string)
	assert.Contains(t, diag, "NonExistentGroup", "diagnostic must name the missing group")
}

// setupStructureMapServerWithResolver returns a server with engine wired to a MapResolver.
func setupStructureMapServerWithResolver(t *testing.T) (*httptest.Server, *smInMemoryRepo) {
	t.Helper()
	repo := newSMInMemoryRepo()
	service := structuremap.NewService(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := transform.New(transform.WithMapResolver(service))
	mux := http.NewServeMux()
	h := NewStructureMapHandler(service, "http://localhost", logger).WithHistory(repo).WithTransformEngine(eng)
	h.RegisterRoutes(mux)
	h.RegisterRoutesAtPrefix(mux, "R5")
	r4 := NewR4StructureMapHandler(service, "http://localhost", logger).WithHistory(repo).WithTransformEngine(eng)
	r4.RegisterRoutes(mux)
	wrapped := Middleware(mux, MaxBodyBytesMiddleware(10<<20))
	return httptest.NewServer(wrapped), repo
}

// TestHandler_Transform_SourceCheckFails_Returns422Invariant verifies check failure returns 422 with expression.
func TestHandler_Transform_SourceCheckFails_Returns422Invariant(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	// StructureMap whose rule has a check that always fails.
	smWithCheck := map[string]any{
		"resourceType": "StructureMap",
		"url":          "http://example.org/sm/check-test",
		"name":         "CheckTest",
		"status":       "active",
		"group": []any{
			map[string]any{
				"name": "g",
				"input": []any{
					map[string]any{"name": "src", "mode": "source"},
					map[string]any{"name": "tgt", "mode": "target"},
				},
				"rule": []any{
					map[string]any{
						"name": "checkRule",
						"source": []any{map[string]any{
							"context":  "src",
							"element":  "id",
							"variable": "v",
							"check":    "$this = 'expected-value'",
						}},
						"target": []any{map[string]any{
							"context":   "tgt",
							"element":   "id",
							"transform": "copy",
							"parameter": []any{map[string]any{"valueId": "v"}},
						}},
					},
				},
			},
		},
	}

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "sourceMap", "resource": smWithCheck},
			map[string]any{"name": "content", "resource": map[string]any{
				"resourceType": "Patient",
				"id":           "wrong-value",
				"active":       true,
			}},
		},
	})

	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue array must be present")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok, "issue[0] must be a map")
	assert.Equal(t, "invariant", issue["code"], "check failure must use FHIR code 'invariant'")
	diag, _ := issue["diagnostics"].(string)
	assert.Contains(t, diag, "$this = 'expected-value'", "diagnostic must contain the check expression")
}

// TestHandler_Transform_ImportsResolveAcrossMaps verifies imports resolve groups across persisted maps.
func TestHandler_Transform_ImportsResolveAcrossMaps(t *testing.T) {
	ts, repo := setupStructureMapServerWithResolver(t)
	defer ts.Close()

	// Persist Map B with group copyId.
	mapBResource := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		ID:           "map-b",
		URL:          "http://example.org/mapB",
		Name:         "MapB",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "copyId",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "cp",
				Source: []structuremap.Source{{Context: "src", Element: "id", Variable: "v"}},
				Target: []structuremap.Target{{Context: "tgt", Element: "id", Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "v"}}}},
			}},
		}},
	}
	_, err := repo.Create(context.Background(), mapBResource)
	require.NoError(t, err)

	// Inline Map A that imports B and delegates to copyId.
	mapAInline := map[string]any{
		"resourceType": "StructureMap",
		"url":          "http://example.org/mapA",
		"name":         "MapA",
		"status":       "active",
		"import":       []any{"http://example.org/mapB"},
		"group": []any{
			map[string]any{
				"name": "entry",
				"input": []any{
					map[string]any{"name": "src", "mode": "source"},
					map[string]any{"name": "tgt", "mode": "target"},
				},
				"rule": []any{
					map[string]any{
						"name":   "delegate",
						"source": []any{map[string]any{"context": "src"}},
						"target": []any{map[string]any{"context": "tgt"}},
						"dependent": []any{map[string]any{
							"name":      "copyId",
							"parameter": []any{map[string]any{"valueId": "src"}, map[string]any{"valueId": "tgt"}},
						}},
					},
				},
			},
		},
	}

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "sourceMap", "resource": mapAInline},
			map[string]any{"name": "content", "resource": map[string]any{
				"resourceType": "Patient",
				"id":           "pat-imports",
				"active":       true,
			}},
		},
	})

	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "imports must resolve group from imported map → 200")

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "pat-imports", result["id"], "imported group must execute and copy id")
}

// TestHandler_Transform_ThenMapURL_Unresolved_Returns422NotFound verifies unresolved then-map URL returns 422.
func TestHandler_Transform_ThenMapURL_Unresolved_Returns422NotFound(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	// StructureMap with a Dependent that has a MapURL not in the repo.
	smWithMapURL := map[string]any{
		"resourceType": "StructureMap",
		"url":          "http://example.org/sm/then-map-url",
		"name":         "ThenMapURL",
		"status":       "active",
		"group": []any{
			map[string]any{
				"name": "entry",
				"input": []any{
					map[string]any{"name": "src", "mode": "source"},
					map[string]any{"name": "tgt", "mode": "target"},
				},
				"rule": []any{
					map[string]any{
						"name":   "delegate",
						"source": []any{map[string]any{"context": "src"}},
						"target": []any{map[string]any{"context": "tgt"}},
						"dependent": []any{map[string]any{
							"name":      "copyId",
							"parameter": []any{map[string]any{"valueId": "src"}, map[string]any{"valueId": "tgt"}},
						}},
					},
				},
			},
		},
	}

	fmlText := `
		map "http://example.org/sm/then-map-url" = "ThenMapURL"
		group entry(source src, target tgt) {
			src -> tgt then map "http://example.org/missing-B" group copyId(src, tgt);
		}
	`
	_ = smWithMapURL // not used; we pass FML inline

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "fml", "valueString": fmlText},
			map[string]any{"name": "content", "resource": map[string]any{
				"resourceType": "Patient",
				"id":           "x",
				"active":       true,
			}},
		},
	})

	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue array must be present")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok, "issue[0] must be a map")
	assert.Equal(t, "not-found", issue["code"], "unresolved then-map URL must return 'not-found'")
	diag, _ := issue["diagnostics"].(string)
	assert.Contains(t, diag, "http://example.org/missing-B", "diagnostic must name the canonical URL")
}

// TestHandler_Transform_UnresolvedMapURL_Returns422NotFound verifies unresolved map parameter on type-level $transform returns 422.
func TestHandler_Transform_UnresolvedMapURL_Returns422NotFound(t *testing.T) {
	ts, _ := setupStructureMapServerWithEngine(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "map", "valueString": "http://example.org/not-loaded"},
			map[string]any{"name": "content", "resource": map[string]any{
				"resourceType": "Patient",
				"id":           "x",
				"active":       true,
			}},
		},
	})

	for _, endpoint := range []string{
		"/fhir/R5/StructureMap/$transform",
		"/fhir/R4/StructureMap/$transform",
	} {
		t.Run(endpoint, func(t *testing.T) {
			resp, err := http.Post(ts.URL+endpoint, "application/fhir+json", bytes.NewReader(body))
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
				"unresolved map URL must return 422 (not 404) on type-level endpoint")

			var outcome map[string]any
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
			assert.Equal(t, "OperationOutcome", outcome["resourceType"])
			issues, ok := outcome["issue"].([]any)
			require.True(t, ok, "issue array must be present")
			require.NotEmpty(t, issues)
			issue, ok := issues[0].(map[string]any)
			require.True(t, ok, "issue[0] must be a map")
			assert.Equal(t, "not-found", issue["code"], "issue code must be 'not-found'")
			diag, _ := issue["diagnostics"].(string)
			assert.Contains(t, diag, "http://example.org/not-loaded", "diagnostic must name the canonical URL")
		})
	}
}

// setupSMServerWithTypeResolver wires an engine with map and type resolvers; sdRepo exposed for pre-loading definitions.
func setupSMServerWithTypeResolver(t *testing.T) (*httptest.Server, *smInMemoryRepo, *sdInMemoryRepo) {
	t.Helper()
	smRepo := newSMInMemoryRepo()
	sdRepo := newSDInMemoryRepo()
	smService := structuremap.NewService(smRepo)
	sdService := structuredefinition.NewService(sdRepo)
	typeResolver := resolver.NewResolver(sdRepo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := transform.New(
		transform.WithMapResolver(smService),
		transform.WithTypeResolver(typeResolver),
	)
	mux := http.NewServeMux()
	h := NewStructureMapHandler(smService, "http://localhost", logger).WithHistory(smRepo).WithTransformEngine(eng)
	h.RegisterRoutes(mux)
	h.RegisterRoutesAtPrefix(mux, "R5")
	r4 := NewR4StructureMapHandler(smService, "http://localhost", logger).WithHistory(smRepo).WithTransformEngine(eng)
	r4.RegisterRoutes(mux)
	sdH := NewStructureDefinitionHandler(sdService, "http://localhost", logger).WithHistory(sdRepo)
	sdH.RegisterRoutes(mux)
	wrapped := Middleware(mux, MaxBodyBytesMiddleware(10<<20))
	return httptest.NewServer(wrapped), smRepo, sdRepo
}

// TestHandler_Transform_CanonicalURL_Patient_Resolves verifies canonical URL types resolve correctly.
func TestHandler_Transform_CanonicalURL_Patient_Resolves(t *testing.T) {
	ts, _, _ := setupSMServerWithTypeResolver(t)
	defer ts.Close()

	smBody := map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{
				"name": "sourceMap",
				"resource": map[string]any{
					"resourceType": "StructureMap",
					"url":          "http://example.org/sm/canonical-patient",
					"name":         "CanonicalPatient",
					"status":       "active",
					"group": []any{
						map[string]any{
							"name": "g",
							"input": []any{
								map[string]any{
									"name": "src",
									"type": "http://hl7.org/fhir/StructureDefinition/Patient",
									"mode": "source",
								},
								map[string]any{"name": "tgt", "mode": "target"},
							},
							"rule": []any{
								map[string]any{
									"name":   "r",
									"source": []any{map[string]any{"context": "src", "element": "id", "variable": "v"}},
									"target": []any{map[string]any{
										"context": "tgt", "element": "id", "transform": "copy",
										"parameter": []any{map[string]any{"valueId": "v"}},
									}},
								},
							},
						},
					},
				},
			},
			map[string]any{
				"name": "content",
				"resource": map[string]any{
					"resourceType": "Patient",
					"id":           "p1",
					"active":       true,
				},
			},
		},
	}
	body, _ := json.Marshal(smBody)
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "canonical URL must resolve and pass type check")
}

// TestHandler_Transform_ProfileURL_ChainsToBase_Resolves verifies profile URLs chain via baseDefinition.
func TestHandler_Transform_ProfileURL_ChainsToBase_Resolves(t *testing.T) {
	ts, _, sdRepo := setupSMServerWithTypeResolver(t)
	defer ts.Close()

	// Pre-load the profile StructureDefinition into the registry.
	profileURL := "http://my-hospital.org/fhir/StructureDefinition/MyPatientProfile"
	sd := &structuredefinition.StructureDefinition{
		ID:             "my-patient-profile",
		ResourceType:   "StructureDefinition",
		URL:            profileURL,
		Name:           "MyPatientProfile",
		Status:         "active",
		Kind:           "resource",
		Type:           "Patient",
		BaseDefinition: "http://hl7.org/fhir/StructureDefinition/Patient",
		Derivation:     "constraint",
	}
	sdRepo.store[sd.ID] = sd
	sdRepo.byURL[profileURL] = sd

	smBody := map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{
				"name": "sourceMap",
				"resource": map[string]any{
					"resourceType": "StructureMap",
					"url":          "http://example.org/sm/profile-patient",
					"name":         "ProfilePatient",
					"status":       "active",
					"group": []any{
						map[string]any{
							"name": "g",
							"input": []any{
								map[string]any{"name": "src", "type": profileURL, "mode": "source"},
								map[string]any{"name": "tgt", "mode": "target"},
							},
							"rule": []any{
								map[string]any{
									"name":   "r",
									"source": []any{map[string]any{"context": "src", "element": "id", "variable": "v"}},
									"target": []any{map[string]any{
										"context": "tgt", "element": "id", "transform": "copy",
										"parameter": []any{map[string]any{"valueId": "v"}},
									}},
								},
							},
						},
					},
				},
			},
			map[string]any{
				"name": "content",
				"resource": map[string]any{
					"resourceType": "Patient",
					"id":           "p2",
					"active":       true,
				},
			},
		},
	}
	body, _ := json.Marshal(smBody)
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "profile URL must chain and pass type check")
}

// TestHandler_Transform_UnknownCanonicalURL_Returns422NotFound verifies unknown URL returns 422 with remediation.
func TestHandler_Transform_UnknownCanonicalURL_Returns422NotFound(t *testing.T) {
	ts, _, _ := setupSMServerWithTypeResolver(t)
	defer ts.Close()

	unknownURL := "http://example.org/fhir/StructureDefinition/Unknown"
	smBody := map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{
				"name": "sourceMap",
				"resource": map[string]any{
					"resourceType": "StructureMap",
					"url":          "http://example.org/sm/unknown-type",
					"name":         "UnknownType",
					"status":       "active",
					"group": []any{
						map[string]any{
							"name": "g",
							"input": []any{
								map[string]any{"name": "src", "type": unknownURL, "mode": "source"},
								map[string]any{"name": "tgt", "mode": "target"},
							},
							"rule": []any{
								map[string]any{
									"name":   "r",
									"source": []any{map[string]any{"context": "src"}},
									"target": []any{map[string]any{"context": "tgt", "transform": "copy"}},
								},
							},
						},
					},
				},
			},
			map[string]any{
				"name": "content",
				"resource": map[string]any{
					"resourceType": "Patient",
					"id":           "p3",
					"active":       true,
				},
			},
		},
	}
	body, _ := json.Marshal(smBody)
	resp, err := http.Post(ts.URL+"/fhir/R5/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, "unknown URL must return 422")
	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue array must be present")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok, "issue[0] must be a map")
	assert.Equal(t, "not-found", issue["code"], "code must be not-found")
	diag, _ := issue["diagnostics"].(string)
	assert.Contains(t, diag, unknownURL, "diagnostic must name the unresolved URL")
	assert.Contains(t, diag, "POST the StructureDefinition", "diagnostic must contain remediation phrase")
}

// TestHandler_Transform_UnknownCanonicalURL_R4_Returns422NotFound verifies unknown URL on R4 path returns 422.
func TestHandler_Transform_UnknownCanonicalURL_R4_Returns422NotFound(t *testing.T) {
	ts, _, _ := setupSMServerWithTypeResolver(t)
	defer ts.Close()

	unknownURL := "http://example.org/fhir/StructureDefinition/UnknownR4"
	smBody := map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{
				"name": "structureMap",
				"resource": map[string]any{
					"resourceType": "StructureMap",
					"url":          "http://example.org/sm/unknown-type-r4",
					"name":         "UnknownTypeR4",
					"status":       "active",
					"group": []any{
						map[string]any{
							"name": "g",
							"input": []any{
								map[string]any{"name": "src", "type": unknownURL, "mode": "source"},
								map[string]any{"name": "tgt", "mode": "target"},
							},
							"rule": []any{
								map[string]any{
									"name":   "r",
									"source": []any{map[string]any{"context": "src"}},
									"target": []any{map[string]any{"context": "tgt", "transform": "copy"}},
								},
							},
						},
					},
				},
			},
			map[string]any{
				"name": "input",
				"part": []any{
					map[string]any{
						"name": "source",
						"resource": map[string]any{
							"resourceType": "Patient",
							"id":           "p4",
							"active":       true,
						},
					},
				},
			},
		},
	}
	body, _ := json.Marshal(smBody)
	resp, err := http.Post(ts.URL+"/fhir/R4/StructureMap/$transform", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, "unknown URL must return 422 on R4")
	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue array must be present")
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "not-found", issue["code"])
}

func TestHandler_StructureMap_History_Returns200(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	smBody, _ := json.Marshal(map[string]any{
		"resourceType": "StructureMap",
		"group": []map[string]any{
			{"input": []map[string]any{{"name": "src"}}, "rule": []map[string]any{{"name": "r1"}}},
		},
	})
	createResp, err := http.Post(ts.URL+"/fhir/StructureMap?_validate=lenient",
		"application/fhir+json", bytes.NewReader(smBody))
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))
	createResp.Body.Close()
	id, _ := created["id"].(string)
	require.NotEmpty(t, id)

	histResp, err := http.Get(ts.URL + "/fhir/StructureMap/" + id + "/_history")
	require.NoError(t, err)
	defer histResp.Body.Close()
	require.Equal(t, http.StatusOK, histResp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(histResp.Body).Decode(&bundle))
	assert.Equal(t, "Bundle", bundle["resourceType"])
}

func TestHandler_StructureMap_R4_Create_BadJSON_Returns400(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/fhir/R4/StructureMap",
		bytes.NewReader([]byte(`{not valid json`)))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_StructureMap_Search_CountOffset(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	searchResp, err := http.Get(ts.URL + "/fhir/StructureMap?_count=5&_offset=0")
	require.NoError(t, err)
	defer searchResp.Body.Close()
	require.Equal(t, http.StatusOK, searchResp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(searchResp.Body).Decode(&bundle))
	assert.Equal(t, "Bundle", bundle["resourceType"])
}

// TestHandler_StructureMap_Update_IfMatchOnUnknown_Returns412 verifies If-Match on non-existent returns 412 not 201.
func TestHandler_StructureMap_Update_IfMatchOnUnknown_Returns412(t *testing.T) {
	ts, _ := setupStructureMapServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"resourceType": "StructureMap",
		"group": []map[string]any{
			{"input": []map[string]any{{"name": "src"}}, "rule": []map[string]any{{"name": "r1"}}},
		},
	})
	req, _ := http.NewRequest(http.MethodPut,
		ts.URL+"/fhir/StructureMap/ghost-sm?_validate=lenient",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-Match", `W/"1"`)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode, "PUT with If-Match against non-existent must return 412")
}
