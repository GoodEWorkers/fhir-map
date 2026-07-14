package transform

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

// TestExecutor_QRToPatient_Copy is the canonical QuestionnaireResponse-to-Patient
// mapping used throughout the executor tests.
func TestExecutor_QRToPatient_Copy(t *testing.T) {
	source := mustJSON(t, `{
		"resourceType": "QuestionnaireResponse",
		"item": [
			{"linkId": "first", "answer": [{"valueString": "Ada"}]},
			{"linkId": "last",  "answer": [{"valueString": "Lovelace"}]}
		]
	}`)

	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/test/qr-to-patient",
		Name:         "QRToPatient",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "MapQRtoPatient",
			Input: []structuremap.Input{
				{Name: "src", Type: "QuestionnaireResponse", Mode: "source"},
				{Name: "tgt", Type: "Patient", Mode: "target"},
			},
			Rule: []structuremap.Rule{
				{
					Name: "firstName",
					Source: []structuremap.Source{{
						Context:  "src",
						Element:  "item.where(linkId = 'first').answer.valueString",
						Variable: "v",
					}},
					Target: []structuremap.Target{{
						Context:   "tgt",
						Element:   "firstName",
						Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "v"}},
					}},
				},
				{
					Name: "lastName",
					Source: []structuremap.Source{{
						Context:  "src",
						Element:  "item.where(linkId = 'last').answer.valueString",
						Variable: "v",
					}},
					Target: []structuremap.Target{{
						Context:   "tgt",
						Element:   "lastName",
						Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "v"}},
					}},
				},
			},
		}},
	}

	engine := NewEngine(nil)
	result, err := engine.Transform(t.Context(), sm, source)
	require.NoError(t, err)

	target, ok := result.(map[string]any)
	require.True(t, ok, "expected target to be a map, got %T", result)
	assert.Equal(t, "Ada", target["firstName"])
	assert.Equal(t, "Lovelace", target["lastName"])
}

func TestExecutor_Create_ThenCopy(t *testing.T) {
	source := mustJSON(t, `{
		"resourceType": "QuestionnaireResponse",
		"item": [
			{"linkId": "name", "answer": [{"valueString": "Augusta"}]}
		]
	}`)

	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/test/create-then-copy",
		Name:         "CreateThenCopy",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name: "name",
				Source: []structuremap.Source{{
					Context:  "src",
					Element:  "item.where(linkId = 'name').answer.valueString",
					Variable: "v",
				}},
				Target: []structuremap.Target{
					{Context: "tgt", Element: "name", Variable: "n", Transform: "create"},
					{Context: "n", Element: "given", Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "v"}}},
				},
			}},
		}},
	}

	engine := NewEngine(nil)
	result, err := engine.Transform(t.Context(), sm, source)
	require.NoError(t, err)
	target, ok := result.(map[string]any)
	require.True(t, ok)

	nameObj, ok := target["name"].(map[string]any)
	require.True(t, ok, "expected target.name to be a map; got %T (%v)", target["name"], target)
	assert.Equal(t, "Augusta", nameObj["given"])
}

func TestExecutor_MissingSource_NoOutput(t *testing.T) {
	source := mustJSON(t, `{"resourceType": "QuestionnaireResponse", "item": []}`)

	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "G", Status: "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name: "missing",
				Source: []structuremap.Source{{
					Context: "src", Element: "item.where(linkId = 'gone').answer.valueString", Variable: "v",
				}},
				Target: []structuremap.Target{{
					Context: "tgt", Element: "ghost", Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "v"}},
				}},
			}},
		}},
	}

	engine := NewEngine(nil)
	result, err := engine.Transform(t.Context(), sm, source)
	require.NoError(t, err)
	target, ok := result.(map[string]any)
	require.True(t, ok)
	_, present := target["ghost"]
	assert.False(t, present, "missing source must not produce a target field")
}

func TestExecutor_EmptyMap_Errors(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap", Name: "X", Status: "active",
	}
	engine := NewEngine(nil)
	_, err := engine.Transform(t.Context(), sm, map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "group")
}

