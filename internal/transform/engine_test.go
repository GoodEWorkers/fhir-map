package transform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

// patientTypedMap returns a minimal StructureMap with a typed "Patient" source input, shared by sentinel tests.
func patientTypedMap() *structuremap.StructureMap {
	return &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/test/sentinel",
		Name:         "SentinelTest",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "Map",
			Input: []structuremap.Input{
				{Name: "src", Type: "Patient", Mode: "source"},
				{Name: "tgt", Type: "Patient", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "passthrough",
				Source: []structuremap.Source{{Context: "src", Element: "id", Variable: "v"}},
				Target: []structuremap.Target{{Context: "tgt", Element: "id", Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "v"}}}},
			}},
		}},
	}
}

// TestEngine_Transform_CanceledContext_ReturnsErrTransformCanceled guards context cancellation:
// Go contexts are cooperative, so without explicit checkpoints a tight engine loop would ignore
// the deadline even on a structurally valid map.
func TestEngine_Transform_CanceledContext_ReturnsErrTransformCanceled(t *testing.T) {
	eng := NewEngine(nil)
	sm := patientTypedMap()
	source := map[string]any{"resourceType": "Patient", "id": "p-1"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before execution reaches the rule loop

	_, err := eng.Transform(ctx, sm, source)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrTransformCanceled),
		"expected ErrTransformCanceled, got: %v", err)
	require.True(t, errors.Is(err, context.Canceled),
		"ErrTransformCanceled must wrap the underlying context error, got: %v", err)
}

// TestEngine_Transform_InputTypeMismatch_ReturnsErrInputTypeMismatch covers the case where the
// source resourceType does not match the StructureMap's declared source input type.
func TestEngine_Transform_InputTypeMismatch_ReturnsErrInputTypeMismatch(t *testing.T) {
	eng := NewEngine(nil)
	sm := patientTypedMap()

	source := map[string]any{
		"resourceType": "Observation",
		"id":           "obs-1",
		"status":       "final",
	}

	_, err := eng.Transform(context.Background(), sm, source)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputTypeMismatch),
		"expected ErrInputTypeMismatch, got: %v", err)
}

// TestEngine_Transform_InputTypeMismatch_SameType_NoError confirms that when
// the resourceType matches the declared type, no ErrInputTypeMismatch is raised.
func TestEngine_Transform_InputTypeMismatch_SameType_NoError(t *testing.T) {
	eng := NewEngine(nil)
	sm := patientTypedMap()

	source := map[string]any{
		"resourceType": "Patient",
		"id":           "pat-1",
		"active":       true,
	}

	_, err := eng.Transform(context.Background(), sm, source)
	// Previously asserted only that specific sentinels were absent, which masked unrelated failures.
	require.NoError(t, err, "same-type, non-empty input must not return any engine error")
}

// TestEngine_Transform_AbsentResourceType_NoMismatchError confirms that absent resourceType skips
// the type check — the payload may be a non-resource value like a CodeableConcept.
func TestEngine_Transform_AbsentResourceType_NoMismatchError(t *testing.T) {
	eng := NewEngine(nil)
	sm := patientTypedMap()

	source := map[string]any{
		"coding": []any{map[string]any{"system": "http://loinc.org", "code": "1234-5"}},
		"text":   "some code",
	}

	_, err := eng.Transform(context.Background(), sm, source)
	require.False(t, errors.Is(err, ErrInputTypeMismatch),
		"absent resourceType must not trigger ErrInputTypeMismatch")
}

// TestEngine_Transform_EmptyResource_ReturnsErrInputInvalid covers an input with no keys
// (or only "resourceType"), which must fail with ErrInputInvalid.
func TestEngine_Transform_EmptyResource_ReturnsErrInputInvalid(t *testing.T) {
	eng := NewEngine(nil)
	sm := patientTypedMap()

	source := map[string]any{
		"resourceType": "Patient",
	}

	_, err := eng.Transform(context.Background(), sm, source)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputInvalid),
		"empty resource must return ErrInputInvalid, got: %v", err)
}

