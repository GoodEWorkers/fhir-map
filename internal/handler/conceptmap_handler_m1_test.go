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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/translate"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// seedAddressUseMap seeds the address-use ConceptMap fixture.
func seedAddressUseMap(repo *mockRepo) *conceptmap.ConceptMap {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "address-use",
		URL:          "http://example.org/fhir/ConceptMap/address-use",
		Status:       "active",
		Group: []conceptmap.Group{
			{
				Source: "http://hl7.org/fhir/address-use",
				Target: "http://terminology.hl7.org/CodeSystem/v3-AddressUse",
				Element: []conceptmap.Element{
					{Code: "home", Target: []conceptmap.Target{{Code: "H", Relationship: "equivalent"}}},
					{Code: "work", Target: []conceptmap.Target{{Code: "WP", Relationship: "equivalent"}}},
					{Code: "temp", Target: []conceptmap.Target{{Code: "TMP", Relationship: "equivalent"}}},
				},
			},
		},
	}
	repo.store[cm.ID] = cm
	repo.byURL[cm.URL] = cm
	return cm
}

func TestHandler_TranslatePOST_RejectsConflictingR4AndR5(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()
	seedAddressUseMap(repo)

	body := map[string]any{
		"resourceType": "Parameters",
		"parameter": []map[string]any{
			{"name": "url", "valueUri": "http://example.org/fhir/ConceptMap/address-use"},
			{"name": "code", "valueCode": "home"},
			{"name": "sourceCode", "valueCode": "home"},
		},
	}
	raw, _ := json.Marshal(body)

	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])

	issues, _ := outcome["issue"].([]any)
	require.NotEmpty(t, issues)
	first, _ := issues[0].(map[string]any)
	assert.Equal(t, "invalid", first["code"])
	diag, _ := first["diagnostics"].(string)
	assert.True(t, strings.Contains(diag, "code") && strings.Contains(diag, "sourceCode"),
		"diagnostics must name both conflicting parameters; got %q", diag)
}

// Same rule on GET: ?code=...&sourceCode=... must 400.
func TestHandler_TranslateGET_RejectsConflictingR4AndR5(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()
	seedAddressUseMap(repo)

	resp, err := http.Get(ts.URL +
		"/fhir/ConceptMap/$translate" +
		"?url=http://example.org/fhir/ConceptMap/address-use" +
		"&code=home&sourceCode=home")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

type recordingEngine struct {
	*translate.Engine
	last translate.Request
}

func (r *recordingEngine) Translate(ctx context.Context, req translate.Request) (*translate.Response, error) {
	r.last = req
	return &translate.Response{Result: true}, nil
}

func TestHandler_TranslatePOST_ParsesDependencyInput(t *testing.T) {
	repo := newMockRepo()
	seedAddressUseMap(repo)
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	rec := &recordingEngine{Engine: engine}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mux := http.NewServeMux()
	h := NewConceptMapHandler(service, rec, "http://localhost", logger)
	h.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := map[string]any{
		"resourceType": "Parameters",
		"parameter": []map[string]any{
			{"name": "url", "valueUri": "http://example.org/fhir/ConceptMap/address-use"},
			{"name": "sourceCode", "valueCode": "home"},
			{
				"name": "dependency",
				"part": []map[string]any{
					{"name": "attribute", "valueUri": "sex"},
					{"name": "value", "valueCode": "male"},
				},
			},
		},
	}
	raw, _ := json.Marshal(body)

	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Len(t, rec.last.Dependencies, 1, "handler must forward the dependency input to the engine")
	dep := rec.last.Dependencies[0]
	assert.Equal(t, "sex", dep.Attribute)
	assert.Equal(t, "male", dep.ValueCode)
}

// M1.7 — concurrent PUTs with the same If-Match must reject the loser with 409.
// We simulate by directly invoking the service against a repo that rejects
// stale versions; the handler must surface ErrConflict as 409.
type concurrencyMockRepo struct {
	*mockRepo
	currentVersionID int
}

func (c *concurrencyMockRepo) Update(_ context.Context, id string, cm *conceptmap.ConceptMap) (*conceptmap.ConceptMap, error) {
	existing, ok := c.store[id]
	if !ok {
		return nil, conceptmap.ErrNotFound
	}
	// Compare cm.Meta.VersionID (carried from If-Match by the handler) with the stored value.
	expected := ""
	if cm.Meta != nil {
		expected = cm.Meta.VersionID
	}
	currentVID := ""
	if existing.Meta != nil {
		currentVID = existing.Meta.VersionID
	}
	if expected != "" && expected != currentVID {
		return nil, conceptmap.ErrConflict
	}
	cm.ID = id
	c.store[id] = cm
	return cm, nil
}

func TestHandler_Update_IfMatchStaleReturns412(t *testing.T) {
	base := newMockRepo()
	base.store["concurrency-id"] = &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "concurrency-id",
		Status:       "active",
		Meta:         &fhir.Meta{VersionID: "2"},
	}
	repo := &concurrencyMockRepo{mockRepo: base, currentVersionID: 2}
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mux := http.NewServeMux()
	h := NewConceptMapHandler(service, engine, "http://localhost", logger)
	h.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	updated := conceptmap.ConceptMap{Status: "retired"}
	body, _ := json.Marshal(updated)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/ConceptMap/concurrency-id", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("If-Match", `W/"1"`) // stale: server is on version 2
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPreconditionFailed {
		// nosymbiotic SYM_GO_0089 -fp -- response body is from a controlled httptest.Server, not user input
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 412 Precondition Failed, got %d: %s", resp.StatusCode, string(raw))
	}
	var outcome map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&outcome))
	assert.Equal(t, "OperationOutcome", outcome["resourceType"])
	issues := outcome["issue"].([]any)
	first := issues[0].(map[string]any)
	assert.Equal(t, "conflict", first["code"])
}