func TestExecutor_TargetListMode_PlusAllocatesEntry(t *testing.T) {
	source := mustJSON(t, `{"items": [{"v": "first"}, {"v": "second"}]}`)

	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "BundleEntryPlus",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name: "each-item",
				Source: []structuremap.Source{{
					Context:  "src",
					Element:  "items",
					Variable: "i",
				}},
				Target: []structuremap.Target{{
					Context:   "tgt",
					Element:   "entry[+].resource",
					Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "i"}},
				}},
			}},
		}},
	}

	engine := NewEngine(nil)
	result, err := engine.Transform(t.Context(), sm, source)
	require.NoError(t, err)
	target := result.(map[string]any)

	entries, ok := target["entry"].([]any)
	require.Truef(t, ok, "entry must be a list, got %T (raw=%+v)", target["entry"], target)
	require.Len(t, entries, 2, "expected one entry per source item")

	e0 := entries[0].(map[string]any)
	e1 := entries[1].(map[string]any)
	assert.Equal(t, map[string]any{"v": "first"}, e0["resource"])
	assert.Equal(t, map[string]any{"v": "second"}, e1["resource"])

	_, hasLiteral := target["entry[+].resource"]
	assert.Falsef(t, hasLiteral, "literal-key bug regressed: %+v", target)
}

func TestExecutor_TargetListMode_EqualsReusesEntry(t *testing.T) {
	source := mustJSON(t, `{"v": "value", "u": "url"}`)

	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "BundleEntryEquals",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{
				{
					Name:   "alloc",
					Source: []structuremap.Source{{Context: "src", Element: "v", Variable: "x"}},
					Target: []structuremap.Target{{
						Context:   "tgt",
						Element:   "entry[+].resource",
						Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "x"}},
					}},
				},
				{
					Name:   "reuse",
					Source: []structuremap.Source{{Context: "src", Element: "u", Variable: "y"}},
					Target: []structuremap.Target{{
						Context:   "tgt",
						Element:   "entry[=].fullUrl",
						Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "y"}},
					}},
				},
			},
		}},
	}

	engine := NewEngine(nil)
	result, err := engine.Transform(t.Context(), sm, source)
	require.NoError(t, err)
	target := result.(map[string]any)

	entries, ok := target["entry"].([]any)
	require.Truef(t, ok, "entry must be a list, got %T (raw=%+v)", target["entry"], target)
	require.Len(t, entries, 1, "[=] reuses the existing entry; should NOT have allocated a second")

	e0 := entries[0].(map[string]any)
	assert.Equal(t, "value", e0["resource"])
	assert.Equal(t, "url", e0["fullUrl"])
}

func TestExecutor_TargetListMode_NoMarker_BehavesAsBefore(t *testing.T) {
	source := mustJSON(t, `{"name": "Ada"}`)

	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "PlainCopy",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "name",
				Source: []structuremap.Source{{Context: "src", Element: "name", Variable: "n"}},
				Target: []structuremap.Target{{
					Context:   "tgt",
					Element:   "firstName",
					Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "n"}},
				}},
			}},
		}},
	}

	engine := NewEngine(nil)
	result, err := engine.Transform(t.Context(), sm, source)
	require.NoError(t, err)
	target := result.(map[string]any)
	assert.Equal(t, "Ada", target["firstName"])
}

func TestExecutor_PercentVar_ResolvesFromScope(t *testing.T) {
	source := mustJSON(t, `{
		"primary":   {"identifier": [{"value": "ABC-123"}]},
		"secondary": "ABC-123"
	}`)

	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "PercentVarThroughScope",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{
				{
					// Rule 1: bind src.primary to %DR via target.variable so
					// scope.values["DR"] holds the primary resource.
					Name: "bind-dr",
					Source: []structuremap.Source{{
						Context: "src", Element: "primary", Variable: "v",
					}},
					Target: []structuremap.Target{{
						Context:   "tgt",
						Variable:  "DR",
						Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "v"}},
					}},
				},
				{
					// Rule 2: source.condition references %DR.identifier.value;
					// rule fires only when the condition matches src.secondary.
					Name: "guard-by-percent",
					Source: []structuremap.Source{{
						Context:   "src",
						Element:   "secondary",
						Variable:  "x",
						Condition: "$this = %DR.identifier.value",
					}},
					Target: []structuremap.Target{{
						Context:   "tgt",
						Element:   "match",
						Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "x"}},
					}},
				},
			},
		}},
	}

	engine := NewEngine(nil)
	result, err := engine.Transform(t.Context(), sm, source)
	require.NoError(t, err)
	target := result.(map[string]any)
	assert.Equal(t, "ABC-123", target["match"], "second rule must fire because %%DR.identifier.value matches src.secondary")
}

