package hl7v2

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// HPRIM delimiter order — spec §6.2. HPRIM `~^\&` declares separators in opposite
// order to HL7v2 MSH-2: `~`=subfield, `^`=repeater (vs HL7v2's `^`=component, `~`=repetition).
// Parser discovers delimiters by position so it handles both correctly without hardcoding.
func TestParse_HPRIM_SubfieldSeparator(t *testing.T) {
	m, err := Parse("H|~^\\&|sender\rP|1|DOE~JOHN~MR\r")
	require.NoError(t, err)
	assert.Equal(t, "DOE", m["P-2-1"], "HPRIM components split on the declared subfield sep '~'")
	assert.Equal(t, "JOHN", m["P-2-2"])
	assert.Equal(t, "MR", m["P-2-3"])
}

// In HPRIM `~^\&`, '^' is the repeater (not component) — opposite of HL7v2.
func TestParse_HPRIM_RepeaterIsCaret(t *testing.T) {
	m, err := Parse("H|~^\\&|sender\rP|1|id1^id2\r")
	require.NoError(t, err)
	assert.Equal(t, []any{"id1", "id2"}, m["P-2"], "HPRIM '^' is the repeater → two repetitions, not components")
}

// Even with HL7v2 byte order (`^~\&`), parser discovers actual delimiters correctly.
func TestParse_HPRIM_HL7v2ByteOrderUnchanged(t *testing.T) {
	m, err := Parse("H|^~\\&|sender\rP|1|DOE^JOHN\r")
	require.NoError(t, err)
	assert.Equal(t, "^~\\&", m["H-1"], "encoding field opaque")
	assert.Equal(t, "DOE", m["P-2-1"], "component='^' here")
	assert.Equal(t, "JOHN", m["P-2-2"])
}