// Sanity: non-conflict ErrConflict path mapping is also wired.
func TestHandlerErrConflict_DistinctFromNotFound(t *testing.T) {
	require.False(t, errors.Is(conceptmap.ErrNotFound, conceptmap.ErrConflict))
}

// firstMatchParam returns the first "match" parameter from a $translate response, or nil.
func firstMatchParam(params fhir.Parameters) *fhir.Parameter {
	for i := range params.Parameter {
		if params.Parameter[i].Name == "match" {
			return &params.Parameter[i]
		}
	}
	return nil
}

// matchConceptCode extracts the target code from a match parameter, or "".
func matchConceptCode(m *fhir.Parameter) string {
	if m == nil {
		return ""
	}
	for _, p := range m.Part {
		if p.Name == "concept" && p.ValueCoding != nil {
			return p.ValueCoding.Code
		}
	}
	return ""
}

// matchQualifierPartName returns the relationship-qualifier part name ("relationship" for R5, "equivalence" for R4), or "".
func matchQualifierPartName(m *fhir.Parameter) string {
	if m == nil {
		return ""
	}
	for _, p := range m.Part {
		if p.Name == "relationship" || p.Name == "equivalence" {
			return p.Name
		}
	}
	return ""
}

// resultBool extracts the "result" boolean from a $translate response.
func resultBool(t *testing.T, params fhir.Parameters) bool {
	t.Helper()
	for _, p := range params.Parameter {
		if p.Name == "result" && p.ValueBoolean != nil {
			return *p.ValueBoolean
		}
	}
	t.Fatal("response has no result parameter")
	return false
}

func TestHandler_Translate_Match_Returns200ResultTrue(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()
	seedAddressUseMap(repo)

	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: "http://example.org/fhir/ConceptMap/address-use"},
			{Name: "sourceCode", ValueCode: "home"},
			{Name: "sourceSystem", ValueURI: "http://hl7.org/fhir/address-use"},
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var params fhir.Parameters
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&params))
	assert.True(t, resultBool(t, params), "result must be true for known mapping")
	assert.Equal(t, "H", matchConceptCode(firstMatchParam(params)),
		"address-use home must map to H")
}

func TestHandler_Translate_NoMatch_Returns200ResultFalse(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()
	seedAddressUseMap(repo)

	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: "http://example.org/fhir/ConceptMap/address-use"},
			{Name: "sourceCode", ValueCode: "no-such-code"},
			{Name: "sourceSystem", ValueURI: "http://hl7.org/fhir/address-use"},
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "unmapped code must NOT be 404")
	var params fhir.Parameters
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&params))
	assert.False(t, resultBool(t, params), "result must be false for unmapped code")
	assert.Nil(t, firstMatchParam(params), "no `match` parameter must be present")
}

func TestHandler_Translate_Reverse_Returns200SourceConcept(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()
	seedAddressUseMap(repo)

	reverseTrue := true
	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: "http://example.org/fhir/ConceptMap/address-use"},
			{Name: "sourceCode", ValueCode: "H"},
			{Name: "sourceSystem", ValueURI: "http://terminology.hl7.org/CodeSystem/v3-AddressUse"},
			{Name: "reverse", ValueBoolean: &reverseTrue},
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var params fhir.Parameters
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&params))
	assert.True(t, resultBool(t, params))
	assert.Equal(t, "home", matchConceptCode(firstMatchParam(params)),
		"reverse(H) must surface source concept `home`")
}