// TestEngine_Transform_BareEmptyObject_ReturnsErrInputInvalid confirms that
// a completely empty map {} also triggers ErrInputInvalid.
func TestEngine_Transform_BareEmptyObject_ReturnsErrInputInvalid(t *testing.T) {
	eng := NewEngine(nil)
	sm := patientTypedMap()

	source := map[string]any{}

	_, err := eng.Transform(context.Background(), sm, source)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputInvalid),
		"bare empty object must return ErrInputInvalid, got: %v", err)
}

// TestEngine_Transform_ExtendsMissingParent_ReturnsErrMapNotFound: naming a non-existent extends
// parent must fail with ErrMapNotFound; the previous silent-no-op hid invalid StructureMaps.
func TestEngine_Transform_ExtendsMissingParent_ReturnsErrMapNotFound(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "ExtendsMissing",
		Status:       "active",
		Group: []structuremap.Group{{
			Name:    "leaf",
			Extends: "NonExistentParent",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "copy",
				Source: []structuremap.Source{{Context: "src", Element: "id", Variable: "v"}},
				Target: []structuremap.Target{{Context: "tgt", Element: "id", Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "v"}}}},
			}},
		}},
	}
	source := map[string]any{"resourceType": "Patient", "id": "x", "active": true}
	_, err := NewEngine(nil).Transform(context.Background(), sm, source)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMapNotFound),
		"extends with missing parent must return ErrMapNotFound, got: %v", err)
	assert.Contains(t, err.Error(), "NonExistentParent")
}

// TestEngine_Transform_ExtendsParentTypeMismatch_ReturnsErrInputTypeMismatch: parent-type vs
// leaf-type mismatch must return ErrInputTypeMismatch naming both groups and both types.
func TestEngine_Transform_ExtendsParentTypeMismatch_ReturnsErrInputTypeMismatch(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "ExtendsTypeMismatch",
		Status:       "active",
		Group: []structuremap.Group{
			{
				Name: "parentG",
				Input: []structuremap.Input{
					{Name: "src", Type: "Patient", Mode: "source"},
					{Name: "tgt", Mode: "target"},
				},
				Rule: []structuremap.Rule{{
					Name:   "pass",
					Source: []structuremap.Source{{Context: "src"}},
					Target: []structuremap.Target{{Context: "tgt"}},
				}},
			},
			{
				Name:    "leafG",
				Extends: "parentG",
				Input: []structuremap.Input{
					{Name: "src", Type: "Observation", Mode: "source"},
					{Name: "tgt", Mode: "target"},
				},
				Rule: []structuremap.Rule{{
					Name:   "pass",
					Source: []structuremap.Source{{Context: "src"}},
					Target: []structuremap.Target{{Context: "tgt"}},
				}},
			},
		},
	}
	// leafG is NOT extended by anything, so entryGroup returns leafG.
	source := map[string]any{"resourceType": "Observation", "id": "obs-1", "status": "final"}
	_, err := NewEngine(nil).Transform(context.Background(), sm, source)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputTypeMismatch),
		"extends parent-type mismatch must return ErrInputTypeMismatch, got: %v", err)
	assert.Contains(t, err.Error(), "leafG")
	assert.Contains(t, err.Error(), "parentG")
	assert.Contains(t, err.Error(), "Observation")
	assert.Contains(t, err.Error(), "Patient")
}

// TestEngine_Transform_DefaultValueChoiceType_FillsBinding: the wire key is `defaultValueString`;
// UnmarshalJSON must capture it into DefaultValue.
func TestEngine_Transform_DefaultValueChoiceType_FillsBinding(t *testing.T) {
	smJSON := `{
		"resourceType": "StructureMap",
		"name": "DefaultValueChoiceType",
		"status": "active",
		"group": [{
			"name": "g",
			"input": [
				{"name": "src", "mode": "source"},
				{"name": "tgt", "mode": "target"}
			],
			"rule": [{
				"name": "def",
				"source": [{"context": "src", "element": "missing", "variable": "v",
				            "defaultValueString": "FALLBACK"}],
				"target": [{"context": "tgt", "element": "out", "transform": "copy",
				            "parameter": [{"valueId": "v"}]}]
			}]
		}]
	}`
	var sm structuremap.StructureMap
	require.NoError(t, json.Unmarshal([]byte(smJSON), &sm))

	source := map[string]any{"resourceType": "Patient", "id": "x", "active": true}
	result, err := NewEngine(nil).Transform(context.Background(), &sm, source)
	require.NoError(t, err)
	assert.Equal(t, "FALLBACK", result.(map[string]any)["out"],
		"defaultValueString wire key must fill binding via UnmarshalJSON path")
}

