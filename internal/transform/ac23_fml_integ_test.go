package transform_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/transform"
	"github.com/goodeworkers/fhir-map/internal/transform/fml"
)

// TestAC2_RecursionCap_FromFMLText tests ErrRecursionLimit through the FML parser;
// this ensures parser regressions in lowering `then group(...)` surface here.
func TestAC2_RecursionCap_FromFMLText(t *testing.T) {
	src := `map "http://example.org/fml/recursion" = "Recursion"
group main(source src, target tgt) {
  src -> tgt then forward(src, tgt);
}
group forward(source src, target tgt) {
  src -> tgt then forward(src, tgt);
}
`
	sm, err := fml.Parse(src)
	require.NoError(t, err)

	eng := transform.New()
	source := map[string]any{"resourceType": "Patient", "id": "pat-1", "active": true}
	_, err = eng.Transform(context.Background(), sm, source)
	require.Error(t, err, "self-recursive forward must trip the recursion cap")
	require.True(t, errors.Is(err, transform.ErrRecursionLimit),
		"FML-driven self-recursion must wrap ErrRecursionLimit; got: %v", err)
}

// ErrCheckFailed is tested via hand-built AST in executor_test.go;
// FML text equivalent omitted since the grammar doesn't yet lower `check (expr)`
// into `Source.Check` — that field is populated programmatically.
