package transform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

// A copy rule firing once per repeating source match writes to a bare target
// element each firing and must preserve ALL items (HL7v2 OBR/OBX), not just
// the last. Engine-level proof that the target_path promote-on-repeat reaches
// the real $transform path.
func TestEngine_RepeatedBareCopy_PreservesAllSegments(t *testing.T) {
	src := map[string]any{
		"segs": []any{"OBR|0001", "OBR|0002", "OBR|0003"},
		"hdr":  `^~\&`,
	}
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap", URL: "http://example.org/seg", Name: "Seg", Status: "active",
		Group: []structuremap.Group{{
			Name:  "g",
			Input: []structuremap.Input{{Name: "src", Mode: "source"}, {Name: "tgt", Mode: "target"}},
			Rule: []structuremap.Rule{
				{
					Name:   "hdr",
					Source: []structuremap.Source{{Context: "src", Element: "hdr", Variable: "h"}},
					Target: []structuremap.Target{{Context: "tgt", Element: "MSH-1", Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "h"}}}},
				},
				{
					Name:   "obr",
					Source: []structuremap.Source{{Context: "src", Element: "segs", Variable: "s"}},
					Target: []structuremap.Target{{Context: "tgt", Element: "OBR", Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "s"}}}},
				},
			},
		}},
	}
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out := got.(map[string]any)
	assert.Equal(t, []any{"OBR|0001", "OBR|0002", "OBR|0003"}, out["OBR"], "all OBR segments preserved")
	assert.Equal(t, `^~\&`, out["MSH-1"], "single-write field stays scalar")
}

// create('X') must stamp resourceType when X is a FHIR resource type, so
// mid-bundle resources are valid FHIR.
func TestEngine_Create_StampsResourceTypeForResource(t *testing.T) {
	sm := mapWithTransform("create", []structuremap.Parameter{{ValueString: "Observation"}}, false)
	got, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "x"})
	require.NoError(t, err)
	out, ok := got.(map[string]any)["out"].(map[string]any)
	require.True(t, ok, "created target should be a map")
	assert.Equal(t, "Observation", out["resourceType"], "resource create must stamp resourceType")
}

// create('Reference') is a complex datatype, NOT a resource: it must NOT
// carry resourceType.
func TestEngine_Create_DoesNotStampResourceTypeForDatatype(t *testing.T) {
	sm := mapWithTransform("create", []structuremap.Parameter{{ValueString: "Reference"}}, false)
	got, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "x"})
	require.NoError(t, err)
	out, ok := got.(map[string]any)["out"].(map[string]any)
	require.True(t, ok, "created target should be a map")
	_, has := out["resourceType"]
	assert.False(t, has, "datatype create must NOT stamp resourceType")
}
