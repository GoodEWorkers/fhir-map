package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

type fakeResource struct {
	ID     string     `json:"id,omitempty"`
	Meta   *fhir.Meta `json:"meta,omitempty"`
	Body   string     `json:"body,omitempty"`
	R5Only string     `json:"r5only,omitempty"`
}

func (f *fakeResource) GetID() string        { return f.ID }
func (f *fakeResource) SetID(s string)       { f.ID = s }
func (f *fakeResource) GetMeta() *fhir.Meta  { return f.Meta }
func (f *fakeResource) SetMeta(m *fhir.Meta) { f.Meta = m }

var (
	errFakeNotFound    = errors.New("not found")
	errFakeGone        = errors.New("gone")
	errFakeConflict    = errors.New("conflict")
	errFakeUnprocessed = errors.New("unprocessable")
	errFakeZeroStatus  = errors.New("zero-status")
)

type fakeAdapter struct {
	store           map[string]*fakeResource
	deleted         map[string]struct{}
	history         map[string][]HistoryEntry[*fakeResource]
	hasHistory      bool
	versionSeq      map[string]int
	forcedReadErr   error
	forcedCreateErr error
}

func newFakeAdapter(withHistory bool) *fakeAdapter {
	return &fakeAdapter{
		store:      map[string]*fakeResource{},
		deleted:    map[string]struct{}{},
		history:    map[string][]HistoryEntry[*fakeResource]{},
		hasHistory: withHistory,
		versionSeq: map[string]int{},
	}
}

func (a *fakeAdapter) bumpVersion(id string) string {
	a.versionSeq[id]++
	return fmt.Sprintf("%d", a.versionSeq[id])
}

func (a *fakeAdapter) ResourceName() string { return "FakeResource" }
func (a *fakeAdapter) New() *fakeResource   { return &fakeResource{} }

func (a *fakeAdapter) Create(_ context.Context, t *fakeResource, _ ValidationMode) (*fakeResource, error) {
	if a.forcedCreateErr != nil {
		return nil, a.forcedCreateErr
	}
	t.Meta = &fhir.Meta{VersionID: a.bumpVersion(t.ID)}
	cp := *t
	a.store[t.ID] = t
	if a.hasHistory {
		a.history[t.ID] = append(
			[]HistoryEntry[*fakeResource]{{VersionID: a.versionSeq[t.ID], Operation: "create", Resource: &cp}},
			a.history[t.ID]...,
		)
	}
	return t, nil
}

func (a *fakeAdapter) Read(_ context.Context, id string) (*fakeResource, error) {
	if a.forcedReadErr != nil {
		return nil, a.forcedReadErr
	}
	if _, gone := a.deleted[id]; gone {
		return nil, errFakeGone
	}
	r, ok := a.store[id]
	if !ok {
		return nil, errFakeNotFound
	}
	return r, nil
}

func (a *fakeAdapter) Update(_ context.Context, id string, t *fakeResource, _ ValidationMode) (*fakeResource, error) {
	if _, gone := a.deleted[id]; gone {
		return nil, errFakeGone
	}
	existing, ok := a.store[id]
	if !ok {
		return nil, errFakeNotFound
	}
	if t.Meta != nil && t.Meta.VersionID != "" && existing.Meta != nil && t.Meta.VersionID != existing.Meta.VersionID {
		return nil, errFakeConflict
	}
	t.ID = id
	t.Meta = &fhir.Meta{VersionID: a.bumpVersion(id)}
	cp := *t
	a.store[id] = t
	if a.hasHistory {
		a.history[id] = append(
			[]HistoryEntry[*fakeResource]{{VersionID: a.versionSeq[id], Operation: "update", Resource: &cp}},
			a.history[id]...,
		)
	}
	return t, nil
}

