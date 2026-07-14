package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/translate"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

type mockRepo struct {
	store      map[string]*conceptmap.ConceptMap
	byURL      map[string]*conceptmap.ConceptMap
	deleted    map[string]bool
	versionSeq int
}

func newMockRepo() *mockRepo {
	return &mockRepo{
		store:   make(map[string]*conceptmap.ConceptMap),
		byURL:   make(map[string]*conceptmap.ConceptMap),
		deleted: make(map[string]bool),
	}
}

// requireIssue decodes a FHIR OperationOutcome from b and asserts it has exactly
// one issue with the given code. Returns the issue map for further assertions.
func requireIssue(t *testing.T, b io.Reader, wantCode string) map[string]any {
	t.Helper()
	var outcome map[string]any
	require.NoError(t, json.NewDecoder(b).Decode(&outcome))
	require.Equal(t, "OperationOutcome", outcome["resourceType"])
	rawIssues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue must be a JSON array")
	require.Len(t, rawIssues, 1, "expected exactly one issue")
	issue, ok := rawIssues[0].(map[string]any)
	require.True(t, ok, "issue[0] must be a JSON object")
	assert.Equal(t, wantCode, issue["code"], "issue[0].code")
	return issue
}

func (m *mockRepo) Create(_ context.Context, cm *conceptmap.ConceptMap) (*conceptmap.ConceptMap, error) {
	m.store[cm.ID] = cm
	if cm.URL != "" {
		m.byURL[cm.URL] = cm
	}
	return cm, nil
}

func (m *mockRepo) Read(_ context.Context, id string) (*conceptmap.ConceptMap, error) {
	if m.deleted[id] {
		return nil, conceptmap.ErrGone
	}
	cm, ok := m.store[id]
	if !ok {
		return nil, conceptmap.ErrNotFound
	}
	return cm, nil
}

func (m *mockRepo) Update(_ context.Context, id string, cm *conceptmap.ConceptMap) (*conceptmap.ConceptMap, error) {
	if m.deleted[id] {
		return nil, conceptmap.ErrGone
	}
	existing, ok := m.store[id]
	if !ok {
		return nil, conceptmap.ErrNotFound
	}
	// Optimistic concurrency: if caller supplied a version, it must match.
	if cm.Meta != nil && cm.Meta.VersionID != "" && existing.Meta != nil {
		if cm.Meta.VersionID != existing.Meta.VersionID {
			return nil, conceptmap.ErrConflict
		}
	}
	cm.ID = id
	m.versionSeq++
	if cm.Meta == nil {
		cm.Meta = &fhir.Meta{}
	}
	cm.Meta.VersionID = fmt.Sprintf("%d", m.versionSeq)
	m.store[id] = cm
	return cm, nil
}

func (m *mockRepo) Delete(_ context.Context, id string) error {
	if m.deleted[id] {
		return conceptmap.ErrGone
	}
	if _, ok := m.store[id]; !ok {
		return conceptmap.ErrNotFound
	}
	delete(m.store, id)
	m.deleted[id] = true
	return nil
}

