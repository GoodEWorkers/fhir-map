package fml

import "testing"

// FML comparison-operator lexing. These appear only inside captured FHIRPath
// expressions (where/check/inline), so the lexer must emit them as tokens for
// captureParenBalanced to reconstruct; the FHIRPath engine evaluates them.
// The single `<`/`>` must not cannibalise the `<<`/`>>` group type-mode
// markers, so order matters: `<<` before `<=` before `<`.
func TestLex_ComparisonOperators(t *testing.T) {
	cases := []struct {
		src  string
		want tokKind
	}{
		{"<", tLt},
		{"<=", tLe},
		{">", tGt},
		{">=", tGe},
	}
	for _, c := range cases {
		toks, err := tokenize(c.src)
		if err != nil {
			t.Fatalf("tokenize(%q): %v", c.src, err)
		}
		if len(toks) != 2 || toks[0].kind != c.want || toks[0].text != c.src {
			t.Fatalf("tokenize(%q) = %+v, want single %v %q", c.src, toks, c.want, c.src)
		}
	}
}

// FHIRPath special variables ($this, $index, $total) must lex in FML text so
// captureParenBalanced can round-trip them to the FHIRPath engine. They are
// captured as a single identifier token including the `$` prefix.
func TestLex_DollarSpecialVariables(t *testing.T) {
	for _, name := range []string{"$this", "$index", "$total"} {
		toks, err := tokenize(name)
		if err != nil {
			t.Fatalf("tokenize(%q): %v", name, err)
		}
		if len(toks) != 2 || toks[0].kind != tIdent || toks[0].text != name {
			t.Fatalf("tokenize(%q) = %+v, want single tIdent %q", name, toks, name)
		}
	}
}

func TestLex_DollarThenPath(t *testing.T) {
	toks, err := tokenize("$this.value")
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if len(toks) != 4 || toks[0].text != "$this" || toks[1].kind != tDot || toks[2].text != "value" {
		t.Fatalf("tokenize($this.value) = %+v", toks)
	}
}

// Regression: `<<`/`>>` type-mode markers still lex as their own tokens and
// are not split into two single comparison tokens.
func TestLex_AngleAngle_StillIntact(t *testing.T) {
	toks, err := tokenize("<<>>")
	if err != nil {
		t.Fatalf("tokenize(<<>>): %v", err)
	}
	if len(toks) != 3 || toks[0].kind != tLAngleAngle || toks[1].kind != tRAngleAngle {
		t.Fatalf("tokenize(<<>>) = %+v, want tLAngleAngle tRAngleAngle tEOF", toks)
	}
}
