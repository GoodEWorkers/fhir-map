package hl7v2

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Field repetition (~) — spec §5.7. Repetition separator splits FIELDS only; repeating fields become []any, single-repetition fields stay scalar. Components come from the first repetition.

func TestParse_FieldRepetition(t *testing.T) {
	m, err := Parse("MSH|^~\\&|FAC\rPID|1|id1^auth1~id2^auth2\r")
	require.NoError(t, err)
	assert.Equal(t, []any{"id1^auth1", "id2^auth2"}, m["PID-2"], "two repetitions modeled as a list")
	// components come off the FIRST repetition, never split on '~'
	assert.Equal(t, "id1", m["PID-2-1"])
	assert.Equal(t, "auth1", m["PID-2-2"])
}

func TestParse_SingleRepetitionStaysScalar(t *testing.T) {
	m, err := Parse("MSH|^~\\&|FAC\rPID|1|solo\r")
	require.NoError(t, err)
	assert.Equal(t, "solo", m["PID-2"], "non-repeating field stays a scalar string, not a list")
}

// Escaped repetition (\R\) must NOT split the field; it is one field with a literal '~'.
func TestParse_EscapedRepetitionNotSplit(t *testing.T) {
	m, err := Parse("MSH|^~\\&|FAC\rPID|1|a\\R\\b\r")
	require.NoError(t, err)
	assert.Equal(t, "a~b", m["PID-2"], "\\R\\ is a literal tilde in one field, not a repetition split")
}

// GUARD: encoding field (MSH-1 / H-1 = ^~\&) is not repetition-split despite containing '~'.
func TestParse_EncodingFieldNotRepetitionSplit(t *testing.T) {
	m, err := Parse("MSH|^~\\&|FAC\r")
	require.NoError(t, err)
	assert.Equal(t, "^~\\&", m["MSH-1"], "encoding field stays scalar despite containing '~'")
	mH, err := Parse("H|^~\\&||LMX5\r")
	require.NoError(t, err)
	assert.Equal(t, "^~\\&", mH["H-1"])
}
