package hl7v2

import (
	"strconv"
	"strings"
)

// unescape resolves HL7v2 escape sequences in a leaf value using a single
// left-to-right scanner. It runs at leaf-value access — AFTER tokenization — so
// escaped delimiters never participated in splitting. Spec §5.6.
func unescape(s string, d Delims) string {
	esc := d.Escape
	if strings.IndexByte(s, esc) < 0 {
		return s // fast path: no escape sequences (the common case)
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != esc {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i+1:], esc)
		if end < 0 {
			b.WriteString(s[i:]) // unterminated — keep verbatim
			break
		}
		seq := s[i+1 : i+1+end]
		b.WriteString(decodeEscape(seq, d))
		i = i + 1 + end + 1 // advance past the closing escape char
	}
	return b.String()
}

// decodeEscape maps the content between a pair of escape characters to its
// replacement text. Unknown or local (\Z..\) codes are preserved verbatim
// (delimited by the escape char) so no data is lost.
func decodeEscape(seq string, d Delims) string {
	verbatim := string(d.Escape) + seq + string(d.Escape)
	if seq == "" {
		return verbatim
	}
	switch seq[0] {
	case 'F':
		return string(d.Field)
	case 'S':
		return string(d.Component)
	case 'T':
		return string(d.Subcomponent)
	case 'R':
		return string(d.Repetition)
	case 'E':
		return string(d.Escape)
	case 'X':
		if hex, ok := decodeHex(seq[1:]); ok {
			return hex
		}
		return verbatim // malformed hex — preserve rather than drop
	case '.':
		if seq == ".br" {
			return "\n"
		}
		return "" // other formatting escapes (\.sp\, \.fi\, …) are stripped
	case 'H', 'N', 'C', 'M':
		return "" // highlight on/off and charset switches are stripped
	default:
		return verbatim // \Z..\ local codes and anything unrecognised
	}
}

// decodeHex turns an even-length string of hex digit pairs into raw bytes
// (\X0D\ -> CR, \X0D0A\ -> CR LF). Returns ok=false for malformed input.
func decodeHex(h string) (string, bool) {
	if h == "" || len(h)%2 != 0 {
		return "", false
	}
	var b strings.Builder
	b.Grow(len(h) / 2)
	for i := 0; i < len(h); i += 2 {
		v, err := strconv.ParseUint(h[i:i+2], 16, 8)
		if err != nil {
			return "", false
		}
		b.WriteByte(byte(v))
	}
	return b.String(), true
}