// seedGenderDependencyMap seeds a ConceptMap with a gender dependency constraint.
func seedGenderDependencyMap(repo *mockRepo) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "gender-dep",
		URL:          "http://example.org/fhir/ConceptMap/gender-dep",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://src",
			Target: "http://tgt",
			Element: []conceptmap.Element{{
				Code: "X",
				Target: []conceptmap.Target{{
					Code:         "F",
					Relationship: "equivalent",
					DependsOn: []conceptmap.DependsOn{{
						Attribute: "gender",
						ValueCode: "female",
					}},
				}},
			}},
		}},
	}
	repo.store[cm.ID] = cm
	repo.byURL[cm.URL] = cm
}

func TestHandler_Translate_DependencyFilters_ExcludesNonMatchingTargets(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()
	seedGenderDependencyMap(repo)

	post := func(genderValue string) fhir.Parameters {
		t.Helper()
		body := fhir.Parameters{
			ResourceType: "Parameters",
			Parameter: []fhir.Parameter{
				{Name: "url", ValueURI: "http://example.org/fhir/ConceptMap/gender-dep"},
				{Name: "sourceCode", ValueCode: "X"},
				{Name: "sourceSystem", ValueURI: "http://src"},
				{Name: "dependency", Part: []fhir.Parameter{
					{Name: "attribute", ValueURI: "gender"},
					{Name: "value", ValueCode: genderValue},
				}},
			},
		}
		raw, _ := json.Marshal(body)
		resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var params fhir.Parameters
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&params))
		return params
	}

	femaleResp := post("female")
	assert.True(t, resultBool(t, femaleResp), "gender=female must match the female-only target")
	assert.Equal(t, "F", matchConceptCode(firstMatchParam(femaleResp)))

	maleResp := post("male")
	assert.False(t, resultBool(t, maleResp),
		"gender=male must NOT match the female-only target")
	assert.Nil(t, firstMatchParam(maleResp),
		"no match parameter must be emitted when the dependency filter excludes all targets")
}

// seedProductMap seeds a ConceptMap with a product attribute on the target.
func seedProductMap(repo *mockRepo) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "product-map",
		URL:          "http://example.org/fhir/ConceptMap/product-map",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://src",
			Target: "http://tgt",
			Element: []conceptmap.Element{{
				Code: "X",
				Target: []conceptmap.Target{{
					Code:         "Y",
					Relationship: "equivalent",
					Product: []conceptmap.DependsOn{{
						Attribute: "route",
						ValueCode: "oral",
					}},
				}},
			}},
		}},
	}
	repo.store[cm.ID] = cm
	repo.byURL[cm.URL] = cm
}

func TestHandler_Translate_ProductPassthrough(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()
	seedProductMap(repo)

	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: "http://example.org/fhir/ConceptMap/product-map"},
			{Name: "sourceCode", ValueCode: "X"},
			{Name: "sourceSystem", ValueURI: "http://src"},
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var params fhir.Parameters
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&params))
	match := firstMatchParam(params)
	require.NotNil(t, match, "match parameter must be present")

	var product *fhir.Parameter
	for i := range match.Part {
		if match.Part[i].Name == "product" {
			product = &match.Part[i]
			break
		}
	}
	require.NotNil(t, product, "match must contain a product part")

	var sawAttribute, sawValue bool
	for _, sub := range product.Part {
		if sub.Name == "attribute" && (sub.ValueURI == "route" || sub.ValueString == "route") {
			sawAttribute = true
		}
		if sub.Name == "value" && sub.ValueCode == "oral" {
			sawValue = true
		}
	}
	assert.True(t, sawAttribute, "product.attribute must be `route`")
	assert.True(t, sawValue, "product.value must be `oral`")
}

func TestHandler_Translate_CodeableConcept_PartialMatch(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()
	seedAddressUseMap(repo)

	// sourceCodeableConcept is polymorphic; build via raw JSON for clarity.
	raw := []byte(`{
		"resourceType": "Parameters",
		"parameter": [
			{"name": "url", "valueUri": "http://example.org/fhir/ConceptMap/address-use"},
			{"name": "sourceCodeableConcept", "valueCodeableConcept": {
				"coding": [
					{"system": "http://hl7.org/fhir/address-use", "code": "home"},
					{"system": "http://hl7.org/fhir/address-use", "code": "no-such-code"}
				]
			}}
		]
	}`)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"partial-miss must not 4xx/5xx; at least one coding matched")
	var params fhir.Parameters
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&params))
	assert.True(t, resultBool(t, params),
		"result must be true because the `home` coding matched")
	assert.Equal(t, "H", matchConceptCode(firstMatchParam(params)),
		"matched coding `home` must surface its target H")
}

