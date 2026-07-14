package translate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// mockRepo implements conceptmap.Repository for unit testing.
type mockRepo struct {
	conceptMaps map[string]*conceptmap.ConceptMap
	byURL       map[string]*conceptmap.ConceptMap
}

func newMockRepo() *mockRepo {
	return &mockRepo{
		conceptMaps: make(map[string]*conceptmap.ConceptMap),
		byURL:       make(map[string]*conceptmap.ConceptMap),
	}
}

func (m *mockRepo) Create(_ context.Context, cm *conceptmap.ConceptMap) (*conceptmap.ConceptMap, error) {
	m.conceptMaps[cm.ID] = cm
	if cm.URL != "" {
		m.byURL[cm.URL] = cm
	}
	return cm, nil
}

func (m *mockRepo) Read(_ context.Context, id string) (*conceptmap.ConceptMap, error) {
	cm, ok := m.conceptMaps[id]
	if !ok {
		return nil, conceptmap.ErrNotFound
	}
	return cm, nil
}

func (m *mockRepo) Update(_ context.Context, id string, cm *conceptmap.ConceptMap) (*conceptmap.ConceptMap, error) {
	if _, ok := m.conceptMaps[id]; !ok {
		return nil, conceptmap.ErrNotFound
	}
	m.conceptMaps[id] = cm
	return cm, nil
}

func (m *mockRepo) Delete(_ context.Context, id string) error {
	if _, ok := m.conceptMaps[id]; !ok {
		return conceptmap.ErrNotFound
	}
	delete(m.conceptMaps, id)
	return nil
}

func (m *mockRepo) Search(_ context.Context, _ conceptmap.SearchParams) (*conceptmap.SearchResult, error) {
	results := make([]conceptmap.ConceptMap, 0, len(m.conceptMaps))
	for _, cm := range m.conceptMaps {
		results = append(results, *cm)
	}
	return &conceptmap.SearchResult{ConceptMaps: results, Total: len(results)}, nil
}

func (m *mockRepo) FindByURL(_ context.Context, url string, _ string) (*conceptmap.ConceptMap, error) {
	cm, ok := m.byURL[url]
	if !ok {
		return nil, conceptmap.ErrNotFound
	}
	return cm, nil
}

func (m *mockRepo) FindBySourceScope(_ context.Context, scope string) (*conceptmap.ConceptMap, error) {
	for _, cm := range m.conceptMaps {
		if cm.SourceScopeURI == scope || cm.SourceScopeCanonical == scope {
			return cm, nil
		}
	}
	return nil, conceptmap.ErrNotFound
}

// --- Test Data: FHIR Example 101 (Address Use Mapping) ---

func makeExample101() *conceptmap.ConceptMap {
	return &conceptmap.ConceptMap{
		ResourceType:   "ConceptMap",
		ID:             "101",
		URL:            "http://hl7.org/fhir/ConceptMap/101",
		Name:           "FHIRv3AddressUse",
		Title:          "FHIR/v3 Address Use Mapping",
		Status:         "draft",
		SourceScopeURI: "http://hl7.org/fhir/ValueSet/address-use",
		TargetScopeURI: "http://terminology.hl7.org/ValueSet/v3-AddressUse",
		Group: []conceptmap.Group{
			{
				Source: "http://hl7.org/fhir/address-use",
				Target: "http://terminology.hl7.org/CodeSystem/v3-AddressUse",
				Element: []conceptmap.Element{
					{
						Code: "home", Display: "Home",
						Target: []conceptmap.Target{
							{Code: "H", Display: "home address", Relationship: "equivalent"},
						},
					},
					{
						Code: "work", Display: "Work",
						Target: []conceptmap.Target{
							{Code: "WP", Display: "work place", Relationship: "equivalent"},
						},
					},
					{
						Code: "temp", Display: "Temporary",
						Target: []conceptmap.Target{
							{Code: "TMP", Display: "temporary address", Relationship: "equivalent"},
						},
					},
					{
						Code: "old", Display: "Old / Incorrect",
						Target: []conceptmap.Target{
							{Code: "BAD", Display: "bad address", Relationship: "not-related-to",
								Comment: "In the HL7 v3 AD, old is handled by the usablePeriod element"},
						},
					},
				},
				Unmapped: &conceptmap.Unmapped{
					Mode:         "fixed",
					Code:         "temp",
					Display:      "temp",
					Relationship: "related-to",
				},
			},
		},
	}
}

