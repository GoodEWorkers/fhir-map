package hl7v2

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Delimiter discovery: the parser must read the field separator (MSH-1) and
// encoding characters (MSH-2) from the message header instead of hardcoding
// ^~\& — spec §5.2. Conventional headers (^~\&) stay byte-identical (the whole
// existing suite is the regression guard); a non-default MSH-2 must drive the
// component/subcomponent splits.
func TestParse_DelimiterDiscovery_NonDefaultComponent(t *testing.T) {
	// MSH-2 = #~\& → component separator is '#', not '^'.
	m, err := Parse("MSH|#~\\&|FAC|x^y\rPID|1|a#b#c\r")
	require.NoError(t, err)
	assert.Equal(t, "a#b#c", m["PID-2"], "raw field value preserved")
	assert.Equal(t, "a", m["PID-2-1"], "components split on the discovered '#'")
	assert.Equal(t, "b", m["PID-2-2"])
	assert.Equal(t, "c", m["PID-2-3"])
	// '^' is NOT special under this header — a caret stays literal, not split.
	assert.Equal(t, "x^y", m["MSH-3-1"], "caret is not a component sep here, so no split")
}

func TestParse_DelimiterDiscovery_NonDefaultSubcomponent(t *testing.T) {
	// MSH-2 = ^~\$ → subcomponent separator is '$', not '&'.
	m, err := Parse("MSH|^~\\$|FAC\rPID|1|comp1^a$b\r")
	require.NoError(t, err)
	assert.Equal(t, "comp1", m["PID-2-1"])
	assert.Equal(t, "a", m["PID-2-2-1"], "subcomponents split on the discovered '$'")
	assert.Equal(t, "b", m["PID-2-2-2"])
}

// Default ^~\& header keeps the legacy '^'/'&' splits exactly (regression).
func TestParse_DelimiterDiscovery_DefaultUnchanged(t *testing.T) {
	m, err := Parse("MSH|^~\\&|FAC\rPID|1|a^b^c\r")
	require.NoError(t, err)
	assert.Equal(t, "a", m["PID-2-1"])
	assert.Equal(t, "b", m["PID-2-2"])
	assert.Equal(t, "c", m["PID-2-3"])
	assert.Equal(t, "^~\\&", m["MSH-1"], "encoding field stays the literal scalar")
}