func sourceListModeMap(mode string) *structuremap.StructureMap {
	return &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "SourceListMode",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name: "each",
				Source: []structuremap.Source{{
					Context:  "src",
					Element:  "items",
					Variable: "v",
					ListMode: mode,
				}},
				Target: []structuremap.Target{{
					Context:   "tgt",
					Element:   "out[+].value",
					Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "v"}},
				}},
			}},
		}},
	}
}

func threeItemSource() any {
	return map[string]any{"items": []any{"a", "b", "c"}}
}

func TestExecutor_SourceListMode_First(t *testing.T) {
	result, err := NewEngine(nil).Transform(t.Context(), sourceListModeMap("first"), threeItemSource())
	require.NoError(t, err)
	target := result.(map[string]any)
	outs := target["out"].([]any)
	require.Len(t, outs, 1, "first must yield exactly one target")
	assert.Equal(t, "a", outs[0].(map[string]any)["value"])
}

func TestExecutor_SourceListMode_Last(t *testing.T) {
	result, err := NewEngine(nil).Transform(t.Context(), sourceListModeMap("last"), threeItemSource())
	require.NoError(t, err)
	target := result.(map[string]any)
	outs := target["out"].([]any)
	require.Len(t, outs, 1)
	assert.Equal(t, "c", outs[0].(map[string]any)["value"])
}

func TestExecutor_SourceListMode_NotFirst(t *testing.T) {
	result, err := NewEngine(nil).Transform(t.Context(), sourceListModeMap("not_first"), threeItemSource())
	require.NoError(t, err)
	target := result.(map[string]any)
	outs := target["out"].([]any)
	require.Len(t, outs, 2)
	assert.Equal(t, "b", outs[0].(map[string]any)["value"])
	assert.Equal(t, "c", outs[1].(map[string]any)["value"])
}

func TestExecutor_SourceListMode_NotLast_Hyphenated(t *testing.T) {
	result, err := NewEngine(nil).Transform(t.Context(), sourceListModeMap("not-last"), threeItemSource())
	require.NoError(t, err)
	target := result.(map[string]any)
	outs := target["out"].([]any)
	require.Len(t, outs, 2)
	assert.Equal(t, "a", outs[0].(map[string]any)["value"])
	assert.Equal(t, "b", outs[1].(map[string]any)["value"])
}

func TestExecutor_SourceListMode_OnlyOne_SingleElementOk(t *testing.T) {
	src := mustJSON(t, `{"items": ["solo"]}`)
	result, err := NewEngine(nil).Transform(t.Context(), sourceListModeMap("only_one"), src)
	require.NoError(t, err)
	target := result.(map[string]any)
	outs := target["out"].([]any)
	require.Len(t, outs, 1)
	assert.Equal(t, "solo", outs[0].(map[string]any)["value"])
}

func TestExecutor_SourceListMode_OnlyOne_MultipleErrors(t *testing.T) {
	_, err := NewEngine(nil).Transform(t.Context(), sourceListModeMap("only_one"), threeItemSource())
	require.Error(t, err, "only_one must hard-error when binding has more than one element")
	assert.Contains(t, err.Error(), "only_one")
}

func srcRuleMap(srcs []structuremap.Source) *structuremap.StructureMap {
	return &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "SrcEnforce",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "r",
				Source: srcs,
				Target: []structuremap.Target{{
					Context:   "tgt",
					Element:   "out",
					Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "v"}},
				}},
			}},
		}},
	}
}