func TestHandler_Translate_VersionPinning_UsesSpecifiedVersion(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	url := "http://example.org/fhir/ConceptMap/pinned"
	v1 := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "pinned-v1",
		URL:          url,
		Version:      "1.0",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://src",
			Target: "http://tgt",
			Element: []conceptmap.Element{{
				Code:   "X",
				Target: []conceptmap.Target{{Code: "V1-target", Relationship: "equivalent"}},
			}},
		}},
	}
	v2 := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "pinned-v2",
		URL:          url,
		Version:      "2.0",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://src",
			Target: "http://tgt",
			Element: []conceptmap.Element{{
				Code:   "X",
				Target: []conceptmap.Target{{Code: "V2-target", Relationship: "equivalent"}},
			}},
		}},
	}
	repo.store[v1.ID] = v1
	repo.store[v2.ID] = v2
	// byURL is the latest-by-URL index; insertion order here picks v2 as
	// "latest" so the pinning lookup must explicitly walk store and hit v1.
	repo.byURL[url] = v2

	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: url},
			{Name: "conceptMapVersion", ValueString: "1.0"},
			{Name: "sourceCode", ValueCode: "X"},
			{Name: "sourceSystem", ValueURI: "http://src"},
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var params fhir.Parameters
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&params))
	assert.Equal(t, "V1-target", matchConceptCode(firstMatchParam(params)),
		"conceptMapVersion=1.0 must select v1, not the latest v2")
}

func TestHandler_Translate_UnknownConceptMap_ReturnsResultFalse(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: "http://example.org/fhir/ConceptMap/does-not-exist"},
			{Name: "sourceCode", ValueCode: "X"},
			{Name: "sourceSystem", ValueURI: "http://src"},
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var params fhir.Parameters
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&params))
	assert.False(t, resultBool(t, params), "unknown ConceptMap must produce result=false")
}

// NOTE: shared-service assumption — if R4 and R5 are ever given separate services, seed each repo individually.
func TestHandler_Translate_R4VsR5_EquivalenceVsRelationship(t *testing.T) {
	repo := newMockRepo()
	seedAddressUseMap(repo)
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	r5mux := http.NewServeMux()
	NewConceptMapHandler(service, engine, "http://localhost", logger).RegisterRoutes(r5mux)
	r4mux := http.NewServeMux()
	NewR4ConceptMapHandler(service, engine, "http://localhost", logger).RegisterRoutes(r4mux)

	r5ts := httptest.NewServer(r5mux)
	defer r5ts.Close()
	r4ts := httptest.NewServer(r4mux)
	defer r4ts.Close()

	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: "http://example.org/fhir/ConceptMap/address-use"},
			{Name: "sourceCode", ValueCode: "home"},
			{Name: "sourceSystem", ValueURI: "http://hl7.org/fhir/address-use"},
		},
	}
	raw, _ := json.Marshal(body)

	r5Resp, err := http.Post(r5ts.URL+"/fhir/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer r5Resp.Body.Close()
	require.Equal(t, http.StatusOK, r5Resp.StatusCode)
	var r5Params fhir.Parameters
	require.NoError(t, json.NewDecoder(r5Resp.Body).Decode(&r5Params))
	assert.Equal(t, "relationship", matchQualifierPartName(firstMatchParam(r5Params)),
		"R5 must use match.relationship")

	r4Resp, err := http.Post(r4ts.URL+"/fhir/R4/ConceptMap/$translate", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer r4Resp.Body.Close()
	require.Equal(t, http.StatusOK, r4Resp.StatusCode)
	var r4Params fhir.Parameters
	require.NoError(t, json.NewDecoder(r4Resp.Body).Decode(&r4Params))
	assert.Equal(t, "equivalence", matchQualifierPartName(firstMatchParam(r4Params)),
		"R4 must use match.equivalence")
}

func TestHandler_TranslateBatch_ReverseRejected_Returns400(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()
	seedAddressUseMap(repo)

	reverseTrue := true
	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: "http://example.org/fhir/ConceptMap/address-use"},
			{Name: "reverse", ValueBoolean: &reverseTrue},
			{Name: "code", Part: []fhir.Parameter{
				{Name: "code", ValueCode: "home"},
				{Name: "system", ValueURI: "http://hl7.org/fhir/address-use"},
			}},
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate-batch", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	issue := requireIssue(t, resp.Body, "invalid")
	diag, _ := issue["diagnostics"].(string)
	assert.Contains(t, strings.ToLower(diag), "reverse",
		"diagnostics must mention reverse; got %q", diag)
}

// ─── batch test helpers ────────────────────────────────────────────

// mockFlatStore implements translate.FlatStore in memory for batch tests against FlatEngine.
type mockFlatStore struct {
	repo *mockRepo
}

