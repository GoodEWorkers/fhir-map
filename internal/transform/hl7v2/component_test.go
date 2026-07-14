package hl7v2

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParse_ComponentsSubcomponentsEmptyAndPerRow tests component and subcomponent key access, empty field handling, and per-row keyed objects.
func TestParse_ComponentsSubcomponentsEmptyAndPerRow(t *testing.T) {
	m, err := Parse("PID|1||id||DOE^JANE^^^MR^a&b")
	require.NoError(t, err)

	assert.Equal(t, "DOE^JANE^^^MR^a&b", m["PID-5"])
	assert.Equal(t, "DOE", m["PID-5-1"])
	assert.Equal(t, "JANE", m["PID-5-2"])
	assert.Equal(t, "MR", m["PID-5-5"])
	assert.Equal(t, "a", m["PID-5-6-1"])
	assert.Equal(t, "b", m["PID-5-6-2"])

	// empty field is ABSENT, not "" (S7b)
	_, hasPID2 := m["PID-2"]
	assert.False(t, hasPID2, "empty PID-2 must be absent")
	_, hasComp3 := m["PID-5-3"]
	assert.False(t, hasComp3, "empty component PID-5-3 must be absent")

	// per-row keyed object: source.PID as obj -> obj.PID-3 navigates
	rows, ok := m["PID"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	row, ok := rows[0].(map[string]any)
	require.True(t, ok, "each segment occurrence is a keyed object")
	assert.Equal(t, "id", row["PID-3"])
	assert.Equal(t, "DOE", row["PID-5-1"])
}