func (m *mockRepo) Search(_ context.Context, params conceptmap.SearchParams) (*conceptmap.SearchResult, error) {
	var results []conceptmap.ConceptMap
	for _, cm := range m.store {
		if params.Status != "" && cm.Status != params.Status {
			continue
		}
		if params.URL != "" && cm.URL != params.URL {
			continue
		}
		if params.SourceGroupSystem != "" {
			match := false
			for _, g := range cm.Group {
				if g.Source == params.SourceGroupSystem {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		results = append(results, *cm)
	}
	total := len(results)
	if params.Count > 0 && params.Count < total {
		results = results[:params.Count]
	}
	return &conceptmap.SearchResult{ConceptMaps: results, Total: total}, nil
}

func (m *mockRepo) FindByURL(_ context.Context, url string, version string) (*conceptmap.ConceptMap, error) {
	if version != "" {
		for _, cm := range m.store {
			// Skip soft-deleted entries — mirrors the Postgres repo's `AND deleted_at IS NULL` guard.
			if m.deleted[cm.ID] {
				continue
			}
			if cm.URL == url && cm.Version == version {
				return cm, nil
			}
		}
		return nil, conceptmap.ErrNotFound
	}
	cm, ok := m.byURL[url]
	// Honour soft-deletes so mock matches Postgres repo's `AND deleted_at IS NULL` guard.
	if !ok || m.deleted[cm.ID] {
		return nil, conceptmap.ErrNotFound
	}
	return cm, nil
}

func (m *mockRepo) FindBySourceScope(_ context.Context, scope string) (*conceptmap.ConceptMap, error) {
	for _, cm := range m.store {
		if cm.SourceScopeURI == scope || cm.SourceScopeCanonical == scope {
			return cm, nil
		}
	}
	return nil, conceptmap.ErrNotFound
}

func setupTestServer(t *testing.T) (*httptest.Server, *mockRepo) {
	t.Helper()
	repo := newMockRepo()
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mux := http.NewServeMux()
	handler := NewConceptMapHandler(service, engine, "http://localhost", logger)
	handler.RegisterRoutes(mux)

	// Apply the global body-size middleware like production does.
	wrapped := Middleware(mux, MaxBodyBytesMiddleware(10<<20))
	ts := httptest.NewServer(wrapped)
	return ts, repo
}

func TestHandler_Create(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	cm := conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "http://example.org/fhir/ConceptMap/test",
		Status:       "draft",
		Name:         "TestMap",
		Group: []conceptmap.Group{
			{
				Source: "http://source.system",
				Target: "http://target.system",
				Element: []conceptmap.Element{
					{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}},
				},
			},
		},
	}

	body, _ := json.Marshal(cm)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/fhir+json")
	assert.NotEmpty(t, resp.Header.Get("Location"))
	assert.NotEmpty(t, resp.Header.Get("ETag"))

	var result conceptmap.ConceptMap
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err)
	assert.NotEmpty(t, result.ID)
	assert.Equal(t, "ConceptMap", result.ResourceType)
	assert.Equal(t, "draft", result.Status)
}

func TestHandler_Create_InvalidBody(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/fhir/ConceptMap", "application/fhir+json", bytes.NewReader([]byte("not json")))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var outcome map[string]any
	json.NewDecoder(resp.Body).Decode(&outcome)
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
}

func TestHandler_Create_InvalidStatus(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	cm := conceptmap.ConceptMap{
		Status: "invalid",
		Group: []conceptmap.Group{
			{Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}}}},
		},
	}
	body, _ := json.Marshal(cm)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

func TestHandler_Read(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "test-read-id",
		Status:       "active",
		Meta:         &fhir.Meta{VersionID: "1", LastUpdated: "2024-01-01T00:00:00Z"},
	}
	repo.store["test-read-id"] = cm

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap/test-read-id")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result conceptmap.ConceptMap
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Equal(t, "test-read-id", result.ID)
	assert.Equal(t, "active", result.Status)
}

func TestHandler_Read_NotFound(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap/nonexistent")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_Update(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	repo.store["update-id"] = &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "update-id",
		Status:       "draft",
		Meta:         &fhir.Meta{VersionID: "1"},
	}

	updated := conceptmap.ConceptMap{
		Status: "active",
		Name:   "Updated",
		Group: []conceptmap.Group{
			{Element: []conceptmap.Element{{Code: "X", Target: []conceptmap.Target{{Code: "Y", Relationship: "equivalent"}}}}},
		},
	}
	body, _ := json.Marshal(updated)

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/ConceptMap/update-id", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result conceptmap.ConceptMap
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Equal(t, "active", result.Status)
	assert.Equal(t, "Updated", result.Name)
}

func TestHandler_Update_IDMismatch(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	repo.store["id1"] = &conceptmap.ConceptMap{ID: "id1", Status: "draft", Meta: &fhir.Meta{VersionID: "1"}}

	cm := conceptmap.ConceptMap{
		ID:     "different-id",
		Status: "active",
		Group:  []conceptmap.Group{{Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}}}}},
	}
	body, _ := json.Marshal(cm)

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/ConceptMap/id1", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_Delete(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	repo.store["delete-id"] = &conceptmap.ConceptMap{ID: "delete-id", Status: "draft"}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/ConceptMap/delete-id", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestHandler_Delete_NotFound(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/ConceptMap/nonexistent", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_Search(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	repo.store["s1"] = &conceptmap.ConceptMap{ID: "s1", Status: "active", URL: "http://test/1", Meta: &fhir.Meta{VersionID: "1"}}
	repo.store["s2"] = &conceptmap.ConceptMap{ID: "s2", Status: "draft", URL: "http://test/2", Meta: &fhir.Meta{VersionID: "1"}}

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap?status=active")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var bundle fhir.Bundle
	json.NewDecoder(resp.Body).Decode(&bundle)
	assert.Equal(t, "searchset", bundle.Type)
	assert.Equal(t, 1, bundle.Total)
}