func (s *mockFlatStore) ResolveConceptMap(_ context.Context, req translate.Request) (translate.FlatConceptMapRef, error) {
	var cm *conceptmap.ConceptMap
	if req.ConceptMapID != "" {
		c, ok := s.repo.store[req.ConceptMapID]
		if !ok {
			return translate.FlatConceptMapRef{}, conceptmap.ErrNotFound
		}
		cm = c
	} else if req.URL != "" {
		if req.Version != "" {
			for _, c := range s.repo.store {
				if c.URL == req.URL && c.Version == req.Version {
					cm = c
					break
				}
			}
		} else {
			c, ok := s.repo.byURL[req.URL]
			if ok {
				cm = c
			}
		}
		if cm == nil {
			return translate.FlatConceptMapRef{}, conceptmap.ErrNotFound
		}
	} else {
		return translate.FlatConceptMapRef{}, conceptmap.ErrNotFound
	}
	return translate.FlatConceptMapRef{PK: 0, URL: cm.URL}, nil
}

func (s *mockFlatStore) QueryForward(_ context.Context, _ int64, sourceSystem, sourceCode, targetSystemFilter string) ([]translate.FlatRow, error) {
	var rows []translate.FlatRow
	for _, cm := range s.repo.store {
		for _, group := range cm.Group {
			if sourceSystem != "" && group.Source != "" && group.Source != sourceSystem {
				continue
			}
			if targetSystemFilter != "" && group.Target != "" && group.Target != targetSystemFilter {
				continue
			}
			for _, elem := range group.Element {
				if elem.Code != sourceCode {
					continue
				}
				for _, tgt := range elem.Target {
					rows = append(rows, translate.FlatRow{
						SourceSystem: group.Source,
						SourceCode:   elem.Code,
						TargetSystem: group.Target,
						TargetCode:   tgt.Code,
						Relationship: tgt.Relationship,
					})
				}
			}
		}
	}
	return rows, nil
}

func (s *mockFlatStore) QueryReverse(_ context.Context, _ int64, targetSystem, targetCode, targetSystemFilter string) ([]translate.FlatRow, error) {
	var rows []translate.FlatRow
	for _, cm := range s.repo.store {
		for _, group := range cm.Group {
			if targetSystem != "" && group.Target != "" && group.Target != targetSystem {
				continue
			}
			if targetSystemFilter != "" && group.Source != "" && group.Source != targetSystemFilter {
				continue
			}
			for _, elem := range group.Element {
				for _, tgt := range elem.Target {
					if tgt.Code == targetCode {
						rows = append(rows, translate.FlatRow{
							SourceSystem: group.Source,
							SourceCode:   elem.Code,
							TargetSystem: group.Target,
							TargetCode:   tgt.Code,
							Relationship: tgt.Relationship,
						})
					}
				}
			}
		}
	}
	return rows, nil
}

func (s *mockFlatStore) GroupUnmapped(_ context.Context, _ int64, groupSource string) (*translate.FlatUnmapped, error) {
	for _, cm := range s.repo.store {
		for _, group := range cm.Group {
			if group.Source == groupSource && group.Unmapped != nil {
				return &translate.FlatUnmapped{
					Mode:         group.Unmapped.Mode,
					Code:         group.Unmapped.Code,
					Display:      group.Unmapped.Display,
					Relationship: group.Unmapped.Relationship,
					OtherMap:     group.Unmapped.OtherMap,
					GroupSource:  group.Source,
					GroupTarget:  group.Target,
				}, nil
			}
		}
	}
	return nil, nil
}

// setupBatchTestServer builds an httptest.Server wired with FlatEngine so the
// TranslateBatch handler can exercise the BatchTranslator interface. The
// mockFlatStore bridges the mockRepo in-memory fixtures to the FlatStore API.
func setupBatchTestServer(t *testing.T) (*httptest.Server, *mockRepo) {
	t.Helper()
	repo := newMockRepo()
	flat := &mockFlatStore{repo: repo}
	service := conceptmap.NewService(repo)
	engine := translate.NewFlatEngine(flat)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mux := http.NewServeMux()
	NewConceptMapHandler(service, engine, "http://localhost", logger).RegisterRoutes(mux)
	return httptest.NewServer(Middleware(mux, MaxBodyBytesMiddleware(10<<20))), repo
}

// probePart builds a single code probe parameter for a $translate-batch body.
func probePart(code, system string) fhir.Parameter {
	return fhir.Parameter{
		Name: "code",
		Part: []fhir.Parameter{
			{Name: "code", ValueCode: code},
			{Name: "system", ValueURI: system},
		},
	}
}

// batchResultParts extracts all translate sub-parameters from a batch response.
func batchResultParts(params fhir.Parameters) []fhir.Parameter {
	var out []fhir.Parameter
	for _, p := range params.Parameter {
		if p.Name == "translate" {
			out = append(out, p)
		}
	}
	return out
}

// probeResult extracts the boolean result from a translate part.
func probeResult(t *testing.T, probe fhir.Parameter) bool {
	t.Helper()
	for _, p := range probe.Part {
		if p.Name == "result" && p.ValueBoolean != nil {
			return *p.ValueBoolean
		}
	}
	t.Fatalf("translate part has no result sub-parameter")
	return false
}

