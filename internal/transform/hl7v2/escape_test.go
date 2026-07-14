package hl7v2

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Escape unescaping — spec §5.6. Done with a left-to-right scanner at leaf
// access (never search-and-replace), AFTER tokenization, so escaped delimiters
// never participate in splitting.

func TestUnescape_DelimiterEscapes(t *testing.T) {
	d := defaultDelims()
	assert.Equal(t, "a|b", unescape("a\\F\\b", d), "\\F\\ -> field separator")
	assert.Equal(t, "a^b", unescape("a\\S\\b", d), "\\S\\ -> component separator")
	assert.Equal(t, "a&b", unescape("a\\T\\b", d), "\\T\\ -> subcomponent separator")
	assert.Equal(t, "a~b", unescape("a\\R\\b", d), "\\R\\ -> repetition separator")
	assert.Equal(t, "a\\b", unescape("a\\E\\b", d), "\\E\\ -> the escape character itself")
}

// The classic recorruption test: a naive strings.Replace(\E\ -> \) then
// reinterpreting would corrupt the following \F\. The scanner must consume each
// sequence atomically.
func TestUnescape_NoRecorruption(t *testing.T) {
	assert.Equal(t, "a\\b|c", unescape("a\\E\\b\\F\\c", defaultDelims()))
}

func TestUnescape_Hex(t *testing.T) {
	d := defaultDelims()
	assert.Equal(t, "\r", unescape("\\X0D\\", d))
	assert.Equal(t, "\r\n", unescape("\\X0D0A\\", d))
	// odd / malformed hex preserved verbatim rather than dropped or panicking.
	assert.Equal(t, "\\Xzz\\", unescape("\\Xzz\\", d))
}

func TestUnescape_FormattingAndLocal(t *testing.T) {
	d := defaultDelims()
	assert.Equal(t, "line1\nline2", unescape("line1\\.br\\line2", d), "\\.br\\ -> newline")
	assert.Equal(t, "ab", unescape("a\\.sp\\b", d), "other formatting escapes stripped")
	assert.Equal(t, "\\Zlocal\\", unescape("\\Zlocal\\", d), "local \\Z..\\ preserved verbatim")
}

func TestUnescape_FastPathAndUnterminated(t *testing.T) {
	d := defaultDelims()
	assert.Equal(t, "plain value", unescape("plain value", d), "no escape char -> unchanged")
	// unterminated escape: keep the remainder verbatim, do not truncate.
	assert.Equal(t, "ok\\Xff", unescape("ok\\Xff", d))
}

// At parse time: an escaped field separator must NOT split the field, and the
// leaf value is unescaped.
func TestParse_UnescapeAtLeaf(t *testing.T) {
	m, err := Parse("MSH|^~\\&|FAC\rNTE|1|a\\F\\b\r")
	require.NoError(t, err)
	assert.Equal(t, "a|b", m["NTE-2"], "escaped \\F\\ becomes a literal pipe in one field, not two fields")
}

// THE GUARD: the encoding-characters field (MSH-1 / H-1 = ^~\&) literally
// contains the escape char and MUST NOT be unescaped — it stays the scalar
// "^~\&".
func TestParse_EncodingFieldNotUnescaped(t *testing.T) {
	mMSH, err := Parse("MSH|^~\\&|FAC\r")
	require.NoError(t, err)
	assert.Equal(t, "^~\\&", mMSH["MSH-1"], "MSH encoding field is opaque, not unescaped")

	mH, err := Parse("H|^~\\&||LMX5\r")
	require.NoError(t, err)
	assert.Equal(t, "^~\\&", mH["H-1"], "HPRIM H encoding field is opaque, not unescaped")
}
