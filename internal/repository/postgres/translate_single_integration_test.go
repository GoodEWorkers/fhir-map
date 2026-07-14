package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/translate"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// Tests all FlatEngine $translate branches against real SQL (forward/reverse/unmapped/other-map),
// not just the in-memory stub used by batch and shadow-diff.
func TestIntegration_FlatEngine_TranslateSingle_ProductionPath(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	store := NewMappingStore(pool)
	engine := translate.NewFlatEngine(store)
	ctx := context.Background()

	mustCreate := func(cm *conceptmap.ConceptMap) {
		t.Helper()
		_, err := repo.Create(ctx, cm)
		require.NoError(t, err)
	}

	// Direct + reverse map: A -> X (equivalent).
	mustCreate(&conceptmap.ConceptMap{
		ResourceType: "ConceptMap", URL: "http://maps/direct", Status: "active",
		Group: []conceptmap.Group{{
			Source: "http://src", Target: "http://tgt",
			Element: []conceptmap.Element{
				{Code: "A", Target: []conceptmap.Target{{Code: "X", Relationship: "equivalent"}}},
			},
		}},
	})

	// Unmapped strategies on their own maps.
	mustCreate(&conceptmap.ConceptMap{
		ResourceType: "ConceptMap", URL: "http://maps/fixed", Status: "active",
		Group: []conceptmap.Group{{
			Source: "http://src", Target: "http://tgt",
			Unmapped: &conceptmap.Unmapped{Mode: "fixed", Code: "UNK", Display: "Unknown", Relationship: "equivalent"},
		}},
	})
	mustCreate(&conceptmap.ConceptMap{
		ResourceType: "ConceptMap", URL: "http://maps/passthru", Status: "active",
		Group: []conceptmap.Group{{
			Source: "http://src", Target: "http://tgt",
			Unmapped: &conceptmap.Unmapped{Mode: "use-source-code", Relationship: "equivalent"},
		}},
	})

	// other-map chain: A unmapped on /chain-a, resolved by /chain-b which holds the row.
	mustCreate(&conceptmap.ConceptMap{
		ResourceType: "ConceptMap", URL: "http://maps/chain-b", Status: "active",
		Group: []conceptmap.Group{{
			Source: "http://src", Target: "http://tgt",
			Element: []conceptmap.Element{
				{Code: "A", Target: []conceptmap.Target{{Code: "Z", Relationship: "equivalent"}}},
			},
		}},
	})
	mustCreate(&conceptmap.ConceptMap{
		ResourceType: "ConceptMap", URL: "http://maps/chain-a", Status: "active",
		Group: []conceptmap.Group{{
			Source: "http://src", Target: "http://tgt",
			Unmapped: &conceptmap.Unmapped{Mode: "other-map", OtherMap: "http://maps/chain-b"},
		}},
	})

	t.Run("forward match", func(t *testing.T) {
		resp, err := engine.Translate(ctx, translate.Request{
			URL: "http://maps/direct", SourceSystem: "http://src", SourceCode: "A",
		})
		require.NoError(t, err)
		require.True(t, resp.Result)
		require.Len(t, resp.Matches, 1)
		assert.Equal(t, "X", resp.Matches[0].Concept.Code)
	})

	t.Run("reverse match", func(t *testing.T) {
		resp, err := engine.Translate(ctx, translate.Request{
			URL:          "http://maps/direct",
			TargetCoding: &fhir.Coding{System: "http://tgt", Code: "X"},
		})
		require.NoError(t, err)
		require.True(t, resp.Result)
		require.Len(t, resp.Matches, 1)
		assert.Equal(t, "A", resp.Matches[0].Concept.Code, "reverse resolves target X back to source A")
	})

	t.Run("no match, no unmapped", func(t *testing.T) {
		resp, err := engine.Translate(ctx, translate.Request{
			URL: "http://maps/direct", SourceSystem: "http://src", SourceCode: "nope",
		})
		require.NoError(t, err)
		assert.False(t, resp.Result)
		assert.Empty(t, resp.Matches)
	})

	t.Run("unmapped fixed", func(t *testing.T) {
		resp, err := engine.Translate(ctx, translate.Request{
			URL: "http://maps/fixed", SourceSystem: "http://src", SourceCode: "anything",
		})
		require.NoError(t, err)
		require.True(t, resp.Result)
		require.Len(t, resp.Matches, 1)
		assert.Equal(t, "UNK", resp.Matches[0].Concept.Code)
	})

	t.Run("unmapped use-source-code", func(t *testing.T) {
		resp, err := engine.Translate(ctx, translate.Request{
			URL: "http://maps/passthru", SourceSystem: "http://src", SourceCode: "echo-me",
		})
		require.NoError(t, err)
		require.True(t, resp.Result)
		require.Len(t, resp.Matches, 1)
		assert.Equal(t, "echo-me", resp.Matches[0].Concept.Code)
	})

	t.Run("unmapped other-map resolves via second map", func(t *testing.T) {
		resp, err := engine.Translate(ctx, translate.Request{
			URL: "http://maps/chain-a", SourceSystem: "http://src", SourceCode: "A",
		})
		require.NoError(t, err)
		require.True(t, resp.Result)
		require.Len(t, resp.Matches, 1)
		assert.Equal(t, "Z", resp.Matches[0].Concept.Code, "chain-a delegates to chain-b which holds A->Z")
	})
}
