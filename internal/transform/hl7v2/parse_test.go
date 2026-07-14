package hl7v2

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParse_HPRIM exercises HPRIM message parsing against a real-world example.
func TestParse_HPRIM(t *testing.T) {
	src := `H|^~\&||LMX5|LABOC||ORU|||CRIH38^CHU Grenoble||P|H2.1^C|20251219145056
P|0001|93038553||50000810305847|DOE^JOHN^^^MR|DOE|19700101|M||2 RUE DU COLOMBIER`

	m, err := Parse(src)
	require.NoError(t, err)
	assert.Equal(t, "^~\\&", m["H-1"])
	_, hasH2 := m["H-2"]
	assert.False(t, hasH2, "empty HL7v2 field is absent, not an empty-string value (S7b)")
	assert.Equal(t, "LMX5", m["H-3"])
	assert.Equal(t, "LABOC", m["H-4"])
	assert.Equal(t, "ORU", m["H-6"])
	assert.Equal(t, "0001", m["P-1"])
	assert.Equal(t, "DOE^JOHN^^^MR", m["P-5"])
}

func TestIsHL7v2Binary_AcceptsHPRIM(t *testing.T) {
	plain := "H|^~\\&||LMX5\r\nP|0001|||"
	bin := map[string]any{
		"resourceType": "Binary",
		"contentType":  "application/json",
		"data":         base64.StdEncoding.EncodeToString([]byte(plain)),
	}
	assert.True(t, IsHL7v2Binary(bin))
}

func TestIsHL7v2Binary_RejectsNonHL7(t *testing.T) {
	bin := map[string]any{
		"resourceType": "Binary",
		"data":         base64.StdEncoding.EncodeToString([]byte("just plain text")),
	}
	assert.False(t, IsHL7v2Binary(bin))
}

// TestParse_MultipleSegmentsExposedAsList verifies that multiple same-name segments
// are exposed as a list for iteration, while field-indexed keys keep first-occurrence semantics.
func TestParse_MultipleSegmentsExposedAsList(t *testing.T) {
	src := `H|^~\&||LMX5
P|0001|||
OBR|1|||CRP|||20251219145056
OBR|2|||GLU|||20251219145100
OBR|3|||NA|||20251219145105
OBX|1|N|CRP|6.2|`

	m, err := Parse(src)
	require.NoError(t, err)

	obrs, ok := m["OBR"].([]any)
	require.Truef(t, ok, "OBR must be a list of rows when multiple OBR segments are present; got %T", m["OBR"])
	require.Len(t, obrs, 3)

	// Each row is a keyed object exposing SEG-N field paths for iteration.
	for i, want := range []string{"CRP", "GLU", "NA"} {
		row, rowOk := obrs[i].(map[string]any)
		require.Truef(t, rowOk, "OBR[%d] must be a keyed row object, got %T", i, obrs[i])
		assert.Equalf(t, want, row["OBR-4"], "OBR[%d] field 4 (test code)", i)
	}

	// Single segments are also exposed as one-element lists (uniform shape).
	ps, ok := m["P"].([]any)
	require.Truef(t, ok, "P should also be a list shape, got %T", m["P"])
	require.Len(t, ps, 1)

	// Field-indexed keys keep first-occurrence semantics for back-compat.
	assert.Equal(t, "1", m["OBR-1"])
}

func TestAdaptBinary_DecodesBase64(t *testing.T) {
	plain := "H|^~\\&||LMX5"
	bin := map[string]any{
		"resourceType": "Binary",
		"data":         base64.StdEncoding.EncodeToString([]byte(plain)),
	}
	m, err := AdaptBinary(bin)
	require.NoError(t, err)
	assert.Equal(t, "LMX5", m["H-3"])
}
