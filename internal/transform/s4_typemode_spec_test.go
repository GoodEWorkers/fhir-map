package transform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

// TestEngine_ImportMerge_SameNamedRuleEnrichesBaseResource verifies that imported maps' same-named rules merge as sub-rules that enrich the base resource.
func TestEngine_ImportMerge_SameNamedRuleEnrichesBaseResource(t *testing.T) {
	src := map[string]any{"items": []any{map[string]any{"code": "A", "extra": "X"}}}

	// Imported map: SAME group name+signature, SAME rule name "makeObs"; adds a
	// sub-rule "enrich" that writes o.enriched. Base-wins means its (absent here)
	// rule-level target is irrelevant; the sub-rule is what merges in.
	mapB := &structuremap.StructureMap{
		ResourceType: "StructureMap", URL: "http://example.org/enrich", Name: "Enrich", Status: "active",
		Group: []structuremap.Group{{
			Name: "main", TypeMode: "types",
			Input: []structuremap.Input{{Name: "src", Type: "Msg", Mode: "source"}, {Name: "tgt", Type: "Bag", Mode: "target"}},
			Rule: []structuremap.Rule{{
				Name:   "makeObs",
				Source: []structuremap.Source{{Context: "src", Element: "items", Variable: "i"}},
				Rule: []structuremap.Rule{{
					Name:   "enrich",
					Source: []structuremap.Source{{Context: "i", Element: "extra", Variable: "e"}},
					Target: []structuremap.Target{{Context: "o", Element: "enriched", Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "e"}}}},
				}},
			}},
		}},
	}

	// Entry map: imports B; rule "makeObs" creates the resource (o) and a base
	// sub-rule sets o.code. After merge, "makeObs" also runs B's "enrich".
	mapA := &structuremap.StructureMap{
		ResourceType: "StructureMap", URL: "http://example.org/entry", Name: "Entry", Status: "active",
		Import: []string{"http://example.org/enrich"},
		Group: []structuremap.Group{{
			Name: "main", TypeMode: "types",
			Input: []structuremap.Input{{Name: "src", Type: "Msg", Mode: "source"}, {Name: "tgt", Type: "Bag", Mode: "target"}},
			Rule: []structuremap.Rule{{
				Name:   "makeObs",
				Source: []structuremap.Source{{Context: "src", Element: "items", Variable: "i"}},
				Target: []structuremap.Target{{Context: "tgt", Element: "obs", Variable: "o", Transform: "create",
					Parameter: []structuremap.Parameter{{ValueString: "Observation"}}}},
				Rule: []structuremap.Rule{{
					Name:   "base",
					Source: []structuremap.Source{{Context: "i", Element: "code", Variable: "c"}},
					Target: []structuremap.Target{{Context: "o", Element: "code", Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "c"}}}},
				}},
			}},
		}},
	}

	resolver := &fakeResolver{maps: map[string]*structuremap.StructureMap{"http://example.org/enrich": mapB}}
	got, err := NewEngineWithResolver(nil, resolver).Transform(context.Background(), mapA, src)
	require.NoError(t, err)
	obs, ok := got.(map[string]any)["obs"].(map[string]any)
	require.True(t, ok, "exactly one merged Observation, not duplicated")
	assert.Equal(t, "Observation", obs["resourceType"], "S3: created resource is stamped")
	assert.Equal(t, "A", obs["code"], "base rule contribution")
	assert.Equal(t, "X", obs["enriched"], "imported same-named rule's sub-rule merged in and enriched the SAME resource")
}
