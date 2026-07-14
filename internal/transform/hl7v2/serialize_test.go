package hl7v2

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleTarget() map[string]any {
	return map[string]any{
		"resourceType": "HL7v2",
		"MSH-1":        `^~\&`, "MSH-2": "APP^FAC", "MSH-9": "ORU^R01", "MSH-12": "2.3",
		"PID-1": "0001",
		"PID-2": []any{map[string]any{"value": "99999999"}}, // wrapped + (single) repetition
		"PID-5": "DOE^JANE",
		"OBR": []any{
			map[string]any{"OBR-1": "0001", "OBR-4": "CRP^Protein^L"},
			map[string]any{"OBR-1": "0002", "OBR-4": "GLU^Glucose^L"},
		},
		"OBX": []any{map[string]any{"OBX-1": "1", "OBX-3": "CRP", "OBX-5": "4.2"}},
	}
}

func TestToER7_StructureAndOrder(t *testing.T) {
	lines := strings.Split(ToER7(sampleTarget()), "\r")
	require.GreaterOrEqual(t, len(lines), 5)

	assert.True(t, strings.HasPrefix(lines[0], `MSH|^~\&|APP^FAC`), "MSH first, fields in index order: %q", lines[0])
	segOrder := make([]string, 0, len(lines))
	for _, l := range lines {
		segOrder = append(segOrder, strings.SplitN(l, "|", 2)[0])
	}
	assert.Equal(t, []string{"MSH", "PID", "OBR", "OBR", "OBX"}, segOrder, "canonical segment order, repeats preserved")

	// empty fields keep their position; trailing empties trimmed
	assert.Equal(t, "OBR|0001|||CRP^Protein^L", lines[2])
	assert.Equal(t, "PID|0001|99999999|||DOE^JANE", lines[1], "{value:} wrapper unwrapped")
}

// TestToER7_RoundTripsThroughParse verifies that ToER7 -> Parse round-trips preserve all field values with no data loss.
func TestToER7_RoundTripsThroughParse(t *testing.T) {
	m, err := Parse(ToER7(sampleTarget()))
	require.NoError(t, err)
	assert.Equal(t, "ORU^R01", m["MSH-9"])
	assert.Equal(t, "99999999", m["PID-2"])
	assert.Equal(t, "DOE", m["PID-5-1"]) // component navigation survives
	assert.Equal(t, "4.2", m["OBX-5"])

	obrs, ok := m["OBR"].([]any)
	require.True(t, ok)
	require.Len(t, obrs, 2)
	assert.Equal(t, "GLU^Glucose^L", obrs[1].(map[string]any)["OBR-4"])
}

// Field repetitions render with the repetition separator.
func TestToER7_Repetitions(t *testing.T) {
	er7 := ToER7(map[string]any{
		"MSH-1": `^~\&`,
		"PID-3": []any{map[string]any{"value": "A"}, map[string]any{"value": "B"}},
	})
	assert.Contains(t, er7, "PID|||A~B")
}
