package transform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

// Fail closed: an unresolved import or translate must error, not silently degrade
// (the reference engine 500s; fhir-map must not return a degraded 200).
func TestEngine_FailClosed_UnresolvedImport(t *testing.T) {
	mapA := &structuremap.StructureMap{
		ResourceType: "StructureMap", URL: "http://example.org/a", Name: "A", Status: "active",
		Import: []string{"http://example.org/does-not-exist"},
		Group: []structuremap.Group{{
			Name:  "main",
			Input: []structuremap.Input{{Name: "src", Mode: "source"}, {Name: "tgt", Mode: "target"}},
			Rule: []structuremap.Rule{{
				Name:   "r",
				Source: []structuremap.Source{{Context: "src", Element: "value", Variable: "v"}},
				Target: []structuremap.Target{{Context: "tgt", Element: "out", Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "v"}}}},
			}},
		}},
	}
	resolver := &fakeResolver{maps: map[string]*structuremap.StructureMap{}} // empty → ErrNotFound
	_, err := NewEngineWithResolver(nil, resolver).Transform(context.Background(), mapA, map[string]any{"value": "x"})
	require.Error(t, err, "unresolved import must fail closed, not silently ignore the import")
}

// A translate with no resolvable ConceptMap must error rather than silently emit an un-coded result.
func TestEngine_FailClosed_UnresolvedTranslate(t *testing.T) {
	sm := mapWithTransform("translate", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "#no-such-conceptmap"}, {ValueString: "code"},
	}, true)
	_, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "A"})
	require.Error(t, err, "translate with no resolvable ConceptMap must fail closed")
	assert.Contains(t, err.Error(), "translate", "error should name the failing transform")
}