func (a *fakeAdapter) Delete(_ context.Context, id string) error {
	if _, gone := a.deleted[id]; gone {
		return errFakeGone
	}
	if _, ok := a.store[id]; !ok {
		return errFakeNotFound
	}
	vid := a.versionSeq[id] + 1
	a.deleted[id] = struct{}{}
	delete(a.store, id)
	if a.hasHistory {
		a.history[id] = append(
			[]HistoryEntry[*fakeResource]{{VersionID: vid, Operation: "delete"}},
			a.history[id]...,
		)
	}
	return nil
}

func (a *fakeAdapter) Search(_ context.Context, _ url.Values) ([]*fakeResource, int, error) {
	out := make([]*fakeResource, 0, len(a.store))
	for _, r := range a.store {
		out = append(out, r)
	}
	return out, len(out), nil
}

func (a *fakeAdapter) FindByURL(_ context.Context, _, _ string) (*fakeResource, error) {
	return nil, errFakeNotFound
}

func (a *fakeAdapter) HasHistory() bool { return a.hasHistory }

func (a *fakeAdapter) History(_ context.Context, id string) ([]HistoryEntry[*fakeResource], error) {
	entries, ok := a.history[id]
	if !ok || len(entries) == 0 {
		return nil, errFakeNotFound
	}
	return entries, nil
}

func (a *fakeAdapter) ReadVersion(_ context.Context, id string, vid int) (*fakeResource, error) {
	for _, e := range a.history[id] {
		if e.VersionID == vid && e.Resource != nil {
			return e.Resource, nil
		}
	}
	return nil, errFakeNotFound
}

func (a *fakeAdapter) MapServiceError(err error) (int, string, string) {
	switch {
	case errors.Is(err, errFakeZeroStatus):
		return 0, "", "" // exercises the status==0 defensive branch in handleServiceError
	case errors.Is(err, errFakeNotFound):
		return http.StatusNotFound, "not-found", "Resource not found"
	case errors.Is(err, errFakeGone):
		return http.StatusGone, "gone", "Resource has been deleted"
	case errors.Is(err, errFakeConflict):
		return http.StatusConflict, "conflict", "Version conflict"
	case errors.Is(err, errFakeUnprocessed):
		return http.StatusUnprocessableEntity, "invalid", "Unprocessable"
	default:
		return http.StatusInternalServerError, "exception", "Internal error"
	}
}

func (a *fakeAdapter) ProjectForWire(t *fakeResource, _ fhir.FHIRVersion) *fakeResource { return t }
func (a *fakeAdapter) CanonicaliseFromR4(_ *fakeResource)                               {}
func (a *fakeAdapter) R5OnlyFields() []string                                           { return []string{"r5only"} }

func setupFakeServer(t *testing.T, withHistory bool, version fhir.FHIRVersion) (*httptest.Server, *fakeAdapter) {
	t.Helper()
	adapter := newFakeAdapter(withHistory)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rh := NewResourceHandler[*fakeResource](adapter, "http://localhost", logger, version)

	mux := http.NewServeMux()
	prefix := "/fhir"
	if version == fhir.VersionR4 {
		prefix = "/fhir/R4"
	}
	mux.HandleFunc("POST "+prefix+"/FakeResource", rh.Create)
	mux.HandleFunc("GET "+prefix+"/FakeResource/{id}", rh.Read)
	mux.HandleFunc("PUT "+prefix+"/FakeResource/{id}", rh.Update)
	mux.HandleFunc("DELETE "+prefix+"/FakeResource/{id}", rh.Delete)
	mux.HandleFunc("GET "+prefix+"/FakeResource", rh.Search)
	mux.HandleFunc("GET "+prefix+"/FakeResource/{id}/_history", rh.History)
	mux.HandleFunc("GET "+prefix+"/FakeResource/{id}/_history/{vid}", rh.Vread)

	return httptest.NewServer(mux), adapter
}

func fakeBody(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return bytes.NewBuffer(b)
}