// --- Test Data: FHIR Example 103 (SNOMED CT / ICD-10) ---

func makeExample103() *conceptmap.ConceptMap {
	return &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "103",
		URL:          "http://hl7.org/fhir/ConceptMap/103",
		Name:         "SNOMEDCTToICD10CMMappingsForFractureOfUlna",
		Status:       "draft",
		Group: []conceptmap.Group{
			{
				Source: "http://snomed.info/sct",
				Target: "http://hl7.org/fhir/sid/icd-10-cm",
				Element: []conceptmap.Element{
					{
						Code: "263204007",
						Target: []conceptmap.Target{
							{Code: "S52.209A", Relationship: "source-is-broader-than-target"},
						},
					},
					{
						Code: "263204007",
						Target: []conceptmap.Target{
							{Code: "S52.209D", Relationship: "source-is-broader-than-target"},
						},
					},
				},
			},
		},
	}
}

// --- Tests ---

func TestTranslateForward_Example101_Home(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/101",
		SourceCode:   "home",
		SourceSystem: "http://hl7.org/fhir/address-use",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	require.Len(t, resp.Matches, 1)
	assert.Equal(t, "equivalent", resp.Matches[0].Relationship)
	assert.Equal(t, "H", resp.Matches[0].Concept.Code)
	assert.Equal(t, "http://terminology.hl7.org/CodeSystem/v3-AddressUse", resp.Matches[0].Concept.System)
	assert.Equal(t, "home address", resp.Matches[0].Concept.Display)
}

func TestTranslateForward_Example101_Work(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/101",
		SourceCode:   "work",
		SourceSystem: "http://hl7.org/fhir/address-use",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	require.Len(t, resp.Matches, 1)
	assert.Equal(t, "equivalent", resp.Matches[0].Relationship)
	assert.Equal(t, "WP", resp.Matches[0].Concept.Code)
}

func TestTranslateForward_Example101_Old_NotRelatedTo(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/101",
		SourceCode:   "old",
		SourceSystem: "http://hl7.org/fhir/address-use",
	})

	require.NoError(t, err)
	// Result should be false because the only match is "not-related-to"
	assert.False(t, resp.Result)
	require.Len(t, resp.Matches, 1)
	assert.Equal(t, "not-related-to", resp.Matches[0].Relationship)
	assert.Equal(t, "BAD", resp.Matches[0].Concept.Code)
}

func TestTranslateForward_Example101_Unmapped(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	// "billing" is not in the map - should use unmapped.mode=fixed
	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/101",
		SourceCode:   "billing",
		SourceSystem: "http://hl7.org/fhir/address-use",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	require.Len(t, resp.Matches, 1)
	assert.Equal(t, "related-to", resp.Matches[0].Relationship)
	assert.Equal(t, "temp", resp.Matches[0].Concept.Code)
}

func TestTranslateForward_Example103_MultipleTargets(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample103())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/103",
		SourceCode:   "263204007",
		SourceSystem: "http://snomed.info/sct",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	require.Len(t, resp.Matches, 2)
	assert.Equal(t, "source-is-broader-than-target", resp.Matches[0].Relationship)
	assert.Equal(t, "S52.209A", resp.Matches[0].Concept.Code)
	assert.Equal(t, "S52.209D", resp.Matches[1].Concept.Code)
}

func TestTranslateForward_WithCoding(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL: "http://hl7.org/fhir/ConceptMap/101",
		SourceCoding: &fhir.Coding{
			System: "http://hl7.org/fhir/address-use",
			Code:   "temp",
		},
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	require.Len(t, resp.Matches, 1)
	assert.Equal(t, "TMP", resp.Matches[0].Concept.Code)
}

func TestTranslateForward_WithCodeableConcept(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL: "http://hl7.org/fhir/ConceptMap/101",
		SourceCodeableConcept: &fhir.CodeableConcept{
			Coding: []fhir.Coding{
				{System: "http://hl7.org/fhir/address-use", Code: "work"},
			},
		},
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	require.Len(t, resp.Matches, 1)
	assert.Equal(t, "WP", resp.Matches[0].Concept.Code)
}

