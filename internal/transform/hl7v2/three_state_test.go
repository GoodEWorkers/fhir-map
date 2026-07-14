package hl7v2

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Three-state field model — spec §5.5. A leaf between delimiters is one of:
// populated (value), absent (nothing → no key, .exists()=false), or explicit
// null (the two-character `""` → "delete/null"). The parser must NOT collapse
// absent and explicit-null. Explicit null is recorded on an inert SEG-N#null
// sidecar (the SEG-N value key stays absent, so the literal `""` never leaks as
// a value and .exists() on the value is false).

func TestParse_ThreeState_PopulatedAbsentNull(t *testing.T) {
	m, err := Parse("MSH|^~\\&|FAC\rPID|val||\"\"\r")
	require.NoError(t, err)

	assert.Equal(t, "val", m["PID-1"], "populated")

	_, hasP2 := m["PID-2"]
	assert.False(t, hasP2, "absent field emits no value key")
	_, hasP2null := m["PID-2#null"]
	assert.False(t, hasP2null, "absent is NOT null")

	_, hasP3 := m["PID-3"]
	assert.False(t, hasP3, "explicit-null emits no literal value key")
	assert.Equal(t, true, m["PID-3#null"], "explicit-null flagged on the sidecar")
}

func TestParse_ThreeState_ComponentNull(t *testing.T) {
	m, err := Parse("MSH|^~\\&|FAC\rPID|||Smith^\"\"^John\r")
	require.NoError(t, err)
	assert.Equal(t, "Smith", m["PID-3-1"])
	_, has := m["PID-3-2"]
	assert.False(t, has, "null component emits no value key")
	assert.Equal(t, true, m["PID-3-2#null"])
	assert.Equal(t, "John", m["PID-3-3"])
}

// The sidecar key must be invisible to the ER7 serializer (not a segment or field key).
func TestParse_ThreeState_SidecarNotASegmentOrField(t *testing.T) {
	assert.False(t, isSegmentName("PID-3#null"))
	_, _, ok := splitFieldKey("PID-3#null")
	assert.False(t, ok, "sidecar key is not a SEG-N field key")
}

// The opaque encoding field is never null-checked.
func TestParse_ThreeState_EncodingFieldUnaffected(t *testing.T) {
	m, err := Parse("MSH|^~\\&|FAC\r")
	require.NoError(t, err)
	assert.Equal(t, "^~\\&", m["MSH-1"])
	_, hasNull := m["MSH-1#null"]
	assert.False(t, hasNull)
}