func TestHandler_Search_Empty(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var bundle fhir.Bundle
	json.NewDecoder(resp.Body).Decode(&bundle)
	assert.Equal(t, 0, bundle.Total)
}

func TestHandler_TranslateType_GET(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	cm := &conceptmap.ConceptMap{
		ID:     "101",
		URL:    "http://hl7.org/fhir/ConceptMap/101",
		Status: "draft",
		Group: []conceptmap.Group{
			{
				Source: "http://hl7.org/fhir/address-use",
				Target: "http://terminology.hl7.org/CodeSystem/v3-AddressUse",
				Element: []conceptmap.Element{
					{Code: "home", Display: "Home", Target: []conceptmap.Target{
						{Code: "H", Display: "home address", Relationship: "equivalent"},
					}},
				},
			},
		},
	}
	repo.store["101"] = cm
	repo.byURL["http://hl7.org/fhir/ConceptMap/101"] = cm

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap/$translate?url=http://hl7.org/fhir/ConceptMap/101&sourceCode=home&sourceSystem=http://hl7.org/fhir/address-use")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var params fhir.Parameters
	json.NewDecoder(resp.Body).Decode(&params)
	assert.Equal(t, "Parameters", params.ResourceType)

	// Check result=true
	require.NotEmpty(t, params.Parameter)
	assert.Equal(t, "result", params.Parameter[0].Name)
	assert.True(t, *params.Parameter[0].ValueBoolean)
}

func TestHandler_TranslateType_POST(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	cm := &conceptmap.ConceptMap{
		ID:     "101",
		URL:    "http://hl7.org/fhir/ConceptMap/101",
		Status: "draft",
		Group: []conceptmap.Group{
			{
				Source: "http://hl7.org/fhir/address-use",
				Target: "http://terminology.hl7.org/CodeSystem/v3-AddressUse",
				Element: []conceptmap.Element{
					{Code: "work", Target: []conceptmap.Target{
						{Code: "WP", Display: "work place", Relationship: "equivalent"},
					}},
				},
			},
		},
	}
	repo.store["101"] = cm
	repo.byURL["http://hl7.org/fhir/ConceptMap/101"] = cm

	reqBody := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: "http://hl7.org/fhir/ConceptMap/101"},
			{Name: "sourceCode", ValueCode: "work"},
			{Name: "sourceSystem", ValueURI: "http://hl7.org/fhir/address-use"},
		},
	}
	body, _ := json.Marshal(reqBody)

	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var params fhir.Parameters
	json.NewDecoder(resp.Body).Decode(&params)
	assert.True(t, *params.Parameter[0].ValueBoolean)
}

func TestHandler_TranslateInstance(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	cm := &conceptmap.ConceptMap{
		ID:     "101",
		URL:    "http://hl7.org/fhir/ConceptMap/101",
		Status: "draft",
		Group: []conceptmap.Group{
			{
				Source: "http://hl7.org/fhir/address-use",
				Target: "http://terminology.hl7.org/CodeSystem/v3-AddressUse",
				Element: []conceptmap.Element{
					{Code: "temp", Target: []conceptmap.Target{
						{Code: "TMP", Display: "temporary address", Relationship: "equivalent"},
					}},
				},
			},
		},
	}
	repo.store["101"] = cm

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap/101/$translate?sourceCode=temp&sourceSystem=http://hl7.org/fhir/address-use")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var params fhir.Parameters
	json.NewDecoder(resp.Body).Decode(&params)
	assert.True(t, *params.Parameter[0].ValueBoolean)
}

func TestHandler_Translate_MissingParams(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	// No source or target code
	resp, err := http.Get(ts.URL + "/fhir/ConceptMap/$translate?url=http://test")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestHandler_LoadFHIRExamples tests that all 8 official FHIR ConceptMap examples
// can be loaded (deserialized) and stored correctly.
func TestHandler_LoadFHIRExamples(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	examples := []string{
		"../../docs/conceptmap-example-101.json",
		"../../docs/conceptmap-example-103.json",
		"../../docs/conceptmap-example-specimen-type-102.json",
		"../../docs/conceptmap-example-2.json",
		"../../docs/conceptmap-example-metadata.json",
		"../../docs/conceptmap-example-metadata-2.json",
		"../../docs/conceptmap-example-priority.json",
		"../../docs/conceptmap-message-adt-a04-to-bundle.json",
	}

	for _, path := range examples {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err, "failed to read example file: %s", path)

			// Verify it can be deserialized
			var cm conceptmap.ConceptMap
			err = json.Unmarshal(data, &cm)
			require.NoError(t, err, "failed to unmarshal example: %s", path)
			assert.Equal(t, "ConceptMap", cm.ResourceType)
			assert.NotEmpty(t, cm.ID)

			// Verify it can be POSTed to the server
			resp, err := http.Post(ts.URL+"/fhir/ConceptMap", "application/fhir+json", bytes.NewReader(data))
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusCreated, resp.StatusCode, "failed to create example: %s", path)
		})
	}
}

