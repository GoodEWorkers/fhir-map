package transform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/internal/translate"
)

// mapWithTransform builds a StructureMap with a single target rule, binding the source value into variable `v` if withVariableBind is true.
func mapWithTransform(transformCode string, parameters []structuremap.Parameter, withVariableBind bool) *structuremap.StructureMap {
	source := []structuremap.Source{{Context: "src", Element: "value", Variable: "v"}}
	if !withVariableBind {
		source = []structuremap.Source{{Context: "src"}}
	}
	return &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/test-" + transformCode,
		Name:         "Test_" + transformCode,
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "r",
				Source: source,
				Target: []structuremap.Target{{
					Context:   "tgt",
					Element:   "out",
					Transform: transformCode,
					Parameter: parameters,
				}},
			}},
		}},
	}
}

func TestTransform_Truncate(t *testing.T) {
	src := map[string]any{"value": "abcdef"}
	intVal := 3
	sm := mapWithTransform("truncate", []structuremap.Parameter{
		{ValueID: "v"}, {ValueInteger: &intVal},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "abc", got.(map[string]any)["out"])
}

func TestTransform_Escape(t *testing.T) {
	src := map[string]any{"value": "<a&b>"}
	sm := mapWithTransform("escape", []structuremap.Parameter{{ValueID: "v"}}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "&lt;a&amp;b&gt;", got.(map[string]any)["out"])
}

func TestTransform_Append(t *testing.T) {
	src := map[string]any{"value": "Hello"}
	sm := mapWithTransform("append", []structuremap.Parameter{
		{ValueID: "v"},
		{ValueString: ", "},
		{ValueString: "world"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "Hello, world", got.(map[string]any)["out"])
}

func TestTransform_UUID(t *testing.T) {
	src := map[string]any{"value": "irrelevant"}
	sm := mapWithTransform("uuid", nil, false)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(string)
	assert.Len(t, out, 36)
}

// Decimal literals in evaluate(...) must lex, parse, and evaluate through the full pipeline; canonical for scaling numeric values (OBX value * factor).
func TestTransform_Evaluate_DecimalLiteral(t *testing.T) {
	src := map[string]any{"value": 10}
	sm := mapWithTransform("evaluate", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "$this * 2.5"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, float64(25), got.(map[string]any)["out"])
}

// Unary minus in evaluate(...) exercises the FHIRPath unary-polarity fix through the full pipeline.
func TestTransform_Evaluate_UnaryMinus(t *testing.T) {
	src := map[string]any{"value": 10}
	sm := mapWithTransform("evaluate", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "$this * -2.5"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, float64(-25), got.(map[string]any)["out"])
}

// Inline `(expr)` form lowers to single-argument evaluate; evaluates against variable scope.
func TestTransform_Evaluate_InlineSingleArg(t *testing.T) {
	src := map[string]any{"value": 1}
	sm := mapWithTransform("evaluate", []structuremap.Parameter{
		{ValueString: "v + 1"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.(map[string]any)["out"])
}

// Wrong arity (neither 1 nor 2 args) is a clear error, not a panic.
func TestTransform_Evaluate_WrongArity(t *testing.T) {
	src := map[string]any{"value": 1}
	sm := mapWithTransform("evaluate", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "$this"}, {ValueString: "extra"},
	}, true)
	_, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evaluate")
}

func TestTransform_Cast_StringToInteger(t *testing.T) {
	src := map[string]any{"value": "42"}
	sm := mapWithTransform("cast", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "integer"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, int64(42), got.(map[string]any)["out"])
}

func TestTransform_Reference(t *testing.T) {
	src := map[string]any{"value": map[string]any{
		"resourceType": "Patient", "id": "p123",
	}}
	sm := mapWithTransform("reference", []structuremap.Parameter{{ValueID: "v"}}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(map[string]any)
	assert.Equal(t, "Patient/p123", out["reference"])
}

func TestTransform_C_BuildsCoding(t *testing.T) {
	src := map[string]any{"value": "x"}
	sm := mapWithTransform("c", []structuremap.Parameter{
		{ValueString: "http://example.org/codes"},
		{ValueString: "abc"},
	}, false)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(map[string]any)
	assert.Equal(t, "http://example.org/codes", out["system"])
	assert.Equal(t, "abc", out["code"])
}

func TestTransform_CC_BuildsCodeableConcept(t *testing.T) {
	src := map[string]any{"value": "y"}
	sm := mapWithTransform("cc", []structuremap.Parameter{
		{ValueString: "http://example.org/codes"},
		{ValueString: "xyz"},
		{ValueString: "Display"},
	}, false)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(map[string]any)
	coding, _ := out["coding"].([]any)
	require.Len(t, coding, 1)
	c0, _ := coding[0].(map[string]any)
	assert.Equal(t, "Display", c0["display"])
}

// Per FHIR R5 mapping spec, cc(text) single-arg produces CodeableConcept with only .text set (no coding[]).
func TestTransform_CC_TextOnly_OneArg(t *testing.T) {
	src := map[string]any{"value": "y"}
	sm := mapWithTransform("cc", []structuremap.Parameter{
		{ValueString: "free-text label"},
	}, false)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(map[string]any)
	require.NotNil(t, out, "cc(text) must produce a CodeableConcept map")
	assert.Equal(t, "free-text label", out["text"])
	_, hasCoding := out["coding"]
	assert.False(t, hasCoding, "cc(text) one-arg form must not synthesise a coding[] entry")
}

func TestTransform_Qty_BuildsQuantity(t *testing.T) {
	src := map[string]any{"value": "z"}
	sm := mapWithTransform("qty", []structuremap.Parameter{
		{ValueString: "12.5"},
		{ValueString: "kg"},
	}, false)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(map[string]any)
	assert.Equal(t, 12.5, out["value"])
	assert.Equal(t, "kg", out["unit"])
}

// Per FHIR R5 mapping spec, qty(text) parses UCUM-style "value [unit]" strings; quoted units promote to UCUM system.
func TestTransform_Qty_OneArg_NumberOnly(t *testing.T) {
	src := map[string]any{"value": "z"}
	sm := mapWithTransform("qty", []structuremap.Parameter{
		{ValueString: "7"},
	}, false)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(map[string]any)
	require.NotNil(t, out, "qty(text) must produce a Quantity map")
	assert.Equal(t, 7.0, out["value"])
	_, hasUnit := out["unit"]
	assert.False(t, hasUnit, "qty(\"7\") must leave .unit unset")
}

func TestTransform_Qty_OneArg_ValueAndUnit(t *testing.T) {
	src := map[string]any{"value": "z"}
	sm := mapWithTransform("qty", []structuremap.Parameter{
		{ValueString: "12.5 kg"},
	}, false)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(map[string]any)
	assert.Equal(t, 12.5, out["value"])
	assert.Equal(t, "kg", out["unit"])
	_, hasSystem := out["system"]
	assert.False(t, hasSystem, "unquoted unit must not promote to UCUM system")
}

func TestTransform_Qty_OneArg_UCUMCoded(t *testing.T) {
	src := map[string]any{"value": "z"}
	sm := mapWithTransform("qty", []structuremap.Parameter{
		{ValueString: "5 'mg'"},
	}, false)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(map[string]any)
	assert.Equal(t, 5.0, out["value"])
	assert.Equal(t, "mg", out["code"])
	assert.Equal(t, "http://unitsofmeasure.org", out["system"])
	assert.Equal(t, "mg", out["unit"], "quoted code must round-trip through .unit for human display")
}

func TestTransform_ID_AssignsId(t *testing.T) {
	src := map[string]any{"value": "id-value-here"}
	sm := mapWithTransform("id", []structuremap.Parameter{{ValueID: "v"}}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "id-value-here", got.(map[string]any)["out"])
}

// Single-arg id(value) stays a passthrough for back-compat; multi-arg builds Identifier structure.
func TestTransform_ID_StructuredForm(t *testing.T) {
	src := map[string]any{"value": "MRN-001"}
	sm := mapWithTransform("id", []structuremap.Parameter{
		{ValueString: "http://hospital.example/mrn"},
		{ValueID: "v"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out := got.(map[string]any)["out"].(map[string]any)
	assert.Equal(t, "http://hospital.example/mrn", out["system"])
	assert.Equal(t, "MRN-001", out["value"])
	assert.NotContains(t, out, "type")
}

func TestTransform_ID_StructuredForm_WithType(t *testing.T) {
	src := map[string]any{"value": "MRN-001"}
	sm := mapWithTransform("id", []structuremap.Parameter{
		{ValueString: "http://hospital.example/mrn"},
		{ValueID: "v"},
		{ValueString: "MR"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out := got.(map[string]any)["out"].(map[string]any)
	assert.Equal(t, "http://hospital.example/mrn", out["system"])
	assert.Equal(t, "MRN-001", out["value"])
	assert.Equal(t, "MR", out["type"])
}

// Single-arg cp(value) stays a passthrough for back-compat; multi-arg builds ContactPoint structure.
func TestTransform_CP_StructuredForm(t *testing.T) {
	src := map[string]any{"value": "+1-555-1234"}
	sm := mapWithTransform("cp", []structuremap.Parameter{
		{ValueString: "phone"},
		{ValueID: "v"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out := got.(map[string]any)["out"].(map[string]any)
	assert.Equal(t, "phone", out["system"])
	assert.Equal(t, "+1-555-1234", out["value"])
}

func TestTransform_CP_StructuredForm_WithUse(t *testing.T) {
	src := map[string]any{"value": "home@example.org"}
	sm := mapWithTransform("cp", []structuremap.Parameter{
		{ValueString: "email"},
		{ValueID: "v"},
		{ValueString: "home"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	out := got.(map[string]any)["out"].(map[string]any)
	assert.Equal(t, "email", out["system"])
	assert.Equal(t, "home@example.org", out["value"])
	assert.Equal(t, "home", out["use"])
}

// dateOp with ISO-8601 duration; unrecognized durations error rather than passthrough.
func TestTransform_DateOp_AddDays(t *testing.T) {
	src := map[string]any{"value": "2026-05-16"}
	sm := mapWithTransform("dateOp", []structuremap.Parameter{
		{ValueID: "v"},
		{ValueString: "+"},
		{ValueString: "P5D"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "2026-05-21", got.(map[string]any)["out"])
}

func TestTransform_DateOp_SubtractYears(t *testing.T) {
	src := map[string]any{"value": "2026-05-16"}
	sm := mapWithTransform("dateOp", []structuremap.Parameter{
		{ValueID: "v"},
		{ValueString: "-"},
		{ValueString: "P30Y"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "1996-05-16", got.(map[string]any)["out"])
}

func TestTransform_DateOp_AddMonths(t *testing.T) {
	src := map[string]any{"value": "2026-05-16"}
	sm := mapWithTransform("dateOp", []structuremap.Parameter{
		{ValueID: "v"},
		{ValueString: "+"},
		{ValueString: "P3M"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "2026-08-16", got.(map[string]any)["out"])
}

// Single-arg dateOp keeps bare-passthrough behavior for back-compat with existing fixtures.
func TestTransform_DateOp_SingleArg_Passthrough(t *testing.T) {
	src := map[string]any{"value": "2020-01-01"}
	sm := mapWithTransform("dateOp", []structuremap.Parameter{{ValueID: "v"}}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "2020-01-01", got.(map[string]any)["out"])
}

func TestTransform_ToDate_ISOPassthrough(t *testing.T) {
	src := map[string]any{"value": "2026-05-16"}
	sm := mapWithTransform("toDate", []structuremap.Parameter{{ValueID: "v"}}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "2026-05-16", got.(map[string]any)["out"])
}

func TestTransform_ToDate_FromDDMMYYYY(t *testing.T) {
	src := map[string]any{"value": "16/05/2026"}
	sm := mapWithTransform("toDate", []structuremap.Parameter{
		{ValueID: "v"},
		{ValueString: "dd/MM/yyyy"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "2026-05-16", got.(map[string]any)["out"])
}

func TestTransform_ToDate_FromHL7v2Compact(t *testing.T) {
	src := map[string]any{"value": "20260516"}
	sm := mapWithTransform("toDate", []structuremap.Parameter{
		{ValueID: "v"},
		{ValueString: "yyyyMMdd"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "2026-05-16", got.(map[string]any)["out"])
}

func TestTransform_ToDateTime_FromHL7v2(t *testing.T) {
	src := map[string]any{"value": "20260516143000"}
	sm := mapWithTransform("toDateTime", []structuremap.Parameter{
		{ValueID: "v"},
		{ValueString: "yyyyMMddHHmmss"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "2026-05-16T14:30:00Z", got.(map[string]any)["out"])
}

func TestTransform_ToTime(t *testing.T) {
	src := map[string]any{"value": "14:30:00"}
	sm := mapWithTransform("toTime", []structuremap.Parameter{{ValueID: "v"}}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "14:30:00", got.(map[string]any)["out"])
}

func TestTransform_UnixToDateTime(t *testing.T) {
	// 1747408200 = 2025-05-16T15:10:00Z
	src := map[string]any{"value": int64(1747408200)}
	sm := mapWithTransform("unixToDateTime", []structuremap.Parameter{{ValueID: "v"}}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "2025-05-16T15:10:00Z", got.(map[string]any)["out"])
}

func TestTransform_UnixToDate(t *testing.T) {
	src := map[string]any{"value": int64(1747408200)}
	sm := mapWithTransform("unixToDate", []structuremap.Parameter{{ValueID: "v"}}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "2025-05-16", got.(map[string]any)["out"])
}

func TestTransform_UnixToDateTime_FromString(t *testing.T) {
	// Hospital ETL often delivers epoch as string from CSV/JSON.
	src := map[string]any{"value": "1747408200"}
	sm := mapWithTransform("unixToDateTime", []structuremap.Parameter{{ValueID: "v"}}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "2025-05-16T15:10:00Z", got.(map[string]any)["out"])
}

// Unsupported format strings must fail loud, not silently passthrough.
func TestTransform_ToDate_UnsupportedFormatErrors(t *testing.T) {
	src := map[string]any{"value": "anything"}
	sm := mapWithTransform("toDate", []structuremap.Parameter{
		{ValueID: "v"},
		{ValueString: "Y-Q-?"},
	}, true)
	_, err := NewEngine(nil).Transform(context.Background(), sm, src)
	require.Error(t, err)
}

func TestTransform_Translate_DelegatesToConceptMap(t *testing.T) {
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "http://example.org/cm/gender",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://hl7.org/fhir/administrative-gender",
			Target: "http://example.org/our-gender",
			Element: []conceptmap.Element{
				{Code: "M", Target: []conceptmap.Target{{Code: "male", Relationship: "equivalent"}}},
				{Code: "F", Target: []conceptmap.Target{{Code: "female", Relationship: "equivalent"}}},
			},
		}},
	}
	repo := newTranslateMockRepo()
	repo.Create(context.Background(), cm)
	translator := translate.NewEngine(repo)

	src := map[string]any{"value": "M"}
	sm := mapWithTransform("translate", []structuremap.Parameter{
		{ValueID: "v"},
		{ValueString: "http://example.org/cm/gender"},
		{ValueString: "code"},
	}, true)
	got, err := NewEngine(translator).Transform(context.Background(), sm, src)
	require.NoError(t, err)
	assert.Equal(t, "male", got.(map[string]any)["out"])
}

type translateMockRepo struct {
	conceptMaps map[string]*conceptmap.ConceptMap
	byURL       map[string]*conceptmap.ConceptMap
}

func newTranslateMockRepo() *translateMockRepo {
	return &translateMockRepo{
		conceptMaps: map[string]*conceptmap.ConceptMap{},
		byURL:       map[string]*conceptmap.ConceptMap{},
	}
}
func (m *translateMockRepo) Create(_ context.Context, cm *conceptmap.ConceptMap) (*conceptmap.ConceptMap, error) {
	if cm.ID == "" {
		cm.ID = "cm-id"
	}
	m.conceptMaps[cm.ID] = cm
	if cm.URL != "" {
		m.byURL[cm.URL] = cm
	}
	return cm, nil
}
func (m *translateMockRepo) Read(_ context.Context, id string) (*conceptmap.ConceptMap, error) {
	cm, ok := m.conceptMaps[id]
	if !ok {
		return nil, conceptmap.ErrNotFound
	}
	return cm, nil
}
func (m *translateMockRepo) Update(_ context.Context, id string, cm *conceptmap.ConceptMap) (*conceptmap.ConceptMap, error) {
	m.conceptMaps[id] = cm
	return cm, nil
}
func (m *translateMockRepo) Delete(_ context.Context, id string) error {
	delete(m.conceptMaps, id)
	return nil
}
func (m *translateMockRepo) Search(_ context.Context, _ conceptmap.SearchParams) (*conceptmap.SearchResult, error) {
	return &conceptmap.SearchResult{}, nil
}
func (m *translateMockRepo) FindByURL(_ context.Context, url, _ string) (*conceptmap.ConceptMap, error) {
	cm, ok := m.byURL[url]
	if !ok {
		return nil, conceptmap.ErrNotFound
	}
	return cm, nil
}
func (m *translateMockRepo) FindBySourceScope(_ context.Context, _ string) (*conceptmap.ConceptMap, error) {
	return nil, conceptmap.ErrNotFound
}

// Dependent rules invoke another group by name, threading named variables; confirms nested-group execution.
func TestExecutor_DependentRuleInvokesGroup(t *testing.T) {
	source := map[string]any{
		"items": []any{
			map[string]any{"linkId": "a", "answer": "1"},
			map[string]any{"linkId": "b", "answer": "2"},
		},
	}
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/dep",
		Name:         "WithDependent",
		Status:       "active",
		Group: []structuremap.Group{
			{
				Name: "main",
				Input: []structuremap.Input{
					{Name: "src", Mode: "source"},
					{Name: "tgt", Mode: "target"},
				},
				Rule: []structuremap.Rule{{
					Name: "loop",
					Source: []structuremap.Source{
						{Context: "src", Element: "items", Variable: "i"},
					},
					Target: []structuremap.Target{
						{Context: "tgt", Element: "entries", Variable: "e", Transform: "create"},
					},
					Dependent: []structuremap.Dependent{{
						Name:      "mapItem",
						Parameter: []structuremap.Parameter{{ValueID: "i"}, {ValueID: "e"}},
					}},
				}},
			},
			{
				Name: "mapItem",
				Input: []structuremap.Input{
					{Name: "i", Mode: "source"},
					{Name: "e", Mode: "target"},
				},
				Rule: []structuremap.Rule{{
					Name: "copyAnswer",
					Source: []structuremap.Source{
						{Context: "i", Element: "answer", Variable: "v"},
					},
					Target: []structuremap.Target{
						{Context: "e", Element: "value", Transform: "copy",
							Parameter: []structuremap.Parameter{{ValueID: "v"}}},
					},
				}},
			},
		},
	}

	got, err := NewEngine(nil).Transform(context.Background(), sm, source)
	require.NoError(t, err)
	target, _ := got.(map[string]any)
	entries, ok := target["entries"].(map[string]any)
	require.True(t, ok, "expected target.entries to be a map populated by the dependent rule; got %v", target)
	assert.Equal(t, "2", entries["value"], "last iteration wins under single-target semantics")
	_ = ok
}
