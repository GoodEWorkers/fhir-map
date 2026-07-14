package translate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// Reverse translate must honour the TargetSystem filter the same way forward translation does,
// otherwise reverse lookups against multi-group ConceptMaps return matches from groups the caller never asked for.
func TestTranslateReverse_AppliesTargetSystemFilter(t *testing.T) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "reverse-multigroup",
		URL:          "http://example.org/cm/reverse-multigroup",
		Status:       "active",
		Group: []conceptmap.Group{
			{
				Source: "http://src",
				Target: "http://tgtA",
				Element: []conceptmap.Element{
					{Code: "alpha", Target: []conceptmap.Target{{Code: "X", Relationship: "equivalent"}}},
				},
			},
			{
				Source: "http://src",
				Target: "http://tgtB",
				Element: []conceptmap.Element{
					{Code: "beta", Target: []conceptmap.Target{{Code: "X", Relationship: "equivalent"}}},
				},
			},
		},
	}
	repo := newMockRepo()
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	respAll, err := engine.Translate(context.Background(), Request{
		URL:        "http://example.org/cm/reverse-multigroup",
		TargetCode: "X",
	})
	require.NoError(t, err)
	require.Len(t, respAll.Matches, 2)

	respFiltered, err := engine.Translate(context.Background(), Request{
		URL:          "http://example.org/cm/reverse-multigroup",
		TargetCode:   "X",
		TargetSystem: "http://tgtA",
	})
	require.NoError(t, err)
	require.Len(t, respFiltered.Matches, 1)
	assert.Equal(t, "alpha", respFiltered.Matches[0].Concept.Code,
		"only the alpha->X mapping should survive the tgtA target-system filter")
}

// DependsOn / Product must preserve value[x]; previously the engine
// only emitted ValueString, silently dropping ValueCoding and ValueCode.
func TestTranslate_DependsOn_ValueCoding_Preserved(t *testing.T) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "depends-coding",
		URL:          "http://example.org/cm/depends-coding",
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
								{Attribute: "ex3", ValueCoding: &fhir.Coding{
									System: "http://example.org/fhir/property-value/example",
									Code:   "DE-1.1",
								}},
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
		URL:          "http://example.org/cm/depends-coding",
		SourceCode:   "A",
		SourceSystem: "http://src",
	})
	require.NoError(t, err)
	require.Len(t, resp.Matches, 1)
	require.Len(t, resp.Matches[0].DependsOn, 1)
	dep := resp.Matches[0].DependsOn[0]
	assert.Equal(t, "ex3", dep.Attribute)
	if assert.NotNil(t, dep.ValueCoding, "ValueCoding must be preserved on the response, not dropped to ValueString") {
		assert.Equal(t, "http://example.org/fhir/property-value/example", dep.ValueCoding.System)
		assert.Equal(t, "DE-1.1", dep.ValueCoding.Code)
	}
}

func TestTranslate_DependsOn_ValueCode_Preserved(t *testing.T) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "depends-code",
		URL:          "http://example.org/cm/depends-code",
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
								{Attribute: "sex", ValueCode: "male"},
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
		URL:          "http://example.org/cm/depends-code",
		SourceCode:   "A",
		SourceSystem: "http://src",
	})
	require.NoError(t, err)
	require.Len(t, resp.Matches[0].DependsOn, 1)
	assert.Equal(t, "male", resp.Matches[0].DependsOn[0].ValueCode)
}

// When an element is not mapped and the group has unmapped.mode=other-map,
// the engine follows the chain to the named ConceptMap.
func TestTranslate_Unmapped_OtherMap_Follows(t *testing.T) {
	primary := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "primary",
		URL:          "http://example.org/cm/primary",
		Status:       "active",
		Group: []conceptmap.Group{
			{
				Source: "http://src",
				Target: "http://tgt",
				Element: []conceptmap.Element{
					{Code: "known", Target: []conceptmap.Target{{Code: "KNOWN", Relationship: "equivalent"}}},
				},
				Unmapped: &conceptmap.Unmapped{
					Mode:     "other-map",
					OtherMap: "http://example.org/cm/fallback",
				},
			},
		},
	}
	fallback := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "fallback",
		URL:          "http://example.org/cm/fallback",
		Status:       "active",
		Group: []conceptmap.Group{
			{
				Source: "http://src",
				Target: "http://tgt",
				Element: []conceptmap.Element{
					{Code: "missing-in-primary", Target: []conceptmap.Target{
						{Code: "FALLBACK-CODE", Relationship: "equivalent"},
					}},
				},
			},
		},
	}
	repo := newMockRepo()
	repo.Create(context.Background(), primary)
	repo.Create(context.Background(), fallback)
	engine := NewEngine(repo)

	resp, err := engine.Translate(context.Background(), Request{
		URL:          "http://example.org/cm/primary",
		SourceCode:   "missing-in-primary",
		SourceSystem: "http://src",
	})
	require.NoError(t, err)
	assert.True(t, resp.Result, "other-map fallback must produce a positive result")
	require.NotEmpty(t, resp.Matches)
	assert.Equal(t, "FALLBACK-CODE", resp.Matches[0].Concept.Code)
	assert.Equal(t, "http://example.org/cm/fallback", resp.Matches[0].OriginMap)
}

// Cyclic other-map references must not loop forever; the depth cap surfaces as an explicit error instead of hanging.
func TestTranslate_Unmapped_OtherMap_CycleCappedAtFive(t *testing.T) {
	a := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "a",
		URL:          "http://example.org/cm/a",
		Status:       "active",
		Group: []conceptmap.Group{
			{Source: "http://src", Target: "http://tgt",
				Element:  []conceptmap.Element{{Code: "only-a", Target: []conceptmap.Target{{Code: "AA", Relationship: "equivalent"}}}},
				Unmapped: &conceptmap.Unmapped{Mode: "other-map", OtherMap: "http://example.org/cm/b"}},
		},
	}
	b := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "b",
		URL:          "http://example.org/cm/b",
		Status:       "active",
		Group: []conceptmap.Group{
			{Source: "http://src", Target: "http://tgt",
				Element:  []conceptmap.Element{{Code: "only-b", Target: []conceptmap.Target{{Code: "BB", Relationship: "equivalent"}}}},
				Unmapped: &conceptmap.Unmapped{Mode: "other-map", OtherMap: "http://example.org/cm/a"}},
		},
	}
	repo := newMockRepo()
	repo.Create(context.Background(), a)
	repo.Create(context.Background(), b)
	engine := NewEngine(repo)

	_, err := engine.Translate(context.Background(), Request{
		URL:          "http://example.org/cm/a",
		SourceCode:   "nope",
		SourceSystem: "http://src",
	})
	require.Error(t, err, "cyclic other-map chain must surface an error, not hang")
	assert.Contains(t, err.Error(), "other-map")
}