func TestExecutor_SourceCheck_FiresOnElementLessSource(t *testing.T) {
	sm := srcRuleMap([]structuremap.Source{{
		Context:  "src",
		Variable: "v",
		Check:    "$this.exists().not()", // intentionally false — src is bound
	}})
	src := map[string]any{"some": "data"}
	_, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.Error(t, err, "check must run even when source has no Element")
	require.True(t, errors.Is(err, ErrCheckFailed), "check failure must wrap ErrCheckFailed, got: %v", err)
	assert.Contains(t, err.Error(), "check")
}

func TestExecutor_SourceMin_FiresOnElementLessSource_WhenContextUnbound(t *testing.T) {
	two := 2
	sm := srcRuleMap([]structuremap.Source{{
		Context:  "src",
		Variable: "v",
		Min:      &two,
	}})
	src := map[string]any{"some": "data"}
	_, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "min")
}

func TestExecutor_SourceCheck_PassesWhenTrue(t *testing.T) {
	sm := srcRuleMap([]structuremap.Source{{
		Context:  "src",
		Element:  "v",
		Variable: "v",
		Check:    "$this = 'ok'",
	}})
	src := map[string]any{"v": "ok"}
	result, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "ok", result.(map[string]any)["out"])
}

func TestExecutor_SourceCheck_FailsTransformWhenFalse(t *testing.T) {
	sm := srcRuleMap([]structuremap.Source{{
		Context:  "src",
		Element:  "v",
		Variable: "v",
		Check:    "$this = 'expected'",
	}})
	src := map[string]any{"v": "actual"}
	_, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.Error(t, err, "check assertion must fail loud")
	require.True(t, errors.Is(err, ErrCheckFailed), "check failure must wrap ErrCheckFailed, got: %v", err)
	assert.Contains(t, err.Error(), "check")
}

func TestExecutor_SourceDefaultValue_FillsEmpty(t *testing.T) {
	sm := srcRuleMap([]structuremap.Source{{
		Context:      "src",
		Element:      "missing",
		Variable:     "v",
		DefaultValue: []byte(`"FALLBACK"`),
	}})
	src := map[string]any{"present": "x"}
	result, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "FALLBACK", result.(map[string]any)["out"])
}

func TestExecutor_SourceDefaultValue_AbsentLeavesNoOp(t *testing.T) {
	sm := srcRuleMap([]structuremap.Source{{
		Context:  "src",
		Element:  "missing",
		Variable: "v",
	}})
	src := map[string]any{"present": "x"}
	result, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.NoError(t, err)
	_, ok := result.(map[string]any)["out"]
	assert.False(t, ok, "missing source without defaultValue → rule is a no-op")
}

func TestExecutor_SourceMin_ErrorsWhenBindingTooSmall(t *testing.T) {
	one := 1
	sm := srcRuleMap([]structuremap.Source{{
		Context:  "src",
		Element:  "missing",
		Variable: "v",
		Min:      &one,
	}})
	src := map[string]any{"present": "x"}
	_, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "min")
}

func TestExecutor_SourceMin_PassesWhenSatisfied(t *testing.T) {
	one := 1
	sm := srcRuleMap([]structuremap.Source{{
		Context:  "src",
		Element:  "v",
		Variable: "v",
		Min:      &one,
	}})
	src := map[string]any{"v": "ok"}
	result, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "ok", result.(map[string]any)["out"])
}

func TestExecutor_SourceMax_ErrorsWhenBindingTooLarge(t *testing.T) {
	sm := srcRuleMap([]structuremap.Source{{
		Context:  "src",
		Element:  "items",
		Variable: "v",
		Max:      "1",
	}})
	src := map[string]any{"items": []any{"a", "b", "c"}}
	_, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max")
}

func TestExecutor_SourceMax_StarMeansUnlimited(t *testing.T) {
	sm := srcRuleMap([]structuremap.Source{{
		Context:  "src",
		Element:  "items",
		Variable: "v",
		Max:      "*",
	}})
	src := map[string]any{"items": []any{"a", "b", "c"}}
	_, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.NoError(t, err)
}

