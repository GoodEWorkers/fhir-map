package fhirpath

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Collection indexer expr[N] — needed for HL7v2 source paths like PID-2[0].
func TestEval_Indexer(t *testing.T) {
	subj := map[string]any{
		"items":  []any{"a", "b", "c"},
		"scalar": "x", // a single field reads as a 1-element collection
	}
	cases := []struct {
		expr string
		want []any
	}{
		{"items[0]", []any{"a"}},
		{"items[2]", []any{"c"}},
		{"scalar[0]", []any{"x"}},       // index 0 of a singleton is the value
		{"items[9]", nil},               // out of range -> empty
		{"items[0] = 'a'", []any{true}}, // composes with operators
	}
	for _, c := range cases {
		got, err := Eval(c.expr, subj)
		require.NoError(t, err, c.expr)
		assert.Equal(t, c.want, got, c.expr)
	}
}
