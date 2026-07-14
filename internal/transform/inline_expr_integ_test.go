package transform_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/transform"
	"github.com/goodeworkers/fhir-map/internal/transform/fml"
)

// Regression for bruno-mapping-coverage 09_fml_as_before_where_GAP: the quoted
// evaluate form evaluate(i, 'name') must evaluate `name` as a FHIRPath against
// i (→ "keep"), not return the string literal "name".
func TestInteg_ExplicitEvaluate_QuotedPath_FromFMLText(t *testing.T) {
	src := "map \"u\"=\"n\"\n" +
		"group main(source src, target tgt) {\n" +
		"  src.items as i where (status = 'active') -> tgt.found = evaluate(i, 'name');\n" +
		"}\n"
	sm, err := fml.Parse(src)
	require.NoError(t, err)
	out, err := transform.New().Transform(context.Background(), sm, map[string]any{
		"resourceType": "Basic",
		"items":        []any{map[string]any{"status": "active", "name": "keep"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "keep", out.(map[string]any)["found"])
}

// P0 — the inline FHIRPath form `tgt.x = (expr)` lowers to a 1-argument
// `evaluate` with no explicit context. It must evaluate the expression
// against the rule's variable scope, so bare variable names (e.g. `v`)
// resolve. Previously this parsed cleanly but failed at runtime with
// "evaluate requires (context, fhirpathExpr); got 1 args".
func TestInteg_InlineExpr_ReferencesBoundVariable(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s.value as v -> t.plusOne = (v + 1);
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)
	out, err := transform.New().Transform(context.Background(), sm, map[string]any{"value": 1})
	require.NoError(t, err)
	assert.Equal(t, int64(2), out.(map[string]any)["plusOne"])
}

func TestInteg_InlineExpr_NavigatesVariableField(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s as v -> t.sum = (v.a + v.b);
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)
	out, err := transform.New().Transform(context.Background(), sm, map[string]any{"a": 3, "b": 4})
	require.NoError(t, err)
	assert.Equal(t, int64(7), out.(map[string]any)["sum"])
}

// $this binds to the context arg in evaluate(), so $this + 2 evaluates correctly.
func TestInteg_ExplicitEvaluate_FromFMLText(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s.value as v -> t.out = evaluate(v, $this + 2);
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)
	out, err := transform.New().Transform(context.Background(), sm, map[string]any{"value": 5})
	require.NoError(t, err)
	assert.Equal(t, int64(7), out.(map[string]any)["out"])
}

func TestInteg_InlineExpr_WithDecimalAndUnary(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s.value as v -> t.scaled = (v * -2.5);
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)
	out, err := transform.New().Transform(context.Background(), sm, map[string]any{"value": 10})
	require.NoError(t, err)
	assert.Equal(t, float64(-25), out.(map[string]any)["scaled"])
}