func TestExecutor_MultiSource_BothVariablesBound(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "FamilyGiven",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name: "fullname",
				Source: []structuremap.Source{
					{Context: "src", Element: "family", Variable: "fam"},
					{Context: "src", Element: "given", Variable: "giv"},
				},
				Target: []structuremap.Target{{
					Context:   "tgt",
					Element:   "fullName",
					Transform: "append",
					Parameter: []structuremap.Parameter{
						{ValueID: "giv"},
						{ValueString: " "},
						{ValueID: "fam"},
					},
				}},
			}},
		}},
	}
	src := map[string]any{"family": "Lovelace", "given": "Ada"}
	result, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.NoError(t, err)
	target := result.(map[string]any)
	assert.Equal(t, "Ada Lovelace", target["fullName"], "second source variable must be bound; today only fam is")
}

func TestExecutor_MultiSource_CartesianProduct(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "Product",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name: "combine",
				Source: []structuremap.Source{
					{Context: "src", Element: "a", Variable: "av"},
					{Context: "src", Element: "b", Variable: "bv"},
				},
				Target: []structuremap.Target{{
					Context:   "tgt",
					Element:   "rows[+].pair",
					Transform: "append",
					Parameter: []structuremap.Parameter{
						{ValueID: "av"},
						{ValueString: ":"},
						{ValueID: "bv"},
					},
				}},
			}},
		}},
	}
	src := map[string]any{
		"a": []any{"x", "y"},
		"b": []any{"1", "2", "3"},
	}
	result, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.NoError(t, err)
	rows := result.(map[string]any)["rows"].([]any)
	require.Len(t, rows, 6, "2x3 cartesian product expected")
	want := []string{"x:1", "x:2", "x:3", "y:1", "y:2", "y:3"}
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.(map[string]any)["pair"].(string))
	}
	assert.ElementsMatch(t, want, got)
}

func TestExecutor_MultiSource_EmptyBindingNoOps(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "OneEmpty",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name: "r",
				Source: []structuremap.Source{
					{Context: "src", Element: "present", Variable: "p"},
					{Context: "src", Element: "missing", Variable: "m"},
				},
				Target: []structuremap.Target{{
					Context: "tgt", Element: "x", Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "p"}},
				}},
			}},
		}},
	}
	src := map[string]any{"present": "yes"}
	result, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.NoError(t, err)
	_, has := result.(map[string]any)["x"]
	assert.False(t, has, "second source empty → rule no-ops, target not written")
}

// Per FHIR spec: a group that extends a parent inherits all its rules
// and executes them alongside its own.
func TestExecutor_GroupExtends_MergesParentRules(t *testing.T) {
	src := mustJSON(t, `{"a": "AA", "b": "BB"}`)

	parent := structuremap.Group{
		Name: "Parent",
		Input: []structuremap.Input{
			{Name: "src", Mode: "source"},
			{Name: "tgt", Mode: "target"},
		},
		Rule: []structuremap.Rule{{
			Name: "from-parent",
			Source: []structuremap.Source{{
				Context: "src", Element: "a", Variable: "v",
			}},
			Target: []structuremap.Target{{
				Context: "tgt", Element: "parentSet", Transform: "copy",
				Parameter: []structuremap.Parameter{{ValueID: "v"}},
			}},
		}},
	}
	child := structuremap.Group{
		Name:    "Child",
		Extends: "Parent",
		Input: []structuremap.Input{
			{Name: "src", Mode: "source"},
			{Name: "tgt", Mode: "target"},
		},
		Rule: []structuremap.Rule{{
			Name: "from-child",
			Source: []structuremap.Source{{
				Context: "src", Element: "b", Variable: "v",
			}},
			Target: []structuremap.Target{{
				Context: "tgt", Element: "childSet", Transform: "copy",
				Parameter: []structuremap.Parameter{{ValueID: "v"}},
			}},
		}},
	}

	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/test/extends",
		Name:         "ExtendsMerge", Status: "active",
		Group: []structuremap.Group{child, parent},
	}

	got, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.NoError(t, err)
	target, ok := got.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "AA", target["parentSet"], "child must inherit parent's rule via extends")
	assert.Equal(t, "BB", target["childSet"], "child's own rule must still fire")
}