func TestTranslateForward_NoMapping(t *testing.T) {
	repo := newMockRepo()
	cm := makeExample103() // no unmapped mode
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/103",
		SourceCode:   "999999999",
		SourceSystem: "http://snomed.info/sct",
	})

	require.NoError(t, err)
	assert.False(t, resp.Result)
	assert.Empty(t, resp.Matches)
	assert.Contains(t, resp.Message, "No mapping found")
}

func TestTranslateForward_TargetSystemFilter(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample103())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/103",
		SourceCode:   "263204007",
		SourceSystem: "http://snomed.info/sct",
		TargetSystem: "http://hl7.org/fhir/sid/icd-10-cm",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	assert.Len(t, resp.Matches, 2)

	resp, err = engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/103",
		SourceCode:   "263204007",
		SourceSystem: "http://snomed.info/sct",
		TargetSystem: "http://wrong.system",
	})

	require.NoError(t, err)
	assert.False(t, resp.Result)
	assert.Empty(t, resp.Matches)
}

func TestTranslateReverse(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:        "http://hl7.org/fhir/ConceptMap/101",
		TargetCode: "H",
		TargetCoding: &fhir.Coding{
			System: "http://terminology.hl7.org/CodeSystem/v3-AddressUse",
			Code:   "H",
		},
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	require.Len(t, resp.Matches, 1)
	assert.Equal(t, "equivalent", resp.Matches[0].Relationship)
	assert.Equal(t, "home", resp.Matches[0].Concept.Code)
	assert.Equal(t, "http://hl7.org/fhir/address-use", resp.Matches[0].Concept.System)
}

func TestTranslateReverse_BroaderNarrowerFlip(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample103())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL: "http://hl7.org/fhir/ConceptMap/103",
		TargetCoding: &fhir.Coding{
			System: "http://hl7.org/fhir/sid/icd-10-cm",
			Code:   "S52.209A",
		},
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	require.Len(t, resp.Matches, 1)
	// source-is-broader-than-target becomes source-is-narrower-than-target in reverse
	assert.Equal(t, "source-is-narrower-than-target", resp.Matches[0].Relationship)
	assert.Equal(t, "263204007", resp.Matches[0].Concept.Code)
}

func TestTranslate_ByID(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		ConceptMapID: "101",
		SourceCode:   "home",
		SourceSystem: "http://hl7.org/fhir/address-use",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	assert.Equal(t, "H", resp.Matches[0].Concept.Code)
}

