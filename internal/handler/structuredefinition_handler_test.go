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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuredefinition"
)

// sdInMemoryRepo is a minimal in-memory structuredefinition.Repository for
// handler tests.
type sdInMemoryRepo struct {
	store      map[string]*structuredefinition.StructureDefinition
	byURL      map[string]*structuredefinition.StructureDefinition
	deleted    map[string]struct{}
	deletedURL map[string]struct{}
	history    map[string][]structuredefinition.HistoryEntry
}

func newSDInMemoryRepo() *sdInMemoryRepo {
	return &sdInMemoryRepo{
		store:      map[string]*structuredefinition.StructureDefinition{},
		byURL:      map[string]*structuredefinition.StructureDefinition{},
		deleted:    map[string]struct{}{},
		deletedURL: map[string]struct{}{},
		history:    map[string][]structuredefinition.HistoryEntry{},
	}
}

func (m *sdInMemoryRepo) appendHistory(id, op string, sd *structuredefinition.StructureDefinition) {
	var snapshot *structuredefinition.StructureDefinition
	if sd != nil {
		cp := *sd
		snapshot = &cp
	}
	vid := len(m.history[id]) + 1
	m.history[id] = append([]structuredefinition.HistoryEntry{{
		VersionID: vid,
		Operation: op,
		Resource:  snapshot,
	}}, m.history[id]...)
}

func (m *sdInMemoryRepo) Create(_ context.Context, sd *structuredefinition.StructureDefinition) (*structuredefinition.StructureDefinition, error) {
	m.store[sd.ID] = sd
	if sd.URL != "" {
		m.byURL[sd.URL] = sd
	}
	m.appendHistory(sd.ID, "create", sd)
	return sd, nil
}
func (m *sdInMemoryRepo) Read(_ context.Context, id string) (*structuredefinition.StructureDefinition, error) {
	if _, gone := m.deleted[id]; gone {
		return nil, structuredefinition.ErrGone
	}
	sd, ok := m.store[id]
	if !ok {
		return nil, structuredefinition.ErrNotFound
	}
	return sd, nil
}
func (m *sdInMemoryRepo) Update(_ context.Context, id string, sd *structuredefinition.StructureDefinition) (*structuredefinition.StructureDefinition, error) {
	if _, gone := m.deleted[id]; gone {
		return nil, structuredefinition.ErrGone
	}
	if _, ok := m.store[id]; !ok {
		return nil, structuredefinition.ErrNotFound
	}
	sd.ID = id
	m.store[id] = sd
	m.appendHistory(id, "update", sd)
	return sd, nil
}
func (m *sdInMemoryRepo) Delete(_ context.Context, id string) error {
	if _, gone := m.deleted[id]; gone {
		return structuredefinition.ErrGone
	}
	sd, ok := m.store[id]
	if !ok {
		return structuredefinition.ErrNotFound
	}
	m.deleted[id] = struct{}{}
	if sd.URL != "" {
		m.deletedURL[sd.URL] = struct{}{}
		delete(m.byURL, sd.URL)
	}
	m.appendHistory(id, "delete", sd)
	delete(m.store, id)
	return nil
}
func (m *sdInMemoryRepo) Search(_ context.Context, p structuredefinition.SearchParams) (*structuredefinition.SearchResult, error) {
	var out []structuredefinition.StructureDefinition
	for _, sd := range m.store {
		if p.URL != "" && sd.URL != p.URL {
			continue
		}
		out = append(out, *sd)
	}
	return &structuredefinition.SearchResult{StructureDefinitions: out, Total: len(out)}, nil
}
func (m *sdInMemoryRepo) FindByURL(_ context.Context, url, _ string) (*structuredefinition.StructureDefinition, error) {
	if sd, ok := m.byURL[url]; ok {
		return sd, nil
	}
	if _, gone := m.deletedURL[url]; gone {
		return nil, structuredefinition.ErrGone
	}
	return nil, structuredefinition.ErrNotFound
}
func (m *sdInMemoryRepo) History(_ context.Context, id string) ([]structuredefinition.HistoryEntry, error) {
	entries, ok := m.history[id]
	if !ok || len(entries) == 0 {
		return nil, structuredefinition.ErrNotFound
	}
	return entries, nil
}
func (m *sdInMemoryRepo) ReadVersion(_ context.Context, id string, vid int) (*structuredefinition.StructureDefinition, error) {
	for _, e := range m.history[id] {
		if e.VersionID == vid && e.Resource != nil {
			return e.Resource, nil
		}
	}
	return nil, structuredefinition.ErrNotFound
}

