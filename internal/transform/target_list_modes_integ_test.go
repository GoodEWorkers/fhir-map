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

// Runtime semantics of Target.ListMode, grounded in HAPI's StructureMapUtilities.

// mode applied to a rule that fires once per source item.
func transformItems(t *testing.T, mode string, items []any) (any, error) {
	t.Helper()
	src := `map "u" = "n"
group G(source s, target t) {
  s.items as v -> t.out = copy(v) ` + mode + `;
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)
	return transform.New().Transform(context.Background(), sm, map[string]any{"items": items})
}

// share: many source items accumulate into one shared instance; without share, later firings overwrite earlier ones.
func TestInteg_TargetShare_CollatesIntoOneInstance(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s.given as g -> t.name = create('HumanName') as n share nameRule, n.given = g;
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)
	out, err := transform.New().Transform(context.Background(), sm,
		map[string]any{"given": []any{"Ada", "Augusta"}})
	require.NoError(t, err)
	name, ok := out.(map[string]any)["name"].(map[string]any)
	require.True(t, ok, "expected a single shared HumanName, got %#v", out.(map[string]any)["name"])
	assert.Equal(t, []any{"Ada", "Augusta"}, name["given"])
}

func TestInteg_TargetListMode_Collate(t *testing.T) {
	out, err := transformItems(t, "collate", []any{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, []any{"a", "b"}, out.(map[string]any)["out"])

	out, err = transformItems(t, "collate", []any{"a"})
	require.NoError(t, err)
	assert.Equal(t, []any{"a"}, out.(map[string]any)["out"], "collate forces a list even for one item")
}

func TestInteg_TargetListMode_First(t *testing.T) {
	out, err := transformItems(t, "first", []any{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, "a", out.(map[string]any)["out"])
}

func TestInteg_TargetListMode_Last(t *testing.T) {
	out, err := transformItems(t, "last", []any{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, "b", out.(map[string]any)["out"])
}

func TestInteg_TargetListMode_Single(t *testing.T) {
	out, err := transformItems(t, "single", []any{"a"})
	require.NoError(t, err)
	assert.Equal(t, "a", out.(map[string]any)["out"])

	_, err = transformItems(t, "single", []any{"a", "b"})
	require.Error(t, err, "two writes under single must error")
	assert.True(t, errors.Is(err, transform.ErrTargetListSingle))
}

// Regression: no list mode preserves the promote-on-repeat behavior (scalar on first write, list on repeat).
func TestInteg_TargetListMode_NoModeUnchanged(t *testing.T) {
	out, err := transformItems(t, "", []any{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, []any{"a", "b"}, out.(map[string]any)["out"])

	out, err = transformItems(t, "", []any{"a"})
	require.NoError(t, err)
	assert.Equal(t, "a", out.(map[string]any)["out"], "single firing stays scalar without a mode")
}