// TestEngine_Transform_DefaultValueBareKey_FillsBinding: bare `defaultValue` key (back-compat path).
func TestEngine_Transform_DefaultValueBareKey_FillsBinding(t *testing.T) {
	smJSON := `{
		"resourceType": "StructureMap",
		"name": "DefaultValueBareKey",
		"status": "active",
		"group": [{
			"name": "g",
			"input": [
				{"name": "src", "mode": "source"},
				{"name": "tgt", "mode": "target"}
			],
			"rule": [{
				"name": "def",
				"source": [{"context": "src", "element": "missing", "variable": "v",
				            "defaultValue": "FALLBACK"}],
				"target": [{"context": "tgt", "element": "out", "transform": "copy",
				            "parameter": [{"valueId": "v"}]}]
			}]
		}]
	}`
	var sm structuremap.StructureMap
	require.NoError(t, json.Unmarshal([]byte(smJSON), &sm))

	source := map[string]any{"resourceType": "Patient", "id": "x", "active": true}
	result, err := NewEngine(nil).Transform(context.Background(), &sm, source)
	require.NoError(t, err)
	assert.Equal(t, "FALLBACK", result.(map[string]any)["out"],
		"bare defaultValue key must fill binding via back-compat UnmarshalJSON path")
}

// fakeResolver is a test-only MapResolver backed by an in-memory map.
type fakeResolver struct {
	maps map[string]*structuremap.StructureMap
}

func (f *fakeResolver) FindByURL(_ context.Context, url, _ string) (*structuremap.StructureMap, error) {
	if sm, ok := f.maps[url]; ok {
		return sm, nil
	}
	// Use the real sentinel so errors.Is correctly distinguishes not-found from transient errors.
	return nil, structuremap.ErrNotFound
}

// TestEngine_Transform_ImportsResolveGroupAcrossMaps: imports must resolve a group from an imported map.
func TestEngine_Transform_ImportsResolveGroupAcrossMaps(t *testing.T) {
	mapB := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/mapB",
		Name:         "MapB",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "copyId",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "cp",
				Source: []structuremap.Source{{Context: "src", Element: "id", Variable: "v"}},
				Target: []structuremap.Target{{Context: "tgt", Element: "id", Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "v"}}}},
			}},
		}},
	}

	mapA := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/mapA",
		Name:         "MapA",
		Status:       "active",
		Import:       []string{"http://example.org/mapB"},
		Group: []structuremap.Group{{
			Name: "entry",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "delegate",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
				Dependent: []structuremap.Dependent{{
					Name:      "copyId",
					Parameter: []structuremap.Parameter{{ValueID: "src"}, {ValueID: "tgt"}},
				}},
			}},
		}},
	}

	resolver := &fakeResolver{maps: map[string]*structuremap.StructureMap{
		"http://example.org/mapB": mapB,
	}}
	eng := NewEngineWithResolver(nil, resolver)

	source := map[string]any{"resourceType": "Patient", "id": "pat-imp", "active": true}
	result, err := eng.Transform(context.Background(), mapA, source)
	require.NoError(t, err, "imports must resolve group from imported map")
	assert.Equal(t, "pat-imp", result.(map[string]any)["id"])
}

// TestEngine_Transform_ThenMapURL_UnresolvedReturnsErrMapNotFound: `then map "<url>"` with an
// unresolvable URL must return ErrMapNotFound with the canonical URL in the detail.
func TestEngine_Transform_ThenMapURL_UnresolvedReturnsErrMapNotFound(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "ThenMapURL",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "entry",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "delegate",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
				Dependent: []structuremap.Dependent{{
					Name:   "copyId",
					MapURL: "http://example.org/missing-map",
					Parameter: []structuremap.Parameter{
						{ValueID: "src"}, {ValueID: "tgt"},
					},
				}},
			}},
		}},
	}
	source := map[string]any{"resourceType": "Patient", "id": "x", "active": true}
	_, err := NewEngine(nil).Transform(context.Background(), sm, source)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMapNotFound),
		"unresolved then-map URL must return ErrMapNotFound, got: %v", err)
	assert.Contains(t, err.Error(), "http://example.org/missing-map")
}