func TestTranslate_InlineConceptMap(t *testing.T) {
	engine := NewEngine(newMockRepo())

	cm := makeExample101()
	resp, err := engine.Translate(context.Background(), Request{
		ConceptMap:   cm,
		SourceCode:   "temp",
		SourceSystem: "http://hl7.org/fhir/address-use",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	assert.Equal(t, "TMP", resp.Matches[0].Concept.Code)
}

func TestTranslate_ConceptMapNotFound(t *testing.T) {
	engine := NewEngine(newMockRepo())

	_, err := engine.Translate(context.Background(), Request{
		URL:        "http://nonexistent",
		SourceCode: "test",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestTranslate_EmptySourceCode_TriggersUnmapped(t *testing.T) {
	engine := NewEngine(newMockRepo())

	resp, err := engine.Translate(context.Background(), Request{
		ConceptMap: makeExample101(),
		SourceCode: "",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result) // "related-to" from unmapped is not "not-related-to"
	require.Len(t, resp.Matches, 1)
	assert.Equal(t, "temp", resp.Matches[0].Concept.Code)
	assert.Equal(t, "related-to", resp.Matches[0].Relationship)
}

func TestTranslateForward_SourceSystemMismatch(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/101",
		SourceCode:   "home",
		SourceSystem: "http://wrong.system/codes",
	})

	require.NoError(t, err)
	assert.False(t, resp.Result)
}

func TestTranslate_OriginMapIncluded(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/101",
		SourceCode:   "home",
		SourceSystem: "http://hl7.org/fhir/address-use",
	})

	require.NoError(t, err)
	assert.Equal(t, "http://hl7.org/fhir/ConceptMap/101", resp.Matches[0].OriginMap)
}

// =============================================================================
// Phase 1.1: Multi-coding CodeableConcept translation
// =============================================================================

func TestTranslate_CodeableConcept_MultipleCoding_AllMatched(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	// Two codings that both have mappings
	resp, err := engine.Translate(context.Background(), Request{
		URL: "http://hl7.org/fhir/ConceptMap/101",
		SourceCodeableConcept: &fhir.CodeableConcept{
			Coding: []fhir.Coding{
				{System: "http://hl7.org/fhir/address-use", Code: "home"},
				{System: "http://hl7.org/fhir/address-use", Code: "work"},
			},
		},
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	assert.GreaterOrEqual(t, len(resp.Matches), 2)

	codes := make(map[string]bool)
	for _, m := range resp.Matches {
		codes[m.Concept.Code] = true
	}
	assert.True(t, codes["H"], "expected 'H' in results")
	assert.True(t, codes["WP"], "expected 'WP' in results")
}

func TestTranslate_CodeableConcept_MultipleCoding_OneMatched(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	// First coding has a mapping, second does not exist in the map (but unmapped applies)
	resp, err := engine.Translate(context.Background(), Request{
		URL: "http://hl7.org/fhir/ConceptMap/101",
		SourceCodeableConcept: &fhir.CodeableConcept{
			Coding: []fhir.Coding{
				{System: "http://hl7.org/fhir/address-use", Code: "home"},
				{System: "http://other.system", Code: "xyz"},
			},
		},
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	found := false
	for _, m := range resp.Matches {
		if m.Concept.Code == "H" {
			found = true
		}
	}
	assert.True(t, found, "expected 'H' in results from first coding")
}

func TestTranslate_CodeableConcept_MultipleCoding_NoneMatched(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample103()) // No unmapped mode
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL: "http://hl7.org/fhir/ConceptMap/103",
		SourceCodeableConcept: &fhir.CodeableConcept{
			Coding: []fhir.Coding{
				{System: "http://snomed.info/sct", Code: "999999"},
				{System: "http://snomed.info/sct", Code: "888888"},
			},
		},
	})

	require.NoError(t, err)
	assert.False(t, resp.Result)
	assert.Empty(t, resp.Matches)
}

func TestTranslate_CodeableConcept_MultipleCoding_Deduplication(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	// Same coding repeated - should not produce duplicate results
	resp, err := engine.Translate(context.Background(), Request{
		URL: "http://hl7.org/fhir/ConceptMap/101",
		SourceCodeableConcept: &fhir.CodeableConcept{
			Coding: []fhir.Coding{
				{System: "http://hl7.org/fhir/address-use", Code: "home"},
				{System: "http://hl7.org/fhir/address-use", Code: "home"},
			},
		},
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	count := 0
	for _, m := range resp.Matches {
		if m.Concept.Code == "H" {
			count++
		}
	}
	assert.Equal(t, 1, count, "expected exactly one 'H' match after deduplication")
}

// =============================================================================
// Phase 1.2: Result deduplication (same code from different element entries)
// =============================================================================

func TestTranslate_Deduplication_SameCodeDifferentElements(t *testing.T) {
	// Example 103 has TWO elements with code 263204007, each mapping to different targets
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample103())
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/103",
		SourceCode:   "263204007",
		SourceSystem: "http://snomed.info/sct",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	// These map to DIFFERENT target codes (S52.209A, S52.209D) so both should remain
	assert.Equal(t, 2, len(resp.Matches))
}

func TestTranslate_Deduplication_IdenticalMatches(t *testing.T) {
	// ConceptMap with duplicate mappings (same source->same target)
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "dup-test",
		URL:          "http://example.org/dup",
		Status:       "active",
		Group: []conceptmap.Group{
			{
				Source: "http://src",
				Target: "http://tgt",
				Element: []conceptmap.Element{
					{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}},
					{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}},
				},
			},
		},
	}
	repo := newMockRepo()
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://example.org/dup",
		SourceCode:   "A",
		SourceSystem: "http://src",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	assert.Equal(t, 1, len(resp.Matches))
}

func TestTranslate_NoDeduplication_DifferentRelationships(t *testing.T) {
	// Same target code but different relationships should NOT be deduplicated
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "nodep-test",
		URL:          "http://example.org/nodep",
		Status:       "active",
		Group: []conceptmap.Group{
			{
				Source: "http://src",
				Target: "http://tgt",
				Element: []conceptmap.Element{
					{Code: "A", Target: []conceptmap.Target{
						{Code: "B", Relationship: "equivalent"},
						{Code: "B", Relationship: "related-to"},
					}},
				},
			},
		},
	}
	repo := newMockRepo()
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://example.org/nodep",
		SourceCode:   "A",
		SourceSystem: "http://src",
	})

	require.NoError(t, err)
	// Both matches should remain since relationships differ
	assert.Equal(t, 2, len(resp.Matches))
}

