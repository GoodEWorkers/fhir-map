package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/translate"
)

func TestIntegration_M3_5_TranslateBatch_PreservesOrderAndOneRoundTrip(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	store := NewMappingStore(pool)
	engine := translate.NewFlatEngine(store)
	ctx := context.Background()

	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "http://example.org/batch/test",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://src",
			Target: "http://tgt",
			Element: []conceptmap.Element{
				{Code: "A", Target: []conceptmap.Target{{Code: "AA", Relationship: "equivalent"}}},
				{Code: "B", Target: []conceptmap.Target{{Code: "BB", Relationship: "equivalent"}}},
				{Code: "C", Target: []conceptmap.Target{{Code: "CC1", Relationship: "equivalent"}, {Code: "CC2", Relationship: "source-is-narrower-than-target"}}},
				{Code: "D", Target: []conceptmap.Target{{Code: "DD", Relationship: "not-related-to"}}},
			},
		}},
	}
	_, err := repo.Create(ctx, cm)
	require.NoError(t, err)

	probes := []translate.BatchProbe{
		{SourceCode: "A", SourceSystem: "http://src"},
		{SourceCode: "B", SourceSystem: "http://src"},
		{SourceCode: "C", SourceSystem: "http://src"},
		{SourceCode: "D", SourceSystem: "http://src"}, // only a not-related-to match
		{SourceCode: "Z", SourceSystem: "http://src"}, // unknown — no matches
	}

	batchResp, err := engine.TranslateBatch(ctx, "http://example.org/batch/test", "", "", probes, "")
	require.NoError(t, err)
	require.Len(t, batchResp.Per, len(probes), "one per-probe response per input")

	assert.True(t, batchResp.Per[0].Result, "A -> AA should be positive")
	require.Len(t, batchResp.Per[0].Matches, 1)
	assert.Equal(t, "AA", batchResp.Per[0].Matches[0].Concept.Code)

	assert.True(t, batchResp.Per[1].Result, "B -> BB should be positive")
	require.Len(t, batchResp.Per[1].Matches, 1)
	assert.Equal(t, "BB", batchResp.Per[1].Matches[0].Concept.Code)

	assert.True(t, batchResp.Per[2].Result, "C -> {CC1, CC2} should be positive")
	require.Len(t, batchResp.Per[2].Matches, 2)
	assert.Equal(t, "CC1", batchResp.Per[2].Matches[0].Concept.Code)
	assert.Equal(t, "CC2", batchResp.Per[2].Matches[1].Concept.Code)

	assert.False(t, batchResp.Per[3].Result, "D has only not-related-to matches; result must be false")
	assert.Equal(t, "Only negative matches found", batchResp.Per[3].Message)

	assert.False(t, batchResp.Per[4].Result, "Z has no matches at all")
	assert.Empty(t, batchResp.Per[4].Matches)

	assert.True(t, batchResp.Overall, "batch overall is the OR of per-probe results")
}

// TestIntegration_M3_5_BatchMatchesSequential verifies that per-probe responses from
// TranslateBatch match what individual $translate calls would produce (diff oracle).
func TestIntegration_M3_5_BatchMatchesSequential(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	store := NewMappingStore(pool)
	engine := translate.NewFlatEngine(store)
	ctx := context.Background()

	// Build a 200-element map so the batch SQL does real work.
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "http://example.org/batch/200",
		Status:       "active",
		Group:        []conceptmap.Group{{Source: "http://src", Target: "http://tgt"}},
	}
	for i := 0; i < 200; i++ {
		cm.Group[0].Element = append(cm.Group[0].Element, conceptmap.Element{
			Code: fmt.Sprintf("S%03d", i),
			Target: []conceptmap.Target{
				{Code: fmt.Sprintf("T%03d", i), Relationship: "equivalent"},
			},
		})
	}
	_, err := repo.Create(ctx, cm)
	require.NoError(t, err)

	// Build 50 probes — mix of hits and misses, deliberately out of order.
	probes := make([]translate.BatchProbe, 0, 10)
	for _, idx := range []int{0, 17, 23, 199, 100, 42, 1000 /*miss*/, 5, 88, 7} {
		probes = append(probes,
			translate.BatchProbe{SourceCode: fmt.Sprintf("S%03d", idx), SourceSystem: "http://src"})
	}

	batchResp, err := engine.TranslateBatch(ctx, "http://example.org/batch/200", "", "", probes, "")
	require.NoError(t, err)
	require.Len(t, batchResp.Per, len(probes))

	for i, p := range probes {
		single, err := engine.Translate(ctx, translate.Request{
			URL:          "http://example.org/batch/200",
			SourceCode:   p.SourceCode,
			SourceSystem: p.SourceSystem,
		})
		require.NoError(t, err)
		assert.Equalf(t, single.Result, batchResp.Per[i].Result,
			"probe[%d] %q result diverged between batch and single", i, p.SourceCode)
		assert.Equalf(t, len(single.Matches), len(batchResp.Per[i].Matches),
			"probe[%d] %q match count diverged", i, p.SourceCode)
		for j := range single.Matches {
			assert.Equal(t, single.Matches[j].Concept.Code, batchResp.Per[i].Matches[j].Concept.Code)
		}
	}
}

// Compile-time guard: MappingStore satisfies translate.BatchFlatStore.
var _ translate.BatchFlatStore = (*MappingStore)(nil)