// TestEngine_Transform_TopLevelLet_ResolvesFromFHIRPath: top-level `let` constants are injected
// into scope before the entry group runs; rules reference them by variable name via ValueID.
func TestEngine_Transform_TopLevelLet_ResolvesFromFHIRPath(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "TopLevelLet",
		Status:       "active",
		Const: []structuremap.Rule{{
			Source: []structuremap.Source{{
				Variable: "prefix",
				Element:  "'X-'", // FHIRPath literal — yields string "X-"
			}},
		}},
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "usePrefix",
				Source: []structuremap.Source{{Context: "src", Element: "code", Variable: "c"}},
				Target: []structuremap.Target{{
					Context:   "tgt",
					Element:   "code",
					Transform: "append",
					Parameter: []structuremap.Parameter{
						{ValueID: "prefix"},
						{ValueID: "c"},
					},
				}},
			}},
		}},
	}
	source := map[string]any{"resourceType": "Patient", "id": "x", "active": true, "code": "ABC"}
	result, err := NewEngine(nil).Transform(context.Background(), sm, source)
	require.NoError(t, err)
	assert.Equal(t, "X-ABC", result.(map[string]any)["code"],
		"top-level let prefix must be visible to group rules")
}

// TestEngine_Transform_GroupScopedLet_ResolvesFromLaterRule: group-scoped `let` must be visible
// to subsequent rules in the same group (let-binding writeback via setRoot).
func TestEngine_Transform_GroupScopedLet_ResolvesFromLaterRule(t *testing.T) {
	// FML lowers `let prefix = src.system;` into a Rule with Source[0].Variable="prefix" and no Target.
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "GroupScopedLet",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{
				// let-binding rule: no Target/Rule/Dependent entries
				{
					Source: []structuremap.Source{{
						Context:  "src",
						Element:  "system",
						Variable: "prefix",
					}},
				},
				{
					Name:   "buildCode",
					Source: []structuremap.Source{{Context: "src", Element: "code", Variable: "c"}},
					Target: []structuremap.Target{{
						Context:   "tgt",
						Element:   "code",
						Transform: "append",
						Parameter: []structuremap.Parameter{
							{ValueID: "prefix"}, // resolved from let-binding via setRoot
							{ValueString: "|"},
							{ValueID: "c"},
						},
					}},
				},
			},
		}},
	}
	source := map[string]any{
		"resourceType": "Patient",
		"id":           "x",
		"active":       true,
		"system":       "http://snomed.info",
		"code":         "12345",
	}
	result, err := NewEngine(nil).Transform(context.Background(), sm, source)
	require.NoError(t, err)
	assert.Equal(t, "http://snomed.info|12345", result.(map[string]any)["code"],
		"group-scoped let must be visible to subsequent rules via setRoot")
}

// TestDependent_MapURL_RoundTripsThroughJSON: MapURL previously had `json:"-"` and was silently
// dropped on persistence; it must survive marshal/unmarshal.
func TestDependent_MapURL_RoundTripsThroughJSON(t *testing.T) {
	original := structuremap.Dependent{
		Name:   "copyId",
		MapURL: "http://example.org/mapB",
		Parameter: []structuremap.Parameter{
			{ValueID: "src"}, {ValueID: "tgt"},
		},
	}
	wire, err := json.Marshal(original)
	require.NoError(t, err)
	assert.Contains(t, string(wire), "mapUrl",
		"mapUrl key must appear on the wire so DB round-trips preserve it")

	var decoded structuremap.Dependent
	require.NoError(t, json.Unmarshal(wire, &decoded))
	assert.Equal(t, "http://example.org/mapB", decoded.MapURL,
		"MapURL must survive marshal/unmarshal")
}

