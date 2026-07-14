package transform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

// TestEngine_Translate_ContainedConceptMap_HashPrefixedID verifies that translate() resolves contained ConceptMaps when the ID includes the leading '#'.
func TestEngine_Translate_ContainedConceptMap_HashPrefixedID(t *testing.T) {
	sm := mapWithTransform("translate", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "#cm"}, {ValueString: "code"},
	}, true)
	sm.Contained = []*conceptmap.ConceptMap{{
		ResourceType: "ConceptMap",
		ID:           "#cm", // stored with the leading '#'
		URL:          "http://example.org/ConceptMap/cm",
		Group: []conceptmap.Group{{
			Target:  "http://loinc.org",
			Element: []conceptmap.Element{{Code: "CRP", Target: []conceptmap.Target{{Code: "1988-5", Relationship: "equivalent"}}}},
		}},
	}}
	got, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "CRP"})
	require.NoError(t, err)
	assert.Equal(t, "1988-5", got.(map[string]any)["out"], "contained ConceptMap resolved via #-fragment")
}