// probeUnmappedPart returns the unmapped sub-part from a translate part, or nil.
func probeUnmappedPart(probe fhir.Parameter) *fhir.Parameter {
	for i := range probe.Part {
		if probe.Part[i].Name == "unmapped" {
			return &probe.Part[i]
		}
	}
	return nil
}

// seedLarge50Map seeds a ConceptMap with exactly 50 mappings.
func seedLarge50Map(repo *mockRepo) *conceptmap.ConceptMap {
	const n = 50
	elements := make([]conceptmap.Element, n)
	for i := range elements {
		code := "SRC-" + fmt.Sprintf("%d", i)
		elements[i] = conceptmap.Element{
			Code:   code,
			Target: []conceptmap.Target{{Code: "TGT-" + fmt.Sprintf("%d", i), Relationship: "equivalent"}},
		}
	}
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "large-50",
		URL:          "http://example.org/fhir/ConceptMap/large-50",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source:  "http://src",
			Target:  "http://tgt",
			Element: elements,
		}},
	}
	repo.store[cm.ID] = cm
	repo.byURL[cm.URL] = cm
	return cm
}

func TestHandler_TranslateBatch_AllMatch(t *testing.T) {
	ts, repo := setupBatchTestServer(t)
	defer ts.Close()
	cm := seedLarge50Map(repo)

	const nProbes = 50
	params := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter:    make([]fhir.Parameter, 0, nProbes+1),
	}
	params.Parameter = append(params.Parameter, fhir.Parameter{Name: "url", ValueURI: cm.URL})
	for i := 0; i < nProbes; i++ {
		params.Parameter = append(params.Parameter,
			probePart(fmt.Sprintf("SRC-%d", i), "http://src"))
	}

	raw, _ := json.Marshal(params)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate-batch", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got fhir.Parameters
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))

	translateParts := batchResultParts(got)
	require.Len(t, translateParts, nProbes, "must have exactly one translate entry per probe")
	for i, tp := range translateParts {
		assert.True(t, probeResult(t, tp), "probe %d (SRC-%d) must be result=true", i, i)
	}
}

func TestHandler_TranslateBatch_PartialUnmapped(t *testing.T) {
	ts, repo := setupBatchTestServer(t)
	defer ts.Close()
	seedAddressUseMap(repo)

	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: "http://example.org/fhir/ConceptMap/address-use"},
			probePart("home", "http://hl7.org/fhir/address-use"),
			probePart("unknown", "http://hl7.org/fhir/address-use"),
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate-batch", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "mixed results must still be 200")
	var params fhir.Parameters
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&params))

	tps := batchResultParts(params)
	require.Len(t, tps, 2)

	assert.True(t, probeResult(t, tps[0]), "home probe must be result=true")
	assert.False(t, probeResult(t, tps[1]), "unknown probe must be result=false")

	unmapped := probeUnmappedPart(tps[1])
	require.NotNil(t, unmapped, "unmapped probe must carry an `unmapped` part")
	var sawCode, sawSystem bool
	for _, sub := range unmapped.Part {
		if sub.Name == "code" && sub.ValueCode == "unknown" {
			sawCode = true
		}
		if sub.Name == "system" && sub.ValueURI == "http://hl7.org/fhir/address-use" {
			sawSystem = true
		}
	}
	assert.True(t, sawCode, "unmapped.code must equal the input code")
	assert.True(t, sawSystem, "unmapped.system must equal the input system")
}

// seedFixedUnmappedMap seeds a ConceptMap with mode=fixed unmapped handling.
func seedFixedUnmappedMap(repo *mockRepo) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "fixed-unmapped",
		URL:          "http://example.org/fhir/ConceptMap/fixed-unmapped",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://src",
			Target: "http://tgt",
			Element: []conceptmap.Element{{
				Code:   "known",
				Target: []conceptmap.Target{{Code: "KNOWN-TARGET", Relationship: "equivalent"}},
			}},
			Unmapped: &conceptmap.Unmapped{
				Mode:         "fixed",
				Code:         "FALLBACK-CODE",
				Relationship: "related-to",
			},
		}},
	}
	repo.store[cm.ID] = cm
	repo.byURL[cm.URL] = cm
}

func TestHandler_TranslateBatch_FixedUnmapped(t *testing.T) {
	ts, repo := setupBatchTestServer(t)
	defer ts.Close()
	seedFixedUnmappedMap(repo)

	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: "http://example.org/fhir/ConceptMap/fixed-unmapped"},
			probePart("not-in-map", "http://src"),
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate-batch", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var params fhir.Parameters
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&params))

	tps := batchResultParts(params)
	require.Len(t, tps, 1)
	assert.True(t, probeResult(t, tps[0]), "fixed-unmapped probe must return result=true")

	var matchCode string
	for _, part := range tps[0].Part {
		if part.Name == "match" {
			for _, mp := range part.Part {
				if mp.Name == "concept" && mp.ValueCoding != nil {
					matchCode = mp.ValueCoding.Code
				}
			}
		}
	}
	assert.Equal(t, "FALLBACK-CODE", matchCode,
		"fixed-unmapped result must carry the declared fallback code")
}