// TestEngine_Transform_ExtendsParentTypeMismatch_CrossMapImport: when the parent group lives in an
// imported map, type mismatch must still be detected — findGroupByName previously only walked sm.Group.
func TestEngine_Transform_ExtendsParentTypeMismatch_CrossMapImport_ReturnsErrInputTypeMismatch(t *testing.T) {
	mapB := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/mapB",
		Name:         "MapB",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "parentGroup",
			Input: []structuremap.Input{
				{Name: "src", Type: "Observation", Mode: "source"},
				{Name: "tgt", Type: "Observation", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "noop",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
			}},
		}},
	}

	mapA := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/mapA",
		Name:         "MapA",
		Status:       "active",
		Import:       []string{"http://example.org/mapB"},
		Group: []structuremap.Group{{
			Name:    "entry",
			Extends: "parentGroup",
			Input: []structuremap.Input{
				{Name: "src", Type: "Patient", Mode: "source"},
				{Name: "tgt", Type: "Patient", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "noop",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
			}},
		}},
	}

	resolver := &fakeResolver{maps: map[string]*structuremap.StructureMap{
		"http://example.org/mapB": mapB,
	}}
	eng := NewEngineWithResolver(nil, resolver)

	source := map[string]any{"resourceType": "Patient", "id": "p1", "active": true}
	_, err := eng.Transform(context.Background(), mapA, source)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputTypeMismatch),
		"cross-map extends type-mismatch must surface ErrInputTypeMismatch, got: %v", err)
	assert.Contains(t, err.Error(), "Patient")
	assert.Contains(t, err.Error(), "Observation")
}

// transientResolver: non-NotFound resolver errors must not be remapped to ErrMapNotFound —
// a DB outage must not be reported to clients as a 422 not-found.
type transientResolver struct{ err error }

func (t *transientResolver) FindByURL(_ context.Context, _, _ string) (*structuremap.StructureMap, error) {
	return nil, t.err
}

func TestEngine_Transform_ResolverTransientError_DoesNotMaskAsErrMapNotFound(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "TransientResolverErr",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "entry",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "delegate",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
				Dependent: []structuremap.Dependent{{
					Name:   "g",
					MapURL: "http://example.org/B",
					Parameter: []structuremap.Parameter{
						{ValueID: "src"}, {ValueID: "tgt"},
					},
				}},
			}},
		}},
	}
	transient := errors.New("dial tcp: connection refused")
	eng := NewEngineWithResolver(nil, &transientResolver{err: transient})

	source := map[string]any{"resourceType": "Patient", "id": "p1", "active": true}
	_, err := eng.Transform(context.Background(), sm, source)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrMapNotFound),
		"transient resolver errors must NOT be remapped to ErrMapNotFound; got: %v", err)
	assert.ErrorIs(t, err, transient,
		"the underlying transient cause must be preserved in the error chain")
}

// TestEngine_Transform_ThenMapURL_MultiHopWithinLoadedMap: a group resolved via dep.MapURL must
// expose its own map's groups for further dependents — previously the child scope inherited the
// caller's map, so a plain `dependent helperInB` inside the loaded group would fail.
func TestEngine_Transform_ThenMapURL_MultiHopWithinLoadedMap_Succeeds(t *testing.T) {
	mapB := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/mapB",
		Name:         "MapB",
		Status:       "active",
		Group: []structuremap.Group{
			{
				Name: "entryB",
				Input: []structuremap.Input{
					{Name: "src", Mode: "source"},
					{Name: "tgt", Mode: "target"},
				},
				Rule: []structuremap.Rule{{
					Name:   "delegateToHelper",
					Source: []structuremap.Source{{Context: "src"}},
					Target: []structuremap.Target{{Context: "tgt"}},
					Dependent: []structuremap.Dependent{{
						Name: "helperB",
						Parameter: []structuremap.Parameter{
							{ValueID: "src"}, {ValueID: "tgt"},
						},
					}},
				}},
			},
			{
				Name: "helperB",
				Input: []structuremap.Input{
					{Name: "src", Mode: "source"},
					{Name: "tgt", Mode: "target"},
				},
				Rule: []structuremap.Rule{{
					Name:   "cp",
					Source: []structuremap.Source{{Context: "src", Element: "id", Variable: "v"}},
					Target: []structuremap.Target{{Context: "tgt", Element: "id", Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "v"}}}},
				}},
			},
		},
	}

	// MapA does not import B; it reaches into B via `then map` (no Import entry).
	mapA := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/mapA",
		Name:         "MapA",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "entry",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "delegate",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
				Dependent: []structuremap.Dependent{{
					Name:   "entryB",
					MapURL: "http://example.org/mapB",
					Parameter: []structuremap.Parameter{
						{ValueID: "src"}, {ValueID: "tgt"},
					},
				}},
			}},
		}},
	}

	resolver := &fakeResolver{maps: map[string]*structuremap.StructureMap{
		"http://example.org/mapB": mapB,
	}}
	eng := NewEngineWithResolver(nil, resolver)

	source := map[string]any{"resourceType": "Patient", "id": "pat-multi", "active": true}
	result, err := eng.Transform(context.Background(), mapA, source)
	require.NoError(t, err, "intra-map dependent inside `then map`-loaded group must resolve")
	assert.Equal(t, "pat-multi", result.(map[string]any)["id"])
}

