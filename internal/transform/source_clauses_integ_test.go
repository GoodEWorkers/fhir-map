package transform_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/transform"
	"github.com/goodeworkers/fhir-map/internal/transform/fml"
)

// Source check: a satisfied assertion lets the rule fire; a violated one
// aborts the transform with ErrCheckFailed.
func TestInteg_SourceCheck_Enforced(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s.value as v check ($this > 0) -> t.out = copy(v);
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)

	out, err := transform.New().Transform(context.Background(), sm, map[string]any{"value": 5})
	require.NoError(t, err)
	assert.Equal(t, 5, out.(map[string]any)["out"])

	_, err = transform.New().Transform(context.Background(), sm, map[string]any{"value": -5})
	require.Error(t, err, "$this > 0 is false for -5 → check must fail")
	assert.True(t, errors.Is(err, transform.ErrCheckFailed))
}

func TestInteg_SourceListMode_First(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s.items first as v -> t.out = copy(v);
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)
	out, err := transform.New().Transform(context.Background(), sm,
		map[string]any{"items": []any{"a", "b", "c"}})
	require.NoError(t, err)
	assert.Equal(t, "a", out.(map[string]any)["out"])
}

// A source element type annotation parses and the rule still executes (the
// annotation is currently a no-op for the value-shaped engine).
func TestInteg_SourceElementType_ParsesAndRuns(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s.value : integer as v -> t.out = copy(v);
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)
	out, err := transform.New().Transform(context.Background(), sm, map[string]any{"value": 7})
	require.NoError(t, err)
	assert.Equal(t, 7, out.(map[string]any)["out"])
}

// A target list mode parses and the rule still executes (annotation captured;
// runtime honoring of target list modes is out of scope here).
func TestInteg_TargetListMode_ParsesAndRuns(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s.value as v -> t.out = copy(v) first;
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)
	out, err := transform.New().Transform(context.Background(), sm, map[string]any{"value": 5})
	require.NoError(t, err)
	assert.Equal(t, 5, out.(map[string]any)["out"])
}

func TestInteg_SourceDefault_FillsAbsentElement(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s.missing default ('fallback') as v -> t.out = copy(v);
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)
	// "missing" is absent (only an unrelated field present), so the default fires.
	out, err := transform.New().Transform(context.Background(), sm, map[string]any{"other": 1})
	require.NoError(t, err)
	assert.Equal(t, "fallback", out.(map[string]any)["out"])
}