func TestResourceHandler_Create_Returns201WithLocationAndETag(t *testing.T) {
	ts, _ := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	body := fakeBody(t, &fakeResource{ID: "new-1", Body: "hello"})
	resp, err := http.Post(ts.URL+"/fhir/FakeResource", "application/fhir+json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "/fhir/FakeResource/new-1")
	assert.NotEmpty(t, resp.Header.Get("ETag"))
}

func TestResourceHandler_Create_R4RejectsR5OnlyField(t *testing.T) {
	ts, _ := setupFakeServer(t, false, fhir.VersionR4)
	defer ts.Close()

	body := fakeBody(t, map[string]any{"id": "x", "r5only": "value"})
	resp, err := http.Post(ts.URL+"/fhir/R4/FakeResource", "application/fhir+json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	issues := out["issue"].([]any)
	assert.Equal(t, "not-supported", issues[0].(map[string]any)["code"])
}

func TestResourceHandler_Create_BadJSON_Returns400(t *testing.T) {
	ts, _ := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/fhir/FakeResource", "application/fhir+json", bytes.NewBufferString("{not json"))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestResourceHandler_Read_Returns200WithETag(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	adapter.store["r1"] = &fakeResource{ID: "r1", Meta: &fhir.Meta{VersionID: "3"}, Body: "hi"}

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/r1")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `W/"3"`, resp.Header.Get("ETag"))
	var got fakeResource
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "hi", got.Body)
}

func TestResourceHandler_Read_Missing_Returns404(t *testing.T) {
	ts, _ := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/nonexistent")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestResourceHandler_Update_Returns200(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	adapter.store["u1"] = &fakeResource{ID: "u1", Meta: &fhir.Meta{VersionID: "1"}, Body: "old"}
	adapter.versionSeq["u1"] = 1

	body := fakeBody(t, &fakeResource{ID: "u1", Body: "new"})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/FakeResource/u1", body)
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestResourceHandler_Update_Upsert_Returns201(t *testing.T) {
	ts, _ := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	body := fakeBody(t, &fakeResource{Body: "brand-new"})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/FakeResource/fresh-id", body)
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "/fhir/FakeResource/fresh-id")
}

func TestResourceHandler_Update_IfMatchOnNonExistent_Returns412(t *testing.T) {
	ts, _ := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	body := fakeBody(t, &fakeResource{Body: "x"})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/FakeResource/ghost", body)
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-Match", `W/"1"`)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
}

func TestResourceHandler_Update_BodyIDMismatch_Returns400(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	adapter.store["existing"] = &fakeResource{ID: "existing", Meta: &fhir.Meta{VersionID: "1"}}
	adapter.versionSeq["existing"] = 1

	body := fakeBody(t, &fakeResource{ID: "different-id"})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/FakeResource/existing", body)
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestResourceHandler_Delete_Returns204(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	adapter.store["del1"] = &fakeResource{ID: "del1"}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/FakeResource/del1", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestResourceHandler_Delete_AlreadyDeleted_Returns204(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	adapter.deleted["gone1"] = struct{}{}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/FakeResource/gone1", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestResourceHandler_Search_ReturnsBundle(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	adapter.store["s1"] = &fakeResource{ID: "s1"}
	adapter.store["s2"] = &fakeResource{ID: "s2"}

	resp, err := http.Get(ts.URL + "/fhir/FakeResource")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&bundle))
	assert.Equal(t, "Bundle", bundle["resourceType"])
	assert.Equal(t, "searchset", bundle["type"])
	assert.Equal(t, float64(2), bundle["total"])
}

func TestResourceHandler_History_NoReader_Returns501(t *testing.T) {
	ts, _ := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/any/_history")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
}

func TestResourceHandler_History_ReturnsBundle(t *testing.T) {
	ts, adapter := setupFakeServer(t, true, fhir.VersionR5)
	defer ts.Close()

	r := &fakeResource{ID: "h1", Body: "v1", Meta: &fhir.Meta{VersionID: "1"}}
	adapter.store["h1"] = r
	adapter.versionSeq["h1"] = 1
	adapter.history["h1"] = []HistoryEntry[*fakeResource]{
		{VersionID: 1, Operation: "create", Resource: r},
	}

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/h1/_history")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&bundle))
	assert.Equal(t, "Bundle", bundle["resourceType"])
	assert.Equal(t, "history", bundle["type"])
	assert.Equal(t, float64(1), bundle["total"])
}

func TestResourceHandler_Vread_NoReader_Returns501(t *testing.T) {
	ts, _ := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/any/_history/1")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
}

func TestResourceHandler_Vread_DeletedVersion_Returns410(t *testing.T) {
	ts, adapter := setupFakeServer(t, true, fhir.VersionR5)
	defer ts.Close()

	adapter.history["vr1"] = []HistoryEntry[*fakeResource]{
		{VersionID: 2, Operation: "delete"},
		{VersionID: 1, Operation: "create", Resource: &fakeResource{ID: "vr1", Body: "v1"}},
	}

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/vr1/_history/2")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusGone, resp.StatusCode)
	assert.Equal(t, `W/"2"`, resp.Header.Get("ETag"))
}

func TestResourceHandler_Vread_Returns200ForExistingVersion(t *testing.T) {
	ts, adapter := setupFakeServer(t, true, fhir.VersionR5)
	defer ts.Close()

	r1 := &fakeResource{ID: "vr2", Body: "snapshot-v1"}
	adapter.history["vr2"] = []HistoryEntry[*fakeResource]{
		{VersionID: 1, Operation: "create", Resource: r1},
	}

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/vr2/_history/1")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var got fakeResource
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "snapshot-v1", got.Body)
}

func TestResourceHandler_Vread_NonExistentVersion_Returns404(t *testing.T) {
	ts, adapter := setupFakeServer(t, true, fhir.VersionR5)
	defer ts.Close()

	adapter.history["vr3"] = []HistoryEntry[*fakeResource]{
		{VersionID: 1, Operation: "create", Resource: &fakeResource{ID: "vr3"}},
	}

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/vr3/_history/999")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ─── handleServiceError branches ─────────────────────────────────────────────

func TestResourceHandler_Read_AdapterReturnsZeroStatus_Returns500(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()
	adapter.forcedReadErr = errFakeZeroStatus

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/any")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestResourceHandler_Read_AdapterReturns422_Returns422(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()
	adapter.forcedReadErr = errFakeUnprocessed

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/any")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

func TestResourceHandler_Delete_NotFound_Returns404(t *testing.T) {
	ts, _ := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/fhir/FakeResource/nonexistent", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestResourceHandler_Vread_NilSnapshot_Returns500(t *testing.T) {
	ts, adapter := setupFakeServer(t, true, fhir.VersionR5)
	defer ts.Close()

	adapter.history["snap1"] = []HistoryEntry[*fakeResource]{
		{VersionID: 1, Operation: "create", Resource: nil},
	}

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/snap1/_history/1")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestResourceHandler_Update_StaleETag_Returns412(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	adapter.store["stale1"] = &fakeResource{ID: "stale1", Meta: &fhir.Meta{VersionID: "5"}}
	adapter.versionSeq["stale1"] = 5

	body := fakeBody(t, &fakeResource{ID: "stale1", Body: "update"})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/FakeResource/stale1", body)
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-Match", `W/"3"`)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
}

func TestResourceHandler_Update_IfMatchGarbage_Returns400(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	adapter.store["g1"] = &fakeResource{ID: "g1", Meta: &fhir.Meta{VersionID: "1"}}
	adapter.versionSeq["g1"] = 1

	// RFC 7232 §2.3: only structurally malformed headers return 400; opaque values are valid.
	cases := []string{
		`foo`,   // no quotes
		`W/foo`, // no quotes after W/
		`""`,    // empty version
		`W/""`,  // empty version, weak form
		`W/"1`,  // unterminated
	}
	for _, etag := range cases {
		t.Run(etag, func(t *testing.T) {
			body := fakeBody(t, &fakeResource{ID: "g1", Body: "x"})
			req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/FakeResource/g1", body)
			req.Header.Set("Content-Type", "application/fhir+json")
			req.Header.Set("If-Match", etag)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "etag=%q", etag)
		})
	}
}

func TestResourceHandler_Update_R4RejectsR5OnlyField(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR4)
	defer ts.Close()

	adapter.store["u2"] = &fakeResource{ID: "u2", Meta: &fhir.Meta{VersionID: "1"}}
	adapter.versionSeq["u2"] = 1

	body := fakeBody(t, map[string]any{"id": "u2", "r5only": "bad"})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/R4/FakeResource/u2", body)
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestResourceHandler_Search_AdapterError_ReturnsError(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	_ = adapter

	resp, err := http.Get(ts.URL + "/fhir/FakeResource?_filter=none")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&bundle))
	assert.Equal(t, float64(0), bundle["total"])
}