func TestExecutor_GroupExtends_MergeAppliesViaDependent(t *testing.T) {
	src := mustJSON(t, `{"a": "AA", "b": "BB"}`)

	parent := structuremap.Group{
		Name: "Parent",
		Input: []structuremap.Input{
			{Name: "s", Mode: "source"},
			{Name: "t", Mode: "target"},
		},
		Rule: []structuremap.Rule{{
			Name: "p-rule",
			Source: []structuremap.Source{{
				Context: "s", Element: "a", Variable: "v",
			}},
			Target: []structuremap.Target{{
				Context: "t", Element: "parentSet", Transform: "copy",
				Parameter: []structuremap.Parameter{{ValueID: "v"}},
			}},
		}},
	}
	child := structuremap.Group{
		Name: "Child", Extends: "Parent",
		Input: []structuremap.Input{
			{Name: "s", Mode: "source"},
			{Name: "t", Mode: "target"},
		},
		Rule: []structuremap.Rule{{
			Name: "c-rule",
			Source: []structuremap.Source{{
				Context: "s", Element: "b", Variable: "v",
			}},
			Target: []structuremap.Target{{
				Context: "t", Element: "childSet", Transform: "copy",
				Parameter: []structuremap.Parameter{{ValueID: "v"}},
			}},
		}},
	}
	entry := structuremap.Group{
		Name: "Entry",
		Input: []structuremap.Input{
			{Name: "src", Mode: "source"},
			{Name: "tgt", Mode: "target"},
		},
		Rule: []structuremap.Rule{{
			Name: "delegate",
			Source: []structuremap.Source{{
				Context: "src", Variable: "v",
			}},
			Target: []structuremap.Target{{
				Context: "tgt", Variable: "out", Transform: "copy",
				Parameter: []structuremap.Parameter{{ValueID: "tgt"}},
			}},
			Dependent: []structuremap.Dependent{{
				Name: "Child",
				Parameter: []structuremap.Parameter{
					{ValueID: "src"},
					{ValueID: "tgt"},
				},
			}},
		}},
	}

	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/test/extends-dep",
		Name:         "ExtendsViaDependent", Status: "active",
		Group: []structuremap.Group{entry, child, parent},
	}

	got, err := NewEngine(nil).Transform(t.Context(), sm, src)
	require.NoError(t, err)
	target, ok := got.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "AA", target["parentSet"], "dependent-invoked child must inherit parent rules")
	assert.Equal(t, "BB", target["childSet"])
}

func TestExecutor_InlineConceptMap_Translate(t *testing.T) {
	source := mustJSON(t, `{"gender": "male"}`)

	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "#gender",
		ID:           "gender",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://hl7.org/fhir/administrative-gender",
			Target: "http://example.org/gender",
			Element: []conceptmap.Element{
				{Code: "male", Target: []conceptmap.Target{{Code: "M", Relationship: "equivalent"}}},
				{Code: "female", Target: []conceptmap.Target{{Code: "F", Relationship: "equivalent"}}},
			},
		}},
	}
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "InlineGender", Status: "active",
		Contained: []*conceptmap.ConceptMap{cm},
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name: "gender",
				Source: []structuremap.Source{{
					Context: "src", Element: "gender", Variable: "v",
				}},
				Target: []structuremap.Target{{
					Context: "tgt", Element: "gender", Transform: "translate",
					Parameter: []structuremap.Parameter{
						{ValueID: "v"},
						{ValueString: "#gender"},
						{ValueString: "code"},
					},
				}},
			}},
		}},
	}

	result, err := NewEngine(nil).Transform(t.Context(), sm, source)
	require.NoError(t, err)
	target := result.(map[string]any)
	assert.Equal(t, "M", target["gender"], "inline conceptmap must translate male → M without an external engine")
}

