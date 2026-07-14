package handler

import (
	"bytes"
	"encoding/json"
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

// setupVersionTreeServer mounts /fhir/R4 and /fhir/R5 ConceptMap trees plus the unprefixed /fhir tree (R5 alias).
func setupVersionTreeServer(t *testing.T) (*httptest.Server, *mockRepo) {
	t.Helper()
	repo := newMockRepo()
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mux := http.NewServeMux()
	r5 := NewConceptMapHandler(service, engine, "http://localhost", logger)
	r5.RegisterRoutes(mux)               // legacy unprefixed alias to R5
	r5.RegisterRoutesAtPrefix(mux, "R5") // explicit /fhir/R5/...
	r4 := NewR4ConceptMapHandler(service, engine, "http://localhost", logger)
	r4.RegisterRoutes(mux) // /fhir/R4/...

	ts := httptest.NewServer(mux)
	return ts, repo
}

func TestHandler_R4Tree_AcceptsEquivalence(t *testing.T) {
	ts, _ := setupVersionTreeServer(t)
	defer ts.Close()

	r4Body := map[string]any{
		"resourceType": "ConceptMap",
		"url":          "http://example.org/cm/r4-roundtrip",
		"status":       "active",
		"group": []map[string]any{
			{
				"source": "http://src",
				"target": "http://tgt",
				"element": []map[string]any{
					{
						"code": "A",
						"target": []map[string]any{
							{"code": "B", "equivalence": "wider"}, // R4 vocab
						},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(r4Body)
	resp, err := http.Post(ts.URL+"/fhir/R4/ConceptMap", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusCreated, resp.StatusCode, "R4 POST should accept equivalence vocabulary")

	var created map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id)

	resp2, err := http.Get(ts.URL + "/fhir/R5/ConceptMap/" + id)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var r5 map[string]any
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&r5))
	groups := r5["group"].([]any)
	target := groups[0].(map[string]any)["element"].([]any)[0].(map[string]any)["target"].([]any)[0].(map[string]any)
	if rel, ok := target["relationship"]; !ok || rel != "source-is-broader-than-target" {
		t.Fatalf("R5 view must surface relationship=source-is-broader-than-target; got %#v", target)
	}
}

func TestHandler_TranslateOutput_R4EmitsEquivalence_R5EmitsRelationship(t *testing.T) {
	ts, repo := setupVersionTreeServer(t)
	defer ts.Close()
	seedAddressUseMap(repo) // home->H equivalent

	r5Resp, err := http.Get(ts.URL +
		"/fhir/R5/ConceptMap/$translate" +
		"?url=http://example.org/fhir/ConceptMap/address-use" +
		"&sourceCode=home&sourceSystem=http://hl7.org/fhir/address-use")
	require.NoError(t, err)
	defer r5Resp.Body.Close()
	require.Equal(t, http.StatusOK, r5Resp.StatusCode)
	r5Body := decodeParameters(t, r5Resp.Body)
	assertHasMatchPart(t, r5Body, "relationship")

	r4Resp, err := http.Get(ts.URL +
		"/fhir/R4/ConceptMap/$translate" +
		"?url=http://example.org/fhir/ConceptMap/address-use" +
		"&code=home&system=http://hl7.org/fhir/address-use")
	require.NoError(t, err)
	defer r4Resp.Body.Close()
	require.Equal(t, http.StatusOK, r4Resp.StatusCode)
	r4Body := decodeParameters(t, r4Resp.Body)
	assertHasMatchPart(t, r4Body, "equivalence")
}

func TestHandler_R4_R5_CrudParity(t *testing.T) {
	for _, prefix := range []string{"/fhir/R4", "/fhir/R5"} {
		t.Run(prefix, func(t *testing.T) {
			ts, _ := setupVersionTreeServer(t)
			defer ts.Close()

			cm := conceptmap.ConceptMap{
				ResourceType: "ConceptMap",
				URL:          "http://example.org/cm/parity",
				Status:       "draft",
				Group: []conceptmap.Group{
					{Source: "http://s", Target: "http://t", Element: []conceptmap.Element{
						{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}},
					}},
				},
			}
			raw, _ := json.Marshal(cm)

			created, err := http.Post(ts.URL+prefix+"/ConceptMap", "application/fhir+json", bytes.NewReader(raw))
			require.NoError(t, err)
			defer created.Body.Close()
			require.Equal(t, http.StatusCreated, created.StatusCode)

			var got conceptmap.ConceptMap
			require.NoError(t, json.NewDecoder(created.Body).Decode(&got))
			id := got.ID
			require.NotEmpty(t, id)

			read, err := http.Get(ts.URL + prefix + "/ConceptMap/" + id)
			require.NoError(t, err)
			defer read.Body.Close()
			require.Equal(t, http.StatusOK, read.StatusCode)

			search, err := http.Get(ts.URL + prefix + "/ConceptMap?status=draft")
			require.NoError(t, err)
			defer search.Body.Close()
			require.Equal(t, http.StatusOK, search.StatusCode)
			var bundle map[string]any
			require.NoError(t, json.NewDecoder(search.Body).Decode(&bundle))
			assert.Equal(t, "Bundle", bundle["resourceType"])
		})
	}
}

// decodeParameters decodes a Parameters JSON resource.
func decodeParameters(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.NewDecoder(body).Decode(&out))
	require.Equal(t, "Parameters", out["resourceType"])
	return out
}

// assertHasMatchPart fails if no match parameter contains a part with the given name.
func assertHasMatchPart(t *testing.T, body map[string]any, partName string) {
	t.Helper()
	params, _ := body["parameter"].([]any)
	for _, p := range params {
		param, _ := p.(map[string]any)
		if param["name"] != "match" {
			continue
		}
		parts, _ := param["part"].([]any)
		for _, sp := range parts {
			pp, _ := sp.(map[string]any)
			if pp["name"] == partName {
				return
			}
		}
	}
	t.Fatalf("no match parameter contained a part named %q; body=%v", partName, body)
}