// =============================================================================
// Phase 1.3: Improved negative match messaging
// =============================================================================

func TestTranslate_AllNegative_ResultFalse_WithMessage(t *testing.T) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	// "old" maps to "BAD" with relationship "not-related-to"
	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://hl7.org/fhir/ConceptMap/101",
		SourceCode:   "old",
		SourceSystem: "http://hl7.org/fhir/address-use",
	})

	require.NoError(t, err)
	assert.False(t, resp.Result)
	assert.NotEmpty(t, resp.Matches)
	assert.Equal(t, "Only negative matches found", resp.Message)
}

func TestTranslate_MixedNegativePositive_ResultTrue_NoNegativeMessage(t *testing.T) {
	// ConceptMap with one positive and one negative mapping for same source
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "mixed-test",
		URL:          "http://example.org/mixed",
		Status:       "active",
		Group: []conceptmap.Group{
			{
				Source: "http://src",
				Target: "http://tgt",
				Element: []conceptmap.Element{
					{Code: "X", Target: []conceptmap.Target{
						{Code: "Y", Relationship: "equivalent"},
						{Code: "Z", Relationship: "not-related-to"},
					}},
				},
			},
		},
	}
	repo := newMockRepo()
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://example.org/mixed",
		SourceCode:   "X",
		SourceSystem: "http://src",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	assert.Empty(t, resp.Message)
}

// =============================================================================
// Phase 1.4: Source/Target scope-based ConceptMap resolution
// =============================================================================

func TestTranslate_ResolveBySourceScope(t *testing.T) {
	cm := &conceptmap.ConceptMap{
		ResourceType:   "ConceptMap",
		ID:             "scope-test",
		URL:            "http://example.org/scope-cm",
		Status:         "active",
		SourceScopeURI: "http://hl7.org/fhir/ValueSet/address-use",
		TargetScopeURI: "http://terminology.hl7.org/ValueSet/v3-AddressUse",
		Group: []conceptmap.Group{
			{
				Source: "http://hl7.org/fhir/address-use",
				Target: "http://terminology.hl7.org/CodeSystem/v3-AddressUse",
				Element: []conceptmap.Element{
					{Code: "home", Target: []conceptmap.Target{{Code: "H", Relationship: "equivalent"}}},
				},
			},
		},
	}
	repo := newMockRepo()
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	// Resolve by source scope without explicit URL
	resp, err := engine.Translate(context.Background(), Request{
		SourceScope:  "http://hl7.org/fhir/ValueSet/address-use",
		SourceCode:   "home",
		SourceSystem: "http://hl7.org/fhir/address-use",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	assert.Equal(t, "H", resp.Matches[0].Concept.Code)
}

// =============================================================================
// Phase 2.1: URL uniqueness - tested in service_test.go
// Phase 2.2: R4 param names - tested in handler_test.go
// Phase 2.3: Inline ConceptMap POST - tested in handler_test.go
// =============================================================================

// =============================================================================
// Phase 2.4: Pipe-delimited version in URL
// =============================================================================

func TestTranslate_PipeVersionInURL(t *testing.T) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "pipe-test",
		URL:          "http://example.org/cm/pipe",
		Version:      "2.0",
		Status:       "active",
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
	repo := newMockRepo()
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	// URL with pipe-delimited version
	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://example.org/cm/pipe|2.0",
		SourceCode:   "A",
		SourceSystem: "http://src",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	assert.Equal(t, "B", resp.Matches[0].Concept.Code)
}

// =============================================================================
// Phase 3.2: dependsOn/product in translate response
// =============================================================================

func TestTranslate_DependsOn_IncludedInResponse(t *testing.T) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "depends-test",
		URL:          "http://example.org/depends",
		Status:       "active",
		Group: []conceptmap.Group{
			{
				Source: "http://src",
				Target: "http://tgt",
				Element: []conceptmap.Element{
					{Code: "A", Target: []conceptmap.Target{
						{
							Code:         "B",
							Relationship: "equivalent",
							DependsOn: []conceptmap.DependsOn{
								{Attribute: "gender", ValueString: "male"},
							},
						},
					}},
				},
			},
		},
	}
	repo := newMockRepo()
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://example.org/depends",
		SourceCode:   "A",
		SourceSystem: "http://src",
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	require.Len(t, resp.Matches, 1)
	require.Len(t, resp.Matches[0].DependsOn, 1)
	assert.Equal(t, "gender", resp.Matches[0].DependsOn[0].Attribute)
	assert.Equal(t, "male", resp.Matches[0].DependsOn[0].ValueString)
}

