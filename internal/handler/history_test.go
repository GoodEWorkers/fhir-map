package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/translate"
)

type stubHistoryReader struct {
	byID map[string][]conceptmap.HistoryEntry
}

func (s *stubHistoryReader) History(_ context.Context, id string) ([]conceptmap.HistoryEntry, error) {
	entries, ok := s.byID[id]
	if !ok || len(entries) == 0 {
		return nil, conceptmap.ErrNotFound
	}
	return entries, nil
}

func (s *stubHistoryReader) ReadVersion(_ context.Context, id string, vid int) (*conceptmap.ConceptMap, error) {
	for _, e := range s.byID[id] {
		if e.VersionID == vid {
			return e.Resource, nil
		}
	}
	return nil, conceptmap.ErrNotFound
}

// _history returns a history Bundle with the right entry count.
func TestHandler_M4_2_History_ReturnsBundle(t *testing.T) {
	stub := &stubHistoryReader{byID: map[string][]conceptmap.HistoryEntry{
		"foo": {
			{VersionID: 3, Operation: "delete", OccurredAt: "2026-05-16T18:00:02Z"},
			{VersionID: 2, Operation: "update", OccurredAt: "2026-05-16T18:00:01Z",
				Resource: &conceptmap.ConceptMap{ResourceType: "ConceptMap", ID: "foo", Status: "active"}},
			{VersionID: 1, Operation: "create", OccurredAt: "2026-05-16T18:00:00Z",
				Resource: &conceptmap.ConceptMap{ResourceType: "ConceptMap", ID: "foo", Status: "draft"}},
		},
	}}
	ts := startServer(t, stub)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/R5/ConceptMap/foo/_history")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&bundle))
	assert.Equal(t, "Bundle", bundle["resourceType"])
	assert.Equal(t, "history", bundle["type"])
	assert.Equal(t, float64(3), bundle["total"])

	entries, _ := bundle["entry"].([]any)
	require.Len(t, entries, 3)
	first := entries[0].(map[string]any)
	req, _ := first["request"].(map[string]any)
	assert.Equal(t, "DELETE", req["method"])
	resourceField, hasResource := first["resource"]
	assert.False(t, hasResource && resourceField != nil,
		"delete entries should not carry a resource body in the response")
}

// Fetching /_history/{vid} returns the snapshot for that version.
func TestHandler_M4_2_Vread_ReturnsVersionSnapshot(t *testing.T) {
	stub := &stubHistoryReader{byID: map[string][]conceptmap.HistoryEntry{
		"bar": {
			{VersionID: 2, Operation: "update",
				Resource: &conceptmap.ConceptMap{ResourceType: "ConceptMap", ID: "bar", Status: "active", Title: "v2"}},
			{VersionID: 1, Operation: "create",
				Resource: &conceptmap.ConceptMap{ResourceType: "ConceptMap", ID: "bar", Status: "draft", Title: "v1"}},
		},
	}}
	ts := startServer(t, stub)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/R5/ConceptMap/bar/_history/1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var cm conceptmap.ConceptMap
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cm))
	assert.Equal(t, "v1", cm.Title, "vread must return the v1 snapshot, not the latest")
	assert.Equal(t, "draft", cm.Status)
	assert.Equal(t, `W/"1"`, resp.Header.Get("ETag"))
}

// Returns 404 OperationOutcome on non-existent version.
func TestHandler_M4_2_Vread_NotFound(t *testing.T) {
	stub := &stubHistoryReader{byID: map[string][]conceptmap.HistoryEntry{
		"bar": {
			{VersionID: 1, Operation: "create",
				Resource: &conceptmap.ConceptMap{ResourceType: "ConceptMap", ID: "bar", Status: "draft"}},
		},
	}}
	ts := startServer(t, stub)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/R5/ConceptMap/bar/_history/999")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// _history returns 501 when no HistoryReader is configured.
func TestHandler_M4_2_History_501WhenNoReader(t *testing.T) {
	repo := newMockRepo()
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mux := http.NewServeMux()
	h := NewConceptMapHandler(service, engine, "http://localhost", logger) // no WithHistory
	h.RegisterRoutesAtPrefix(mux, "R5")
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/R5/ConceptMap/foo/_history")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
}

// startServer wires a server with a HistoryReader on both R4 and R5 trees.
func startServer(t *testing.T, hr HistoryReader) *httptest.Server {
	t.Helper()
	repo := newMockRepo()
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mux := http.NewServeMux()
	r5 := NewConceptMapHandler(service, engine, "http://localhost", logger).WithHistory(hr)
	r5.RegisterRoutesAtPrefix(mux, "R5")
	r5.RegisterRoutes(mux) // legacy unprefixed alias
	r4 := NewR4ConceptMapHandler(service, engine, "http://localhost", logger).WithHistory(hr)
	r4.RegisterRoutes(mux)
	return httptest.NewServer(mux)
}

// Compile-time guards.
var _ HistoryReader = (*stubHistoryReader)(nil)
var _ = errors.New