func TestExecutor_InlineConceptMap_TranslateCoding(t *testing.T) {
	source := mustJSON(t, `{"gender": "female"}`)

	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "#gender",
		ID:           "gender",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://src",
			Target: "http://tgt",
			Element: []conceptmap.Element{
				{Code: "female", Target: []conceptmap.Target{{Code: "F", Display: "Female", Relationship: "equivalent"}}},
			},
		}},
	}
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "InlineGenderCoding", Status: "active",
		Contained: []*conceptmap.ConceptMap{cm},
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name: "gender",
				Source: []structuremap.Source{{
					Context: "src", Element: "gender", Variable: "v",
				}},
				Target: []structuremap.Target{{
					Context: "tgt", Element: "gender", Transform: "translate",
					Parameter: []structuremap.Parameter{
						{ValueID: "v"},
						{ValueString: "#gender"},
						{ValueString: "Coding"},
					},
				}},
			}},
		}},
	}

	result, err := NewEngine(nil).Transform(t.Context(), sm, source)
	require.NoError(t, err)
	target := result.(map[string]any)
	coding, ok := target["gender"].(map[string]any)
	require.True(t, ok, "Coding output must be a map; got %T", target["gender"])
	assert.Equal(t, "F", coding["code"])
	assert.Equal(t, "http://tgt", coding["system"])
	assert.Equal(t, "Female", coding["display"])
}

func TestExecutor_InlineConceptMap_NoMatchNoTranslator(t *testing.T) {
	source := mustJSON(t, `{"gender": "male"}`)

	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "MissingInline", Status: "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "gender",
				Source: []structuremap.Source{{Context: "src", Element: "gender", Variable: "v"}},
				Target: []structuremap.Target{{
					Context: "tgt", Element: "gender", Transform: "translate",
					Parameter: []structuremap.Parameter{
						{ValueID: "v"},
						{ValueString: "#unknown"},
						{ValueString: "code"},
					},
				}},
			}},
		}},
	}

	_, err := NewEngine(nil).Transform(t.Context(), sm, source)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no inline map matched")
}

func TestEngine_Transform_UnresolvedDependent_ReturnsErrMapNotFound(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/test/unresolved-dep",
		Name:         "UnresolvedDepTest",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "Main",
			Input: []structuremap.Input{
				{Name: "src", Type: "Patient", Mode: "source"},
				{Name: "tgt", Type: "Patient", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "callMissing",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
				Dependent: []structuremap.Dependent{{
					Name:      "NonExistentGroup",
					Parameter: []structuremap.Parameter{{ValueID: "src"}, {ValueID: "tgt"}},
				}},
			}},
		}},
	}

	source := map[string]any{
		"resourceType": "Patient",
		"id":           "pat-unresolved",
		"active":       true,
	}

	_, err := NewEngine(nil).Transform(t.Context(), sm, source)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMapNotFound),
		"unresolved dependent group must return ErrMapNotFound, got: %v", err)
}

func TestExecutor_DependentRecursion_SelfReference_HitsCap(t *testing.T) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "SelfRecursive",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "recurse",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
				Dependent: []structuremap.Dependent{{
					Name:      "g",
					Parameter: []structuremap.Parameter{{ValueID: "src"}, {ValueID: "tgt"}},
				}},
			}},
		}},
	}
	source := map[string]any{"resourceType": "Patient", "id": "x", "active": true}
	_, err := NewEngine(nil).Transform(t.Context(), sm, source)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrRecursionLimit),
		"self-recursive group must return ErrRecursionLimit, got: %v", err)
	assert.Contains(t, err.Error(), "g")
}

func TestExecutor_DependentRecursion_MutualChain_HitsCap(t *testing.T) {
	makeGroup := func(name, next string) structuremap.Group {
		return structuremap.Group{
			Name: name,
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "fwd",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
				Dependent: []structuremap.Dependent{{
					Name:      next,
					Parameter: []structuremap.Parameter{{ValueID: "src"}, {ValueID: "tgt"}},
				}},
			}},
		}
	}
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "MutualChain",
		Status:       "active",
		Group: []structuremap.Group{
			makeGroup("a", "b"),
			makeGroup("b", "c"),
			makeGroup("c", "a"),
		},
	}
	source := map[string]any{"resourceType": "Patient", "id": "x", "active": true}
	_, err := NewEngine(nil).Transform(t.Context(), sm, source)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrRecursionLimit),
		"mutual-chain recursion must return ErrRecursionLimit, got: %v", err)
}

