// Package handler provides HTTP handlers for FHIR operations, including body-size limit tests.
//
// MaxBodyBytesMiddleware enforces a global 10 MiB cap on all endpoints, protecting handlers
// that lack per-endpoint guards.
package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/internal/transform"
	"github.com/goodeworkers/fhir-map/internal/translate"
)

// maxBodyTestLimit must match the production default (10 MiB).
const maxBodyTestLimit = 10 << 20 // 10 MiB

// oversizedBody builds valid JSON one byte over limit to avoid decoder bailout before the limit reader fires.
func oversizedBody(limit int) []byte {
	body := make([]byte, limit+1)
	prefix := []byte(`{"resourceType":"ConceptMap","_pad":"`)
	copy(body, prefix)
	for i := len(prefix); i < len(body)-2; i++ {
		body[i] = 'x'
	}
	body[len(body)-2] = '"'
	body[len(body)-1] = '}'
	return body
}

// assert413OperationOutcome checks that the response is 413 with an
// OperationOutcome body containing code "too-costly".
func assert413OperationOutcome(t *testing.T, resp *http.Response) {
	t.Helper()
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode,
		"body exceeding limit must return 413")
	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues, ok := outcome["issue"].([]any)
	require.True(t, ok, "issue array must be present")
	require.NotEmpty(t, issues)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "too-costly", issue["code"], "413 response must use FHIR code too-costly")
}

func setupConceptMapServerForBodyTests(t *testing.T) *httptest.Server {
	t.Helper()
	repo := newMockRepo()
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	h := NewConceptMapHandler(service, engine, "http://localhost", logger)
	h.RegisterRoutes(mux)
	return httptest.NewServer(Middleware(mux, MaxBodyBytesMiddleware(maxBodyTestLimit)))
}

func TestConceptMapCreate_BodyTooLarge_Returns413(t *testing.T) {
	ts := setupConceptMapServerForBodyTests(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/fhir/ConceptMap", "application/fhir+json",
		bytes.NewReader(oversizedBody(maxBodyTestLimit)))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert413OperationOutcome(t, resp)
}

func TestConceptMapUpdate_BodyTooLarge_Returns413(t *testing.T) {
	ts := setupConceptMapServerForBodyTests(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/ConceptMap/some-id",
		bytes.NewReader(oversizedBody(maxBodyTestLimit)))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert413OperationOutcome(t, resp)
}

func TestConceptMapTranslateBatch_BodyTooLarge_Returns413(t *testing.T) {
	ts := setupConceptMapServerForBodyTests(t)
	defer ts.Close()

	body := make([]byte, maxBodyTestLimit+1)
	prefix := []byte(`{"resourceType":"Parameters","parameter":[{"name":"code","_pad":"`)
	copy(body, prefix)
	for i := len(prefix); i < len(body)-3; i++ {
		body[i] = 'x'
	}
	body[len(body)-3] = '"'
	body[len(body)-2] = '}'
	body[len(body)-1] = ']'

	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate-batch", "application/fhir+json",
		bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert413OperationOutcome(t, resp)
}

func TestConceptMapTranslate_POST_BodyTooLarge_Returns413(t *testing.T) {
	ts := setupConceptMapServerForBodyTests(t)
	defer ts.Close()

	body := make([]byte, maxBodyTestLimit+1)
	prefix := []byte(`{"resourceType":"Parameters","parameter":[{"name":"code","_pad":"`)
	copy(body, prefix)
	for i := len(prefix); i < len(body)-3; i++ {
		body[i] = 'x'
	}
	body[len(body)-3] = '"'
	body[len(body)-2] = '}'
	body[len(body)-1] = ']'

	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json",
		bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert413OperationOutcome(t, resp)
}

func setupStructureMapServerForBodyTests(t *testing.T) *httptest.Server {
	t.Helper()
	repo := newSMInMemoryRepo()
	service := structuremap.NewService(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := transform.NewEngine(nil)
	mux := http.NewServeMux()
	h := NewStructureMapHandler(service, "http://localhost", logger).WithTransformEngine(eng)
	h.RegisterRoutes(mux)
	h.RegisterRoutesAtPrefix(mux, "R5")
	r4 := NewR4StructureMapHandler(service, "http://localhost", logger).WithTransformEngine(eng)
	r4.RegisterRoutes(mux)
	return httptest.NewServer(Middleware(mux, MaxBodyBytesMiddleware(maxBodyTestLimit)))
}

func smOversizedBody(limit int) []byte {
	body := make([]byte, limit+1)
	prefix := []byte(`{"resourceType":"StructureMap","_pad":"`)
	copy(body, prefix)
	for i := len(prefix); i < len(body)-2; i++ {
		body[i] = 'x'
	}
	body[len(body)-2] = '"'
	body[len(body)-1] = '}'
	return body
}

func TestStructureMapCreate_BodyTooLarge_Returns413(t *testing.T) {
	ts := setupStructureMapServerForBodyTests(t)
	defer ts.Close()

	for _, path := range []string{"/fhir/StructureMap", "/fhir/R5/StructureMap", "/fhir/R4/StructureMap"} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Post(ts.URL+path, "application/fhir+json",
				bytes.NewReader(smOversizedBody(maxBodyTestLimit)))
			require.NoError(t, err)
			defer resp.Body.Close()
			assert413OperationOutcome(t, resp)
		})
	}
}

// TestIsBodyTooLarge_UnwrapsDecoderError pins the contract that callers downstream of json.NewDecoder
// can still classify size failures even when *http.MaxBytesError is wrapped by json.Decode.
func TestIsBodyTooLarge_UnwrapsDecoderError(t *testing.T) {
	const limit = 1 << 10
	var caughtTooLarge bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /probe", func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			wrapped := fmt.Errorf("invalid Parameters resource: %w", err)
			caughtTooLarge = IsBodyTooLarge(wrapped)
			if caughtTooLarge {
				WriteBodyTooLargeResponse(w)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(Middleware(mux, MaxBodyBytesMiddleware(limit)))
	defer ts.Close()

	body := make([]byte, limit+1)
	copy(body, []byte(`{"x":"`))
	for i := 6; i < len(body)-2; i++ {
		body[i] = 'a'
	}
	body[len(body)-2] = '"'
	body[len(body)-1] = '}'

	resp, err := http.Post(ts.URL+"/probe", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.True(t, caughtTooLarge,
		"IsBodyTooLarge must unwrap *http.MaxBytesError through %%w-wrapped json.Decode errors")
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}

func TestStructureMapUpdate_BodyTooLarge_Returns413(t *testing.T) {
	ts := setupStructureMapServerForBodyTests(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/StructureMap/some-id",
		bytes.NewReader(smOversizedBody(maxBodyTestLimit)))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert413OperationOutcome(t, resp)
}
