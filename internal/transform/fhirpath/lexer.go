// Package fhirpath implements the subset of FHIRPath the FHIR mapping
// language (FML) needs at runtime — path navigation, equality, boolean
// ops, the where/exists/empty/first/last/count predicates, and a handful
// of string functions.
//
// This is deliberately a small evaluator that supports just the
// expressions appearing in the official structuremap-example-*.json
// corpus, not the full FHIRPath spec. The package is designed to be
// extended one operator at a time as broader fixtures land.
package fhirpath

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// tokenKind enumerates the lexer's lexemes. Names match the FHIRPath spec
// where possible so a reader who knows FHIRPath finds the right operator
// quickly.
type tokenKind int

const (
	tIdent tokenKind = iota
	tInteger
	tDecimal
	tString
	tBool
	tDot
	tLParen
	tRParen
	tEq
	tComma
	tAnd
	tOr
	tNot
	tExtConst // %name external constant; Text carries the bare name
	tPipe     // `|` separator inside union literals (a | b | c)
	tLt       // `<`
	tLe       // `<=`
	tGt       // `>`
	tGe       // `>=`
	tNe       // `!=`
	tEqv      // `~` (loose equivalence: case-insensitive strings)
	tNeqv     // `!~`
	tPlus     // `+`
	tMinus    // `-` (standalone; hyphens inside identifiers are eaten by isIdentPart)
	tStar     // `*`
	tSlash    // `/`
	tAmp      // `&` (string concat)
	tLBracket
	tRBracket
	tEOF
)

type token struct {
	kind tokenKind
	text string
}

// tokenize returns the lexeme stream for an expression. Whitespace and
// comments are dropped. Errors are returned at parse time, not here —
// the lexer never produces invalid tokens, it just refuses to advance.
func tokenize(src string) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '.':
			out = append(out, token{tDot, "."})
			i++
		case c == '(':
			out = append(out, token{tLParen, "("})
			i++
		case c == ')':
			out = append(out, token{tRParen, ")"})
			i++
		case c == ',':
			out = append(out, token{tComma, ","})
			i++
		case c == '=':
			out = append(out, token{tEq, "="})
			i++
		case c == '<':
			if i+1 < len(src) && src[i+1] == '=' {
				out = append(out, token{tLe, "<="})
				i += 2
				continue
			}
			out = append(out, token{tLt, "<"})
			i++
		case c == '>':
			if i+1 < len(src) && src[i+1] == '=' {
				out = append(out, token{tGe, ">="})
				i += 2
				continue
			}
			out = append(out, token{tGt, ">"})
			i++
		case c == '!':
			if i+1 >= len(src) {
				return nil, fmt.Errorf("dangling '!' at offset %d", i)
			}
			switch src[i+1] {
			case '=':
				out = append(out, token{tNe, "!="})
				i += 2
			case '~':
				out = append(out, token{tNeqv, "!~"})
				i += 2
			default:
				return nil, fmt.Errorf("expected '=' or '~' after '!' at offset %d", i)
			}
		case c == '~':
			out = append(out, token{tEqv, "~"})
			i++
		case c == '|':
			out = append(out, token{tPipe, "|"})
			i++
		case c == '+':
			out = append(out, token{tPlus, "+"})
			i++
		case c == '-':
			// `-` at a token boundary becomes tMinus; hyphens inside identifiers (MSH-3-2) stay part of tIdent.
			out = append(out, token{tMinus, "-"})
			i++
		case c == '*':
			out = append(out, token{tStar, "*"})
			i++
		case c == '/':
			out = append(out, token{tSlash, "/"})
			i++
		case c == '&':
			out = append(out, token{tAmp, "&"})
			i++
		case c == '[':
			out = append(out, token{tLBracket, "["})
			i++
		case c == ']':
			out = append(out, token{tRBracket, "]"})
			i++
		case c == '\'' || c == '"':
			end := strings.IndexByte(src[i+1:], c)
			if end < 0 {
				return nil, fmt.Errorf("unterminated string starting at offset %d", i)
			}
			out = append(out, token{tString, src[i+1 : i+1+end]})
			i = i + 2 + end
		case unicode.IsDigit(rune(c)):
			j := i
			for j < len(src) && unicode.IsDigit(rune(src[j])) {
				j++
			}
			// FHIRPath DECIMAL is `[0-9]+ '.' [0-9]+`: the '.' only joins the
			// number when a digit follows it. That keeps the decimal `1.5`
			// distinct from the integer-then-method `5.toString()` (where the
			// '.' is followed by a letter and stays a separate tDot). This
			// matches the reference org.hl7.fhir FHIRLexer and the FML lexer.
			if j+1 < len(src) && src[j] == '.' && unicode.IsDigit(rune(src[j+1])) {
				j++ // consume '.'
				for j < len(src) && unicode.IsDigit(rune(src[j])) {
					j++
				}
				out = append(out, token{tDecimal, src[i:j]})
				i = j
				continue
			}
			out = append(out, token{tInteger, src[i:j]})
			i = j
		case c == '%':
			// FHIRPath external constant: '%' identifier or '%' STRING.
			// Token text is the bare name without the '%'.
			j := i + 1
			if j < len(src) && (src[j] == '\'' || src[j] == '"') {
				q := src[j]
				end := strings.IndexByte(src[j+1:], q)
				if end < 0 {
					return nil, fmt.Errorf("unterminated %% string starting at offset %d", i)
				}
				out = append(out, token{tExtConst, src[j+1 : j+1+end]})
				i = j + 2 + end
				continue
			}
			for j < len(src) && isIdentPart(rune(src[j])) {
				j++
			}
			if j == i+1 {
				return nil, fmt.Errorf("expected identifier after %% at offset %d", i)
			}
			out = append(out, token{tExtConst, src[i+1 : j]})
			i = j
		case c == '$':
			// FHIRPath `$this` operator; captured as an identifier with evaluator special-casing the leading $.
			j := i + 1
			for j < len(src) && isIdentPart(rune(src[j])) {
				j++
			}
			out = append(out, token{tIdent, src[i:j]})
			i = j
		case isIdentStart(rune(c)):
			j := i
			for j < len(src) && isIdentPart(rune(src[j])) {
				j++
			}
			text := src[i:j]
			switch text {
			case "true", "false":
				out = append(out, token{tBool, text})
			case "and":
				out = append(out, token{tAnd, text})
			case "or":
				out = append(out, token{tOr, text})
			case "not":
				out = append(out, token{tNot, text})
			default:
				out = append(out, token{tIdent, text})
			}
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q at offset %d in %q", c, i, src)
		}
	}
	out = append(out, token{tEOF, ""})
	return out, nil
}

// isIdentStart and isIdentPart implement the identifier rule.
//
// Standard FHIRPath allows letters, digits, and underscore. We additionally
// allow `-` *inside* identifiers (never as the first character) so HL7v2
// segment-field paths like `H-2`, `MSH-9`, `PID-3` lex as single tokens.
// Strict FHIRPath would reject this, but the HAPI-flavoured StructureMap
// fixtures we compatibility-test against use the hyphen heavily.
//
// FHIRPath subtraction is not supported by our subset, so there's no
// ambiguity with arithmetic minus. If subtraction is ever added, the
// lexer should switch back to strict spec-conformance and require the
// HL7v2 path strings to be quoted.
func isIdentStart(r rune) bool { return unicode.IsLetter(r) || r == '_' }
func isIdentPart(r rune) bool  { return isIdentStart(r) || unicode.IsDigit(r) || r == '-' }

func parseInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