var _ structuredefinition.Repository = (*sdInMemoryRepo)(nil)

func setupSDServer(t *testing.T) (*httptest.Server, *sdInMemoryRepo) {
	t.Helper()
	repo := newSDInMemoryRepo()
	service := structuredefinition.NewService(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	h := NewStructureDefinitionHandler(service, "http://localhost", logger).WithHistory(repo)
	h.RegisterRoutes(mux)
	h.RegisterRoutesAtPrefix(mux, "R5")
	r4 := NewR4StructureDefinitionHandler(service, "http://localhost", logger).WithHistory(repo)
	r4.RegisterRoutes(mux)
	wrapped := Middleware(mux, MaxBodyBytesMiddleware(10<<20))
	return httptest.NewServer(wrapped), repo
}

func sampleSDBody() map[string]any {
	return map[string]any{
		"resourceType":   "StructureDefinition",
		"url":            "http://example.org/StructureDefinition/MyProfile",
		"name":           "MyProfile",
		"status":         "active",
		"kind":           "resource",
		"type":           "Patient",
		"baseDefinition": "http://hl7.org/fhir/StructureDefinition/Patient",
		"derivation":     "constraint",
	}
}

func TestHandler_StructureDefinition_Create(t *testing.T) {
	ts, _ := setupSDServer(t)
	defer ts.Close()

	body, _ := json.Marshal(sampleSDBody())
	resp, err := http.Post(ts.URL+"/fhir/StructureDefinition", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("Location"))
	assert.Contains(t, resp.Header.Get("ETag"), "W/")

	var sd map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sd))
	assert.Equal(t, "StructureDefinition", sd["resourceType"])
	assert.NotEmpty(t, sd["id"])
}

func TestHandler_StructureDefinition_Read(t *testing.T) {
	ts, _ := setupSDServer(t)
	defer ts.Close()

	body, _ := json.Marshal(sampleSDBody())
	createResp, err := http.Post(ts.URL+"/fhir/StructureDefinition", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer createResp.Body.Close()

	var created map[string]any
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id)

	resp, err := http.Get(ts.URL + "/fhir/StructureDefinition/" + id)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var sd map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sd))
	assert.Equal(t, id, sd["id"])
	assert.Equal(t, "MyProfile", sd["name"])
}