func TestHandler_TranslateType_R4ParamNames(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	cm := &conceptmap.ConceptMap{
		ID:     "r4-test",
		URL:    "http://example.org/r4-compat",
		Status: "active",
		Group: []conceptmap.Group{
			{
				Source: "http://hl7.org/fhir/address-use",
				Target: "http://terminology.hl7.org/CodeSystem/v3-AddressUse",
				Element: []conceptmap.Element{
					{Code: "home", Target: []conceptmap.Target{
						{Code: "H", Display: "home address", Relationship: "equivalent"},
					}},
				},
			},
		},
	}
	repo.store["r4-test"] = cm
	repo.byURL["http://example.org/r4-compat"] = cm

	// Use R4 parameter names: "code" instead of "sourceCode", "system" instead of "sourceSystem"
	resp, err := http.Get(ts.URL + "/fhir/ConceptMap/$translate?url=http://example.org/r4-compat&code=home&system=http://hl7.org/fhir/address-use")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var params fhir.Parameters
	json.NewDecoder(resp.Body).Decode(&params)
	assert.True(t, *params.Parameter[0].ValueBoolean)
}

func TestHandler_TranslateType_R4SourceScope(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	cm := &conceptmap.ConceptMap{
		ID:             "scope-r4",
		URL:            "http://example.org/scope-r4",
		Status:         "active",
		SourceScopeURI: "http://hl7.org/fhir/ValueSet/address-use",
		Group: []conceptmap.Group{
			{
				Source: "http://hl7.org/fhir/address-use",
				Target: "http://tgt",
				Element: []conceptmap.Element{
					{Code: "home", Target: []conceptmap.Target{{Code: "H", Relationship: "equivalent"}}},
				},
			},
		},
	}
	repo.store["scope-r4"] = cm
	repo.byURL["http://example.org/scope-r4"] = cm

	// R4: "source" instead of "sourceScope"
	resp, err := http.Get(ts.URL + "/fhir/ConceptMap/$translate?url=http://example.org/scope-r4&code=home&system=http://hl7.org/fhir/address-use&source=http://hl7.org/fhir/ValueSet/address-use")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHandler_TranslateType_ReverseParam(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	cm := &conceptmap.ConceptMap{
		ID:     "rev-test",
		URL:    "http://example.org/reverse",
		Status: "active",
		Group: []conceptmap.Group{
			{
				Source: "http://src",
				Target: "http://tgt",
				Element: []conceptmap.Element{
					{Code: "A", Display: "Source A", Target: []conceptmap.Target{
						{Code: "B", Display: "Target B", Relationship: "equivalent"},
					}},
				},
			},
		},
	}
	repo.store["rev-test"] = cm
	repo.byURL["http://example.org/reverse"] = cm

	// reverse=true with code=B should find that B maps back to A
	resp, err := http.Get(ts.URL + "/fhir/ConceptMap/$translate?url=http://example.org/reverse&code=B&system=http://tgt&reverse=true")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var params fhir.Parameters
	json.NewDecoder(resp.Body).Decode(&params)
	assert.True(t, *params.Parameter[0].ValueBoolean)
}

// ─── AC-1: POST creates resource with Location, ETag, meta ───────────────────

func TestHandler_Create_R5_Returns201WithLocationETagMeta(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	cm := conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "http://example.org/fhir/ConceptMap/ac1",
		Status:       "draft",
		Group: []conceptmap.Group{
			{
				Source: "http://src",
				Target: "http://tgt",
				Element: []conceptmap.Element{
					{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}},
				},
			},
		},
	}
	body, _ := json.Marshal(cm)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("ETag"))

	var result conceptmap.ConceptMap
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotEmpty(t, result.ID)
	assert.NotNil(t, result.Meta)
	assert.NotEmpty(t, result.Meta.LastUpdated)
	// Location must point to the new resource.
	assert.Contains(t, resp.Header.Get("Location"), "/ConceptMap/"+result.ID)
}