func TestHandler_TranslateBatch_MissingProbes_Returns422(t *testing.T) {
	ts, _ := setupBatchTestServer(t)
	defer ts.Close()

	body := []byte(`{"resourceType":"Parameters","parameter":[{"name":"url","valueUri":"http://example.org/fhir/ConceptMap/address-use"}]}`)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate-batch", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	requireIssue(t, resp.Body, "invalid")
}

func TestHandler_TranslateBatch_MissingURL_Returns422(t *testing.T) {
	ts, _ := setupBatchTestServer(t)
	defer ts.Close()

	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter:    []fhir.Parameter{probePart("home", "http://hl7.org/fhir/address-use")},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate-batch", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	requireIssue(t, resp.Body, "invalid")
}

// seedCyclicOtherMapPair seeds two ConceptMaps with a cyclic other-map chain.
func seedCyclicOtherMapPair(repo *mockRepo) {
	a := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "cycle-a",
		URL:          "http://example.org/fhir/ConceptMap/cycle-a",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://src",
			Target: "http://tgt",
			Element: []conceptmap.Element{{
				Code:   "only-a",
				Target: []conceptmap.Target{{Code: "AA", Relationship: "equivalent"}},
			}},
			Unmapped: &conceptmap.Unmapped{
				Mode:     "other-map",
				OtherMap: "http://example.org/fhir/ConceptMap/cycle-b",
			},
		}},
	}
	b := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "cycle-b",
		URL:          "http://example.org/fhir/ConceptMap/cycle-b",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://src",
			Target: "http://tgt",
			Element: []conceptmap.Element{{
				Code:   "only-b",
				Target: []conceptmap.Target{{Code: "BB", Relationship: "equivalent"}},
			}},
			Unmapped: &conceptmap.Unmapped{
				Mode:     "other-map",
				OtherMap: "http://example.org/fhir/ConceptMap/cycle-a",
			},
		}},
	}
	repo.store[a.ID] = a
	repo.byURL[a.URL] = a
	repo.store[b.ID] = b
	repo.byURL[b.URL] = b
}

// Engine-layer cycle coverage is already in engine_m1_test.go: TestTranslate_Unmapped_OtherMap_CycleCappedAtFive.
func TestHandler_TranslateBatch_OtherMapDepthCap(t *testing.T) {
	ts, repo := setupBatchTestServer(t)
	defer ts.Close()
	seedCyclicOtherMapPair(repo)

	body := fhir.Parameters{
		ResourceType: "Parameters",
		Parameter: []fhir.Parameter{
			{Name: "url", ValueURI: "http://example.org/fhir/ConceptMap/cycle-a"},
			probePart("nope", "http://src"), // not in either map → triggers cycle
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap/$translate-batch", "application/fhir+json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"depth-cap error must not surface as 5xx; batch must return 200")

	var params fhir.Parameters
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&params))

	tps := batchResultParts(params)
	require.Len(t, tps, 1, "must have exactly one translate entry for the single probe")

	// AC-4: the capped probe must return result=false with a diagnostic message.
	assert.False(t, probeResult(t, tps[0]),
		"depth-capped probe must return result=false")

	var gotMessage string
	for _, p := range tps[0].Part {
		if p.Name == "message" {
			gotMessage = p.ValueString
			break
		}
	}
	assert.NotEmpty(t, gotMessage,
		"depth-capped probe must carry a non-empty diagnostic message part")
}

// ─── R4 ingress strictness ────────────────────────────────────────

func TestHandler_R4_ConceptMap_Create_RejectsR5OnlyField(t *testing.T) {
	repo := newMockRepo()
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	NewR4ConceptMapHandler(service, engine, "http://localhost", logger).RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"resourceType":           "ConceptMap",
		"status":                 "active",
		"versionAlgorithmString": "semver", // R5-only field
	})
	resp, err := http.Post(ts.URL+"/fhir/R4/ConceptMap", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	issue := requireIssue(t, resp.Body, "not-supported")
	diag, _ := issue["diagnostics"].(string)
	assert.Contains(t, diag, "versionAlgorithmString", "diagnostic must name the offending R5-only field")
}