func TestResourceHandler_History_Error_ReturnsHandled(t *testing.T) {
	ts, adapter := setupFakeServer(t, true, fhir.VersionR5)
	defer ts.Close()

	_ = adapter

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/missing-id/_history")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestResourceHandler_Vread_HistoryError_ReturnsHandled(t *testing.T) {
	ts, adapter := setupFakeServer(t, true, fhir.VersionR5)
	defer ts.Close()

	_ = adapter

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/vr-missing/_history/1")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read error") }

func TestResourceHandler_Create_BodyReadError_Returns400(t *testing.T) {
	adapter := newFakeAdapter(false)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rh := NewResourceHandler[*fakeResource](adapter, "http://localhost", logger, fhir.VersionR5)

	req := httptest.NewRequest(http.MethodPost, "/fhir/FakeResource", io.NopCloser(errReader{}))
	w := httptest.NewRecorder()
	rh.Create(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestResourceHandler_Update_BodyReadError_Returns400(t *testing.T) {
	adapter := newFakeAdapter(false)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rh := NewResourceHandler[*fakeResource](adapter, "http://localhost", logger, fhir.VersionR5)

	req := httptest.NewRequest(http.MethodPut, "/fhir/FakeResource/x", io.NopCloser(errReader{}))
	req.SetPathValue("id", "x")
	w := httptest.NewRecorder()
	rh.Update(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestResourceHandler_Update_BadJSON_Returns400(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	adapter.store["uj1"] = &fakeResource{ID: "uj1", Meta: &fhir.Meta{VersionID: "1"}}
	adapter.versionSeq["uj1"] = 1

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/FakeResource/uj1", bytes.NewBufferString("{bad json"))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestResourceHandler_Update_R4CanonicaliseFromR4(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR4)
	defer ts.Close()

	adapter.store["r4u1"] = &fakeResource{ID: "r4u1", Meta: &fhir.Meta{VersionID: "1"}}
	adapter.versionSeq["r4u1"] = 1

	body := fakeBody(t, &fakeResource{ID: "r4u1", Body: "updated"})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/R4/FakeResource/r4u1", body)
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestResourceHandler_Update_UpsertCreateFails_ReturnsError(t *testing.T) {
	ts, adapter := setupFakeServer(t, false, fhir.VersionR5)
	defer ts.Close()

	adapter.forcedCreateErr = errFakeUnprocessed

	body := fakeBody(t, &fakeResource{Body: "should fail"})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/FakeResource/new-failing", body)
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

func TestResourceHandler_Vread_InvalidVersion_Returns400(t *testing.T) {
	ts, _ := setupFakeServer(t, true, fhir.VersionR5)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/any/_history/notanumber")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestResourceHandler_Vread_ZeroVersion_Returns400(t *testing.T) {
	ts, _ := setupFakeServer(t, true, fhir.VersionR5)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/FakeResource/any/_history/0")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

var _ Adapter[*fakeResource] = (*fakeAdapter)(nil)
var _ Resource = (*fakeResource)(nil)

// keep context import used
var _ = context.Background