func TestHandler_R4_Read_ProjectsToR4(t *testing.T) {
	repo := newMockRepo()
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	r5mux := http.NewServeMux()
	r5handler := NewConceptMapHandler(service, engine, "http://localhost", logger)
	r5handler.RegisterRoutes(r5mux)

	r4mux := http.NewServeMux()
	r4handler := NewR4ConceptMapHandler(service, engine, "http://localhost", logger)
	r4handler.RegisterRoutes(r4mux)

	r5ts := httptest.NewServer(Middleware(r5mux, MaxBodyBytesMiddleware(10<<20)))
	r4ts := httptest.NewServer(Middleware(r4mux, MaxBodyBytesMiddleware(10<<20)))
	defer r5ts.Close()
	defer r4ts.Close()

	noMap := true
	cm := conceptmap.ConceptMap{
		ResourceType:           "ConceptMap",
		Status:                 "active",
		VersionAlgorithmString: "semver",
		AdditionalAttribute:    []conceptmap.AdditionalAttribute{{Code: "attr1", Type: "code"}},
		Property:               []conceptmap.Property{{Code: "prop1", Type: "string"}},
		Group: []conceptmap.Group{
			{
				Source: "http://src",
				Target: "http://tgt",
				Element: []conceptmap.Element{
					{
						Code:  "X",
						NoMap: &noMap,
						Target: []conceptmap.Target{{
							Code:         "Y",
							Relationship: "equivalent",
							ValueSet:     "http://example.org/vs/target",
							Property:     []conceptmap.TargetProperty{{Code: "tp1"}},
						}},
					},
					{
						ValueSet: "http://example.org/vs/element",
					},
				},
			},
		},
	}
	body, _ := json.Marshal(cm)
	createResp, err := http.Post(r5ts.URL+"/fhir/ConceptMap", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	var created conceptmap.ConceptMap
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))

	r4Resp, err := http.Get(r4ts.URL + "/fhir/R4/ConceptMap/" + created.ID)
	require.NoError(t, err)
	defer r4Resp.Body.Close()
	require.Equal(t, http.StatusOK, r4Resp.StatusCode)

	var raw map[string]any
	require.NoError(t, json.NewDecoder(r4Resp.Body).Decode(&raw))
	assert.Empty(t, raw["versionAlgorithmString"], "R5-only versionAlgorithmString must be absent")
	assert.Empty(t, raw["additionalAttribute"], "R5-only additionalAttribute must be absent")
	assert.Empty(t, raw["property"], "R5-only root property must be absent")

	groups, _ := raw["group"].([]any)
	require.Len(t, groups, 1)
	elements, _ := groups[0].(map[string]any)["element"].([]any)
	require.Len(t, elements, 2)
	elem := elements[0].(map[string]any)
	assert.Empty(t, elem["noMap"], "R5-only noMap must be absent in R4 projection")
	targets, _ := elem["target"].([]any)
	require.Len(t, targets, 1)
	tgt := targets[0].(map[string]any)
	assert.Empty(t, tgt["property"], "R5-only target.property must be absent")
	assert.Empty(t, tgt["relationship"], "relationship must be absent in R4 projection")
	assert.Equal(t, "equivalent", tgt["equivalence"], "equivalence must be populated from relationship")

	assert.Empty(t, tgt["valueSet"], "R5-only target.valueSet must be absent in R4 projection")
	elem2 := elements[1].(map[string]any)
	assert.Empty(t, elem2["valueSet"], "R5-only element.valueSet must be absent in R4 projection")
}

func TestHandler_Search_FHIRControlParams_Return200(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	for _, q := range []string{
		"_format=json",
		"_summary=true",
		"_pretty=true",
		"_elements=id",
		"_total=accurate",
	} {
		t.Run(q, func(t *testing.T) {
			resp, err := http.Get(ts.URL + "/fhir/ConceptMap?" + q)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode,
				"FHIR control param %q must not be rejected as unsupported", q)
		})
	}
}

func TestHandler_Delete_Then_Get_Returns410(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	repo.store["gone-id"] = &conceptmap.ConceptMap{
		ID:     "gone-id",
		Status: "active",
		Meta:   &fhir.Meta{VersionID: "1"},
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/ConceptMap/gone-id", nil)
	delResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer delResp.Body.Close()
	assert.Equal(t, http.StatusNoContent, delResp.StatusCode)

	getResp, err := http.Get(ts.URL + "/fhir/ConceptMap/gone-id")
	require.NoError(t, err)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusGone, getResp.StatusCode)

	issue := requireIssue(t, getResp.Body, "gone")
	assert.Equal(t, "error", issue["severity"])
}