// TestEngine_Transform_TransitiveImports_ResolveAcrossChain: A imports B, B imports C; a dependent
// on a C-only group must still resolve from A (full transitive import chain).
func TestEngine_Transform_TransitiveImports_ResolveAcrossChain(t *testing.T) {
	mapC := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/mapC",
		Name:         "MapC",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "leafCopy",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "cp",
				Source: []structuremap.Source{{Context: "src", Element: "id", Variable: "v"}},
				Target: []structuremap.Target{{Context: "tgt", Element: "id", Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "v"}}}},
			}},
		}},
	}
	mapB := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/mapB",
		Name:         "MapB",
		Status:       "active",
		Import:       []string{"http://example.org/mapC"},
	}
	mapA := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/mapA",
		Name:         "MapA",
		Status:       "active",
		Import:       []string{"http://example.org/mapB"},
		Group: []structuremap.Group{{
			Name: "entry",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "delegate",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
				Dependent: []structuremap.Dependent{{
					Name: "leafCopy", // declared in C, not in A or B directly
					Parameter: []structuremap.Parameter{
						{ValueID: "src"}, {ValueID: "tgt"},
					},
				}},
			}},
		}},
	}

	resolver := &fakeResolver{maps: map[string]*structuremap.StructureMap{
		"http://example.org/mapB": mapB,
		"http://example.org/mapC": mapC,
	}}
	eng := NewEngineWithResolver(nil, resolver)

	source := map[string]any{"resourceType": "Patient", "id": "pat-trans", "active": true}
	result, err := eng.Transform(context.Background(), mapA, source)
	require.NoError(t, err, "transitive imports (A→B→C) must follow the full chain")
	assert.Equal(t, "pat-trans", result.(map[string]any)["id"])
}

// staticTypeResolver implements resolver.Resolver with a fixed URL→type map.
type staticTypeResolver struct {
	m map[string]string
}

func (r *staticTypeResolver) ResolveType(_ context.Context, url string) (string, error) {
	if t, ok := r.m[url]; ok {
		return t, nil
	}
	return "", fmt.Errorf("unknown URL: %s", url)
}

// TestEngine_Transform_AliasExpansion_ResolvesViaStructureMap: a FML alias type (e.g. "QR") must
// be expanded to the aliased URL before the type resolver runs — the engine must consult
// Structure.Alias before comparing against resourceType.
func TestEngine_Transform_AliasExpansion_ResolvesViaStructureMap(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "AliasEchoTest",
		Status:       "active",
		Structure: []structuremap.Structure{
			{URL: "http://hl7.org/fhir/StructureDefinition/QuestionnaireResponse", Mode: "source", Alias: "QR"},
		},
		Group: []structuremap.Group{{
			Name: "main",
			Input: []structuremap.Input{
				{Name: "src", Type: "QR", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "copyName",
				Source: []structuremap.Source{{Context: "src", Element: "name", Variable: "v"}},
				Target: []structuremap.Target{{Context: "tgt", Element: "name", Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "v"}}}},
			}},
		}},
	}
	tr := &staticTypeResolver{m: map[string]string{
		"http://hl7.org/fhir/StructureDefinition/QuestionnaireResponse": "QuestionnaireResponse",
	}}
	eng := New(WithTypeResolver(tr))

	source := map[string]any{"resourceType": "QuestionnaireResponse", "name": "Ada"}
	result, err := eng.Transform(context.Background(), sm, source)
	require.NoError(t, err, "alias QR should expand to the QuestionnaireResponse URL and pass the type-check")
	assert.Equal(t, "Ada", result.(map[string]any)["name"])
}
