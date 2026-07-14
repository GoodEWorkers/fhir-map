package transform_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/transform"
	"github.com/goodeworkers/fhir-map/internal/transform/fml"
)

// TestInteg_NegativeLiteralArg_FromFMLText tests that a bare negative literal argument flows through the FML engine correctly.
func TestInteg_NegativeLiteralArg_FromFMLText(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s.value as v -> t.factor = copy(-2.5);
  s.value as v -> t.same = copy(v);
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)

	out, err := transform.New().Transform(context.Background(), sm, map[string]any{"value": 2})
	require.NoError(t, err)
	m := out.(map[string]any)
	assert.Equal(t, -2.5, m["factor"], "literal negative decimal arg")
	assert.Equal(t, 2, m["same"])
}