func TestHandler_Create_MissingStatus_Returns422(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	// Valid JSON but missing required `status` field
	body := []byte(`{"resourceType":"ConceptMap","group":[{"element":[{"code":"A","target":[{"code":"B","relationship":"equivalent"}]}]}]}`)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	issue := requireIssue(t, resp.Body, "invalid")
	assert.Equal(t, "error", issue["severity"])
}

func TestHandler_Create_MalformedJSON_Returns400(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/fhir/ConceptMap", "application/fhir+json", bytes.NewReader([]byte("{not valid json")))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
}

func TestHandler_Search_BySourceSystem_ReturnsFilteredBundle(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	icd10 := "http://hl7.org/fhir/sid/icd-10"
	snomed := "http://snomed.info/sct"

	repo.store["cm1"] = &conceptmap.ConceptMap{
		ID: "cm1", Status: "active",
		Meta:  &fhir.Meta{VersionID: "1"},
		Group: []conceptmap.Group{{Source: icd10, Target: snomed}},
	}
	repo.store["cm2"] = &conceptmap.ConceptMap{
		ID: "cm2", Status: "active",
		Meta:  &fhir.Meta{VersionID: "1"},
		Group: []conceptmap.Group{{Source: snomed, Target: icd10}},
	}

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap?source-system=" + icd10)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var bundle fhir.Bundle
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&bundle))
	assert.Equal(t, "searchset", bundle.Type)
	assert.Equal(t, 1, bundle.Total)
}

func TestHandler_Search_NoResults_ReturnsBundleTotalZero(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap?url=http://no-such-url")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var bundle fhir.Bundle
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&bundle))
	assert.Equal(t, "searchset", bundle.Type)
	assert.Equal(t, 0, bundle.Total)
}

func TestHandler_Search_CountZero_Returns400(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()
	// Seed a single resource so the happy paths return a usable body.
	repo.store["a"] = &conceptmap.ConceptMap{ID: "a", Status: "active", Meta: &fhir.Meta{VersionID: "1"}}

	cases := []struct {
		name       string
		query      string
		wantStatus int
		wantDiag   string
	}{
		{"count=0", "_count=0", http.StatusBadRequest, "must be greater than zero"},
		{"count=-1", "_count=-1", http.StatusBadRequest, "must be > 0"},
		{"count=abc", "_count=abc", http.StatusBadRequest, "must be an integer"},
		{"count_overflow", "_count=999999999999999999999", http.StatusBadRequest, "out of range"},
		{"count=2000_clamped", "_count=2000", http.StatusOK, ""},
		{"count_missing", "", http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := ts.URL + "/fhir/ConceptMap"
			if tc.query != "" {
				url += "?" + tc.query
			}
			resp, err := http.Get(url)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tc.wantStatus, resp.StatusCode)
			if tc.wantStatus == http.StatusBadRequest {
				var oo map[string]any
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&oo))
				assert.Equal(t, "OperationOutcome", oo["resourceType"])
				issues, _ := oo["issue"].([]any)
				require.NotEmpty(t, issues)
				iss := issues[0].(map[string]any)
				assert.Equal(t, "invalid", iss["code"])
				diag, _ := iss["diagnostics"].(string)
				assert.Contains(t, diag, tc.wantDiag)
			}
		})
	}
}

func TestHandler_Search_CountClamped(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	// Seed 3 resources
	for i := range 3 {
		id := fmt.Sprintf("clamp-%d", i)
		repo.store[id] = &conceptmap.ConceptMap{ID: id, Status: "active", Meta: &fhir.Meta{VersionID: "1"}}
	}

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap?_count=2000")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var bundle fhir.Bundle
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&bundle))
	assert.Equal(t, 3, bundle.Total)
	assert.Len(t, bundle.Entry, 3)
}

func TestHandler_Search_UnsupportedParam_Returns400(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap?foo=bar")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	issue := requireIssue(t, resp.Body, "not-supported")
	assert.Equal(t, "error", issue["severity"])
}

func TestHandler_Update_VersionConflict_Returns412(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	repo.store["conflict-id"] = &conceptmap.ConceptMap{
		ID:     "conflict-id",
		Status: "active",
		Meta:   &fhir.Meta{VersionID: "1"},
	}

	// No Meta in body — the If-Match header is the sole source of the expected version.
	update := conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "conflict-id",
		Status:       "retired",
	}
	body, _ := json.Marshal(update)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/ConceptMap/conflict-id", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-Match", `W/"99"`) // wrong version: stored is "1"

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
	requireIssue(t, resp.Body, "conflict")
}