func TestHandler_StructureDefinition_Update(t *testing.T) {
	ts, _ := setupSDServer(t)
	defer ts.Close()

	body, _ := json.Marshal(sampleSDBody())
	createResp, err := http.Post(ts.URL+"/fhir/StructureDefinition", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	createResp.Body.Close()

	var created map[string]any
	createResp2, err := http.Post(ts.URL+"/fhir/StructureDefinition", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer createResp2.Body.Close()
	require.NoError(t, json.NewDecoder(createResp2.Body).Decode(&created))
	id, _ := created["id"].(string)

	updated := sampleSDBody()
	updated["title"] = "Updated Title"
	updateBody, _ := json.Marshal(updated)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/StructureDefinition/"+id, bytes.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated)
}

func TestHandler_StructureDefinition_Delete(t *testing.T) {
	ts, _ := setupSDServer(t)
	defer ts.Close()

	body, _ := json.Marshal(sampleSDBody())
	createResp, err := http.Post(ts.URL+"/fhir/StructureDefinition", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))
	createResp.Body.Close()
	id, _ := created["id"].(string)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/StructureDefinition/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Second delete should also return 204 (FHIR idempotent delete).
	req2, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/StructureDefinition/"+id, nil)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp2.StatusCode)
}

func TestHandler_StructureDefinition_SearchByURL(t *testing.T) {
	ts, _ := setupSDServer(t)
	defer ts.Close()

	body, _ := json.Marshal(sampleSDBody())
	resp, err := http.Post(ts.URL+"/fhir/StructureDefinition", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()

	searchResp, err := http.Get(ts.URL + "/fhir/StructureDefinition?url=http%3A%2F%2Fexample.org%2FStructureDefinition%2FMyProfile")
	require.NoError(t, err)
	defer searchResp.Body.Close()

	require.Equal(t, http.StatusOK, searchResp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(searchResp.Body).Decode(&bundle))
	assert.Equal(t, "Bundle", bundle["resourceType"])
	assert.Equal(t, float64(1), bundle["total"])
}

func TestHandler_StructureDefinition_History(t *testing.T) {
	ts, _ := setupSDServer(t)
	defer ts.Close()

	body, _ := json.Marshal(sampleSDBody())
	createResp, err := http.Post(ts.URL+"/fhir/StructureDefinition", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))
	createResp.Body.Close()
	id, _ := created["id"].(string)

	histResp, err := http.Get(ts.URL + "/fhir/StructureDefinition/" + id + "/_history")
	require.NoError(t, err)
	defer histResp.Body.Close()

	require.Equal(t, http.StatusOK, histResp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(histResp.Body).Decode(&bundle))
	assert.Equal(t, "Bundle", bundle["resourceType"])
	assert.Equal(t, float64(1), bundle["total"])
}

func TestHandler_StructureDefinition_Vread(t *testing.T) {
	ts, _ := setupSDServer(t)
	defer ts.Close()

	body, _ := json.Marshal(sampleSDBody())
	createResp, err := http.Post(ts.URL+"/fhir/StructureDefinition", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))
	createResp.Body.Close()
	id, _ := created["id"].(string)
	require.NotEmpty(t, id)

	vreadResp, err := http.Get(ts.URL + "/fhir/StructureDefinition/" + id + "/_history/1")
	require.NoError(t, err)
	defer vreadResp.Body.Close()
	require.Equal(t, http.StatusOK, vreadResp.StatusCode)
	var sd map[string]any
	require.NoError(t, json.NewDecoder(vreadResp.Body).Decode(&sd))
	assert.Equal(t, id, sd["id"])
}

func TestHandler_StructureDefinition_ReadNonExistent_Returns404(t *testing.T) {
	ts, _ := setupSDServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/StructureDefinition/does-not-exist")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_StructureDefinition_R4_Create(t *testing.T) {
	ts, _ := setupSDServer(t)
	defer ts.Close()

	body, _ := json.Marshal(sampleSDBody())
	resp, err := http.Post(ts.URL+"/fhir/R4/StructureDefinition", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var sd map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sd))
	assert.Equal(t, "StructureDefinition", sd["resourceType"])
}

func TestHandler_StructureDefinition_Search_CountOffset(t *testing.T) {
	ts, _ := setupSDServer(t)
	defer ts.Close()

	body, _ := json.Marshal(sampleSDBody())
	resp, err := http.Post(ts.URL+"/fhir/StructureDefinition", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()

	searchResp, err := http.Get(ts.URL + "/fhir/StructureDefinition?_count=10&_offset=0")
	require.NoError(t, err)
	defer searchResp.Body.Close()
	require.Equal(t, http.StatusOK, searchResp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(searchResp.Body).Decode(&bundle))
	assert.Equal(t, "Bundle", bundle["resourceType"])
}
