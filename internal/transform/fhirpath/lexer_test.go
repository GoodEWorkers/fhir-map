package fhirpath

import "testing"

// FHIRPath grammar: DECIMAL is `[0-9]+ '.' [0-9]+` (digits required on both sides,
// no exponent). A dot only joins when a digit follows, distinguishing `1.5` (decimal)
// from `5.toString()` (integer-then-method). Matches reference FHIRLexer and FML lexer.

// lexKinds tokenizes src and returns token pairs, dropping trailing tEOF.
func lexKinds(t *testing.T, src string) []token {
	t.Helper()
	toks, err := tokenize(src)
	if err != nil {
		t.Fatalf("tokenize(%q): %v", src, err)
	}
	if len(toks) > 0 && toks[len(toks)-1].kind == tEOF {
		toks = toks[:len(toks)-1]
	}
	return toks
}

func TestLex_DecimalLiteral(t *testing.T) {
	for _, src := range []string{"1.5", "0.5", "3.14159"} {
		toks := lexKinds(t, src)
		if len(toks) != 1 || toks[0].kind != tDecimal || toks[0].text != src {
			t.Fatalf("tokenize(%q) = %+v, want single tDecimal %q", src, toks, src)
		}
	}
}

// `5.toString()` is NOT a decimal: the `.` is followed by a letter, so it
// stays an integer followed by a method call. Regression guard for the
// disambiguation rule.
func TestLex_IntegerThenMethod_NotDecimal(t *testing.T) {
	toks := lexKinds(t, "5.toString()")
	want := []token{
		{tInteger, "5"},
		{tDot, "."},
		{tIdent, "toString"},
		{tLParen, "("},
		{tRParen, ")"},
	}
	if len(toks) != len(want) {
		t.Fatalf("tokenize(5.toString()) = %+v, want %+v", toks, want)
	}
	for i, w := range want {
		if toks[i].kind != w.kind || toks[i].text != w.text {
			t.Fatalf("token[%d] = %+v, want %+v", i, toks[i], w)
		}
	}
}

func TestLex_PlainInteger_StillInteger(t *testing.T) {
	toks := lexKinds(t, "42")
	if len(toks) != 1 || toks[0].kind != tInteger || toks[0].text != "42" {
		t.Fatalf("tokenize(42) = %+v, want single tInteger 42", toks)
	}
}

// A decimal inside an arithmetic expression: `1.5 + 2` must be three tokens
// (decimal, plus, integer), not a misread of `1 . 5 + 2`.
func TestLex_DecimalInArithmetic(t *testing.T) {
	toks := lexKinds(t, "1.5 + 2")
	want := []token{{tDecimal, "1.5"}, {tPlus, "+"}, {tInteger, "2"}}
	if len(toks) != len(want) {
		t.Fatalf("tokenize(1.5 + 2) = %+v, want %+v", toks, want)
	}
	for i, w := range want {
		if toks[i].kind != w.kind || toks[i].text != w.text {
			t.Fatalf("token[%d] = %+v, want %+v", i, toks[i], w)
		}
	}
}

// Regression: HL7v2 hyphen paths and collection indexers must not be
// disturbed by decimal scanning — neither contains a digit-dot-digit run.
func TestLex_HL7PathAndIndex_Unaffected(t *testing.T) {
	toks := lexKinds(t, "MSH-9")
	if len(toks) != 1 || toks[0].kind != tIdent || toks[0].text != "MSH-9" {
		t.Fatalf("tokenize(MSH-9) = %+v, want single tIdent", toks)
	}
	toks = lexKinds(t, "value[0]")
	want := []token{{tIdent, "value"}, {tLBracket, "["}, {tInteger, "0"}, {tRBracket, "]"}}
	if len(toks) != len(want) {
		t.Fatalf("tokenize(value[0]) = %+v, want %+v", toks, want)
	}
	for i, w := range want {
		if toks[i].kind != w.kind || toks[i].text != w.text {
			t.Fatalf("token[%d] = %+v, want %+v", i, toks[i], w)
		}
	}
}