func TestHandler_R4_ConceptMap_Update_RejectsR5OnlyField(t *testing.T) {
	repo := newMockRepo()
	service := conceptmap.NewService(repo)
	engine := translate.NewEngine(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	NewR4ConceptMapHandler(service, engine, "http://localhost", logger).RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cm := &conceptmap.ConceptMap{ResourceType: "ConceptMap", ID: "r4-cm-1", Status: "active", Meta: &fhir.Meta{VersionID: "1"}}
	repo.store["r4-cm-1"] = cm
	repo.versionSeq = 1

	body, _ := json.Marshal(map[string]any{
		"resourceType":           "ConceptMap",
		"id":                     "r4-cm-1",
		"status":                 "active",
		"versionAlgorithmString": "semver", // R5-only field
	})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/fhir/R4/ConceptMap/r4-cm-1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	issue := requireIssue(t, resp.Body, "not-supported")
	diag, _ := issue["diagnostics"].(string)
	assert.Contains(t, diag, "versionAlgorithmString", "diagnostic must name the offending R5-only field")
}

func TestHandler_R5_ConceptMap_Create_AcceptsR5OnlyField(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"resourceType":           "ConceptMap",
		"status":                 "active",
		"versionAlgorithmString": "semver",
	})
	resp, err := http.Post(ts.URL+"/fhir/ConceptMap", "application/fhir+json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode, "R5 tree must accept R5-only fields")
}

// ─── _validate case-fold harmonisation ───────────────────────────

func TestHandler_ConceptMap_ValidateModeIsCaseInsensitive(t *testing.T) {
	for _, qParam := range []string{"lenient", "Lenient", "LENIENT"} {
		t.Run(qParam, func(t *testing.T) {
			ts, _ := setupTestServer(t)
			defer ts.Close()

			// No status field → fails strict validation (ErrUnprocessable → 422).
			// Must succeed in lenient mode regardless of case.
			body, _ := json.Marshal(map[string]any{
				"resourceType": "ConceptMap",
			})
			resp, err := http.Post(ts.URL+"/fhir/ConceptMap?_validate="+qParam, "application/fhir+json", bytes.NewReader(body))
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusCreated, resp.StatusCode,
				"_validate=%s must map to lenient mode and succeed", qParam)
		})
	}
}

// ─── ConceptMap Vread delete-version ───────────────────

func TestHandler_ConceptMap_VreadDeleteVersion_Returns410(t *testing.T) {
	stub := &stubHistoryReader{byID: map[string][]conceptmap.HistoryEntry{
		"del-cm": {
			{VersionID: 2, Operation: "delete", OccurredAt: "2026-05-24T10:00:01Z"},
			{VersionID: 1, Operation: "create", OccurredAt: "2026-05-24T10:00:00Z",
				Resource: &conceptmap.ConceptMap{ResourceType: "ConceptMap", ID: "del-cm", Status: "active"}},
		},
	}}
	ts := startServer(t, stub)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap/del-cm/_history/2")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusGone, resp.StatusCode, "vread of a delete-op version must return 410")
	assert.Equal(t, `W/"2"`, resp.Header.Get("ETag"))
}

// ─── TranslateInstance error paths ───────────────────────────────────────────

func TestHandler_TranslateInstance_ErrGone_Returns410(t *testing.T) {
	ts, repo := setupTestServer(t)
	defer ts.Close()

	cm := seedAddressUseMap(repo)
	repo.deleted[cm.ID] = true

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap/" + cm.ID + "/$translate?code=home&system=http://hl7.org/fhir/address-use")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusGone, resp.StatusCode)
}

func TestHandler_TranslateInstance_ErrNotFound_ReturnsResultFalse(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fhir/ConceptMap/nonexistent-cm/$translate?code=home&system=http://hl7.org/fhir/address-use")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "Parameters", body["resourceType"])
}

func TestHandler_TranslateInstance_BadJSONBody_Returns400(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/fhir/ConceptMap/some-id/$translate",
		bytes.NewReader([]byte(`{invalid`)))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ─── Search parameter coverage ────────────────────────────────────────────────

func TestHandler_ConceptMap_Search_CountOffset(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	searchResp, err := http.Get(ts.URL + "/fhir/ConceptMap?_count=5&_offset=0")
	require.NoError(t, err)
	defer searchResp.Body.Close()
	require.Equal(t, http.StatusOK, searchResp.StatusCode)
	var bundle map[string]any
	require.NoError(t, json.NewDecoder(searchResp.Body).Decode(&bundle))
	assert.Equal(t, "Bundle", bundle["resourceType"])
}

func TestHandler_ConceptMap_Search_WithModifierSuffix(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	searchResp, err := http.Get(ts.URL + "/fhir/ConceptMap?status:exact=active")
	require.NoError(t, err)
	defer searchResp.Body.Close()
	require.Equal(t, http.StatusOK, searchResp.StatusCode)
}

func TestHandler_TranslateBatch_BadJSONBody_Returns400(t *testing.T) {
	ts, _ := setupBatchTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/fhir/ConceptMap/$translate-batch",
		bytes.NewReader([]byte(`{not json`)))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