func TestHandler_R4_Create_CanonicaliseFromR4(t *testing.T) {
	repo := newMockRepo()
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mux := http.NewServeMux()
	r4handler := NewR4ConceptMapHandler(service, engine, "http://localhost", logger)
	r4handler.RegisterRoutes(mux)
	ts := httptest.NewServer(Middleware(mux, MaxBodyBytesMiddleware(10<<20)))
	defer ts.Close()

	// R4 payload uses `equivalence` (not `relationship`)
	r4body := []byte(`{
		"resourceType": "ConceptMap",
		"status": "draft",
		"group": [{
			"source": "http://src",
			"target": "http://tgt",
			"element": [{
				"code": "A",
				"target": [{"code": "B", "equivalence": "equivalent"}]
			}]
		}]
	}`)

	resp, err := http.Post(ts.URL+"/fhir/R4/ConceptMap", "application/fhir+json", bytes.NewReader(r4body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created conceptmap.ConceptMap
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))

	assert.NotEmpty(t, created.ID)
	require.Len(t, created.Group, 1)
	require.Len(t, created.Group[0].Element, 1)
	require.Len(t, created.Group[0].Element[0].Target, 1)
	assert.Empty(t, created.Group[0].Element[0].Target[0].Relationship)
	assert.Equal(t, "equivalent", created.Group[0].Element[0].Target[0].Equivalence)
}

func TestHandler_Update_OnDeletedResource_Returns410(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	repo.store["resurrect-id"] = &conceptmap.ConceptMap{
		ID: "resurrect-id", Status: "active", Meta: &fhir.Meta{VersionID: "1"},
	}
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/ConceptMap/resurrect-id", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	delResp.Body.Close()
	require.Equal(t, http.StatusNoContent, delResp.StatusCode)

	update := conceptmap.ConceptMap{Status: "active",
		Group: []conceptmap.Group{{Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}}}}},
	}
	body, _ := json.Marshal(update)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/ConceptMap/resurrect-id", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusGone, resp.StatusCode, "PUT on soft-deleted resource must return 410, not resurrect it")
	requireIssue(t, resp.Body, "gone")
}

func TestHandler_Update_IfMatchOnNonExistent_Returns412(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	update := conceptmap.ConceptMap{Status: "active",
		Group: []conceptmap.Group{{Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}}}}},
	}
	body, _ := json.Marshal(update)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/ConceptMap/never-existed", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-Match", `W/"1"`)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode,
		"If-Match against a resource that never existed must be 412, not a 201 upsert")
	requireIssue(t, resp.Body, "conflict")
}

func TestHandler_Update_IfMatchMalformed_Returns400(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()
	repo.store["g1"] = &conceptmap.ConceptMap{
		ID: "g1", Status: "active", Meta: &fhir.Meta{VersionID: "1"},
	}

	cases := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{"no_quotes", "garbage", http.StatusBadRequest},
		{"weak_no_quotes", "W/garbage", http.StatusBadRequest},
		{"unterminated", `"garbage`, http.StatusBadRequest},
		{"no_opening", `garbage"`, http.StatusBadRequest},
		{"empty_quotes", `""`, http.StatusBadRequest},
		{"weak_empty_quotes", `W/""`, http.StatusBadRequest},
		{"valid_but_stale", `W/"3"`, http.StatusPreconditionFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := bytes.NewBufferString(`{"resourceType":"ConceptMap","id":"g1","status":"active"}`)
			req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/ConceptMap/g1", body)
			req.Header.Set("Content-Type", "application/fhir+json")
			req.Header.Set("If-Match", tc.header)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, tc.wantStatus, resp.StatusCode, "header=%q", tc.header)
			if tc.wantStatus == http.StatusBadRequest {
				var oo map[string]any
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&oo))
				issues, _ := oo["issue"].([]any)
				require.NotEmpty(t, issues)
				iss := issues[0].(map[string]any)
				assert.Equal(t, "invalid", iss["code"])
				diag, _ := iss["diagnostics"].(string)
				assert.Contains(t, diag, "If-Match")
			}
		})
	}
}

func TestHandler_Delete_AlreadyDeleted_Returns204(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	repo.store["idem-delete"] = &conceptmap.ConceptMap{
		ID: "idem-delete", Status: "active", Meta: &fhir.Meta{VersionID: "1"},
	}

	for i, expectStatus := range []int{http.StatusNoContent, http.StatusNoContent} {
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/ConceptMap/idem-delete", nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, expectStatus, resp.StatusCode, "DELETE attempt #%d", i+1)
	}
}

