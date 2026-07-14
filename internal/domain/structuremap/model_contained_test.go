package structuremap

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
)

func TestStructureMap_ContainedConceptMap_JSONRoundTrip(t *testing.T) {
	in := &StructureMap{
		ResourceType: "StructureMap", URL: "http://example.org/sm", Name: "SM", Status: "active",
		Group: []Group{{Name: "main"}},
		Contained: []*conceptmap.ConceptMap{{
			ResourceType: "ConceptMap", ID: "cm", URL: "#cm",
			Group: []conceptmap.Group{{Target: "http://loinc.org",
				Element: []conceptmap.Element{{Code: "CRP", Target: []conceptmap.Target{{Code: "1988-5", Relationship: "equivalent"}}}}}},
		}},
	}

	raw, err := json.Marshal(in)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"contained"`, "contained array emitted")
	assert.Contains(t, string(raw), `"#cm"`)

	var out StructureMap
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Len(t, out.Contained, 1, "contained ConceptMap survives round-trip")
	assert.Equal(t, "#cm", out.Contained[0].URL)
	assert.Equal(t, "1988-5", out.Contained[0].Group[0].Element[0].Target[0].Code)
	assert.Equal(t, "http://example.org/sm", out.URL)
	require.Len(t, out.Group, 1)
}

func TestStructureMap_NoContained_OmitsKeyAndStaysNil(t *testing.T) {
	in := &StructureMap{ResourceType: "StructureMap", URL: "u", Name: "N", Status: "active", Group: []Group{{Name: "g"}}}
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(raw), `"contained"`), "no contained key when empty (byte-compat)")

	var out StructureMap
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Nil(t, out.Contained)
}

func TestStructureMap_Contained_IgnoresNonConceptMap(t *testing.T) {
	js := `{"resourceType":"StructureMap","url":"u","name":"N","status":"active",
		"group":[{"name":"g"}],
		"contained":[{"resourceType":"Patient","id":"p"},{"resourceType":"ConceptMap","id":"cm","url":"#cm"}]}`
	var out StructureMap
	require.NoError(t, json.Unmarshal([]byte(js), &out))
	require.Len(t, out.Contained, 1, "only the ConceptMap is captured")
	assert.Equal(t, "#cm", out.Contained[0].URL)
}
