package fhirpath

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// HL7v2 component / subcomponent navigation after an index: `field[rep]-C` splits
// the field on '^', `field[rep]-C-S` splits component C on '&'. Used by source
// paths like PID-2[0]-1 and PID-2[0]-4-1.
func TestEval_HL7v2ComponentNavigation(t *testing.T) {
	subj := map[string]any{
		"f": "DOE^JANE^^^MR^sub1&sub2", // a 6-component field; component 6 has subcomponents
	}
	cases := []struct {
		expr string
		want []any
	}{
		{"f[0]-1", []any{"DOE"}},
		{"f[0]-2", []any{"JANE"}},
		{"f[0]-5", []any{"MR"}},
		{"f[0]-3", nil},             // empty component -> absent
		{"f[0]-6-1", []any{"sub1"}}, // subcomponent
		{"f[0]-6-2", []any{"sub2"}},
		{"f[0]-9", nil}, // out of range
	}
	for _, c := range cases {
		got, err := Eval(c.expr, subj)
		require.NoError(t, err, c.expr)
		assert.Equal(t, c.want, got, c.expr)
	}
}

// The component step must NOT swallow ordinary subtraction: `a - 1` (where a is a
// plain identifier, not an index/component) stays arithmetic.
func TestEval_ComponentStepDoesNotBreakSubtraction(t *testing.T) {
	got, err := Eval("a - 1", map[string]any{"a": 5})
	require.NoError(t, err)
	assert.Equal(t, []any{int64(4)}, got)
}