func TestHandler_Search_CapAt1000(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	for i := 0; i < 1001; i++ {
		id := fmt.Sprintf("cm-%04d", i)
		repo.store[id] = &conceptmap.ConceptMap{ID: id, Status: "active", Meta: &fhir.Meta{VersionID: "1"}}
	}

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var bundle fhir.Bundle
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&bundle))
	assert.Equal(t, 1001, bundle.Total, "bundle.total reflects the true match count")
	assert.Len(t, bundle.Entry, 1000, "server caps entries at 1000 even when more match")
}

func TestHandler_Search_LegacyParam_SourceGroupSystem(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	icd10 := "http://hl7.org/fhir/sid/icd-10"
	snomed := "http://snomed.info/sct"
	repo.store["legacy-a"] = &conceptmap.ConceptMap{
		ID: "legacy-a", Status: "active", Meta: &fhir.Meta{VersionID: "1"},
		Group: []conceptmap.Group{{Source: icd10, Target: snomed}},
	}
	repo.store["legacy-b"] = &conceptmap.ConceptMap{
		ID: "legacy-b", Status: "active", Meta: &fhir.Meta{VersionID: "1"},
		Group: []conceptmap.Group{{Source: snomed, Target: icd10}},
	}

	legacyResp, err := http.Get(ts.URL + "/fhir/ConceptMap?source-group-system=" + icd10)
	require.NoError(t, err)
	defer legacyResp.Body.Close()
	require.Equal(t, http.StatusOK, legacyResp.StatusCode, "legacy alias must not be rejected as unsupported")

	var legacyBundle fhir.Bundle
	require.NoError(t, json.NewDecoder(legacyResp.Body).Decode(&legacyBundle))

	canonicalResp, err := http.Get(ts.URL + "/fhir/ConceptMap?source-system=" + icd10)
	require.NoError(t, err)
	defer canonicalResp.Body.Close()
	require.Equal(t, http.StatusOK, canonicalResp.StatusCode)

	var canonicalBundle fhir.Bundle
	require.NoError(t, json.NewDecoder(canonicalResp.Body).Decode(&canonicalBundle))

	assert.Equal(t, canonicalBundle.Total, legacyBundle.Total,
		"legacy alias must return same total as canonical source-system")
	assert.Equal(t, 1, legacyBundle.Total)
}

func TestHandler_TranslateType_UnmatchedDependency_EmitsIssueParameter(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	cm := &conceptmap.ConceptMap{
		ID:     "depends-unmatched",
		URL:    "http://example.org/depends-unmatched",
		Status: "active",
		Group: []conceptmap.Group{{
			Source: "http://src",
			Target: "http://tgt",
			Element: []conceptmap.Element{
				{Code: "A", Target: []conceptmap.Target{{
					Code:         "B",
					Relationship: "equivalent",
					DependsOn: []conceptmap.DependsOn{
						{Attribute: "language", ValueString: "en"},
					},
				}}},
			},
		}},
	}
	repo.store[cm.ID] = cm
	repo.byURL[cm.URL] = cm

	reqBody := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: cm.URL},
			{Name: "sourceCode", ValueCode: "A"},
			{Name: "sourceSystem", ValueURI: "http://src"},
			{Name: "dependency", Part: []fhir.Parameter{
				{Name: "attribute", ValueURI: "weather"},
				{Name: "value", ValueString: "sunny"},
			}},
		},
	}
	body, _ := json.Marshal(reqBody)

	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var raw map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&raw))
	params, ok := raw["parameter"].([]any)
	require.True(t, ok, "parameter array missing from response: %v", raw)

	var resultParam, issueParam map[string]any
	for _, p := range params {
		m := p.(map[string]any)
		switch m["name"] {
		case "result":
			resultParam = m
		case "issue":
			issueParam = m
		}
	}
	require.NotNil(t, resultParam, "result parameter missing")
	assert.Equal(t, true, resultParam["valueBoolean"], "result must remain true (pass-through)")

	require.NotNil(t, issueParam, "no issue parameter found")
	res, ok := issueParam["resource"].(map[string]any)
	require.True(t, ok, "issue parameter missing embedded resource: %v", issueParam)
	assert.Equal(t, "OperationOutcome", res["resourceType"])
	issues, ok := res["issue"].([]any)
	require.True(t, ok)
	require.Len(t, issues, 1, "exactly one issue per warning")
	iss := issues[0].(map[string]any)
	assert.Equal(t, "warning", iss["severity"])
	assert.Equal(t, "not-supported", iss["code"])
	diag, _ := iss["diagnostics"].(string)
	assert.Contains(t, diag, "weather")
	assert.Contains(t, diag, "matched no target's dependsOn")
}