// Engine emits a warning when dependency attribute doesn't match any target declaration, but filtering pass-through preserves a successful match.
func TestEngine_Translate_DependencyAttributeUnmatched_EmitsWarning(t *testing.T) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "depends-unmatched",
		URL:          "http://example.org/depends-unmatched",
		Status:       "active",
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
	repo := newMockRepo()
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://example.org/depends-unmatched",
		SourceCode:   "A",
		SourceSystem: "http://src",
		Dependencies: []DependencyInput{
			{Attribute: "weather", ValueString: "sunny"},
		},
	})

	require.NoError(t, err)
	assert.True(t, resp.Result, "match still succeeds: target declares no `weather` attribute → unconstrained")
	require.Len(t, resp.Matches, 1)
	require.Len(t, resp.Warnings, 1, "engine must emit exactly one warning for the unmatched attribute")
	assert.Equal(t, "not-supported", resp.Warnings[0].Code)
	assert.Contains(t, resp.Warnings[0].Diagnostics, "weather")
	assert.Contains(t, resp.Warnings[0].Diagnostics, "matched no target's dependsOn")
}

// Repeated unmatched attributes must not produce duplicate warning entries.
func TestEngine_Translate_DuplicateUnmatchedDependency_EmitsSingleWarning(t *testing.T) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "depends-dup",
		URL:          "http://example.org/depends-dup",
		Status:       "active",
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
	repo := newMockRepo()
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          cm.URL,
		SourceCode:   "A",
		SourceSystem: "http://src",
		Dependencies: []DependencyInput{
			{Attribute: "weather", ValueString: "sunny"},
			{Attribute: "weather", ValueString: "rainy"},
		},
	})

	require.NoError(t, err)
	require.Len(t, resp.Warnings, 1, "duplicate unmatched attribute must emit exactly one warning")
	assert.Contains(t, resp.Warnings[0].Diagnostics, "weather")
}

// Declared attributes must not produce warnings (negative regression guard).
func TestEngine_Translate_DependencyAttributeMatched_NoWarning(t *testing.T) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "depends-matched",
		URL:          "http://example.org/depends-matched",
		Status:       "active",
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
	repo := newMockRepo()
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://example.org/depends-matched",
		SourceCode:   "A",
		SourceSystem: "http://src",
		Dependencies: []DependencyInput{
			{Attribute: "language", ValueString: "en"},
		},
	})

	require.NoError(t, err)
	assert.True(t, resp.Result)
	assert.Empty(t, resp.Warnings, "declared attribute must not emit a warning")
}

// =============================================================================
// Existing tests
// =============================================================================

func TestReverseRelationship(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"equivalent", "equivalent"},
		{"related-to", "related-to"},
		{"not-related-to", "not-related-to"},
		{"source-is-narrower-than-target", "source-is-broader-than-target"},
		{"source-is-broader-than-target", "source-is-narrower-than-target"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, reverseRelationship(tt.input))
		})
	}
}