func TestExecutor_DependentRecursion_Depth4_Succeeds(t *testing.T) {
	leaf := structuremap.Group{
		Name: "leaf",
		Input: []structuremap.Input{
			{Name: "src", Mode: "source"},
			{Name: "tgt", Mode: "target"},
		},
		Rule: []structuremap.Rule{{
			Name:   "copyId",
			Source: []structuremap.Source{{Context: "src", Element: "id", Variable: "v"}},
			Target: []structuremap.Target{{Context: "tgt", Element: "id", Transform: "copy", Parameter: []structuremap.Parameter{{ValueID: "v"}}}},
		}},
	}
	makeForwarder := func(name, next string) structuremap.Group {
		return structuremap.Group{
			Name: name,
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "fwd",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
				Dependent: []structuremap.Dependent{{
					Name:      next,
					Parameter: []structuremap.Parameter{{ValueID: "src"}, {ValueID: "tgt"}},
				}},
			}},
		}
	}
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "Depth4",
		Status:       "active",
		Group: []structuremap.Group{
			makeForwarder("g1", "g2"),
			makeForwarder("g2", "g3"),
			makeForwarder("g3", "g4"),
			makeForwarder("g4", "leaf"),
			leaf,
		},
	}
	source := map[string]any{"resourceType": "Patient", "id": "pat-1", "active": true}
	result, err := NewEngine(nil).Transform(t.Context(), sm, source)
	require.NoError(t, err, "chain of 4 dependents is within the cap")
	assert.Equal(t, "pat-1", result.(map[string]any)["id"])
}

func TestExecutor_ThenAnonymousBlock_NestedRuleSeesParentScope(t *testing.T) {
	// FML equivalent:
	//   src.name as n -> tgt.name = n then {
	//     n.given as g -> tgt.given = g;
	//   };
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "ThenBlock",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name: "outer",
				Source: []structuremap.Source{{
					Context: "src", Element: "name", Variable: "n",
				}},
				Target: []structuremap.Target{{
					Context: "tgt", Element: "name", Transform: "copy",
					Parameter: []structuremap.Parameter{{ValueID: "n"}},
				}},
				// then { n.given as g -> tgt.given = g; }
				Rule: []structuremap.Rule{{
					Name: "inner",
					Source: []structuremap.Source{{
						Context: "n", Element: "given", Variable: "g",
					}},
					Target: []structuremap.Target{{
						Context: "tgt", Element: "given", Transform: "copy",
						Parameter: []structuremap.Parameter{{ValueID: "g"}},
					}},
				}},
			}},
		}},
	}
	source := map[string]any{
		"resourceType": "Patient",
		"id":           "x",
		"name":         map[string]any{"given": "Ada"},
	}
	result, err := NewEngine(nil).Transform(t.Context(), sm, source)
	require.NoError(t, err)
	m := result.(map[string]any)
	assert.Equal(t, map[string]any{"given": "Ada"}, m["name"], "outer target write must fire")
	assert.Equal(t, "Ada", m["given"], "nested rule must see parent-scope variable n")
}

// mustJSON unmarshals a JSON string literal into a Go any.
func mustJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("mustJSON: %v", err)
	}
	return v
}

func TestExecutor_DependentRecursion_Depth5_HitsCap(t *testing.T) {
	leaf := structuremap.Group{
		Name: "leaf",
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
	}
	makeForwarder := func(name, next string) structuremap.Group {
		return structuremap.Group{
			Name: name,
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "fwd",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt"}},
				Dependent: []structuremap.Dependent{{
					Name:      next,
					Parameter: []structuremap.Parameter{{ValueID: "src"}, {ValueID: "tgt"}},
				}},
			}},
		}
	}
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "Depth5",
		Status:       "active",
		Group: []structuremap.Group{
			makeForwarder("g1", "g2"),
			makeForwarder("g2", "g3"),
			makeForwarder("g3", "g4"),
			makeForwarder("g4", "g5"),
			makeForwarder("g5", "leaf"),
			leaf,
		},
	}
	source := map[string]any{"resourceType": "Patient", "id": "pat-1", "active": true}
	_, err := NewEngine(nil).Transform(t.Context(), sm, source)
	require.Error(t, err, "5 nested dependents must trip the cap")
	require.True(t, errors.Is(err, ErrRecursionLimit),
		"depth-5 chain must return ErrRecursionLimit, got: %v", err)
}
