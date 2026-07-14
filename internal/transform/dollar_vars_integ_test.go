package transform_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/transform"
	"github.com/goodeworkers/fhir-map/internal/transform/fml"
)

// TestInteg_DollarThisCondition_FromFMLText verifies that $this inside an FML where(...) guard is evaluated end-to-end.
func TestInteg_DollarThisCondition_FromFMLText(t *testing.T) {
	src := `map "u" = "n"
group G(source s, target t) {
  s.value as v where ($this >= 5) -> t.out = copy(v);
}`
	sm, err := fml.Parse(src)
	require.NoError(t, err)

	eng := transform.New()

	out, err := eng.Transform(context.Background(), sm, map[string]any{"value": 10})
	require.NoError(t, err)
	assert.Equal(t, 10, out.(map[string]any)["out"], "10 >= 5 → rule fires")

	out2, err := eng.Transform(context.Background(), sm, map[string]any{"value": 3})
	require.NoError(t, err)
	_, present := out2.(map[string]any)["out"]
	assert.False(t, present, "3 >= 5 is false → rule must not fire")
}
