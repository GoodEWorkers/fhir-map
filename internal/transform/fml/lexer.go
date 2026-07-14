// Package fml parses the FHIR Mapping Language text form into a
// structuremap.StructureMap AST.
//
// The grammar implemented here covers the constructs the official FHIR
// corpus uses and that our executor knows how to run:
//
//	map "url" = "name"
//	uses "structure-url" alias name as mode
//	imports "url"
//	group name(input1 : Type, input2 : Type, ...) {
//	  source -> target [name];
//	}
//	source: context.element [where (cond)] [as variable]
//	target: context.element = transform(arg, ...)  [as variable]
//
// FHIRPath paths inside transform args are kept as-is (stored on the
// Parameter's ValueID) so the executor can re-evaluate them at run time
// against the bound source variable's value.
//
// This is not a complete FML parser — it intentionally stops where the
// official structuremap-example-*.json corpus stops. M5b is the parsing
// layer; broader grammar extensions get test-first additions as needed.
package fml

import (
	"fmt"
	"strings"
	"unicode"
)

type tokKind int

const (
	tIdent tokKind = iota
	tNumber
	tString // single- or double-quoted literal
	tLBrace // {
	tRBrace // }
	tLParen
	tRParen
	tColon
	tComma
	tSemi
	tDot
	tEq
	tEqEq        // M6.11: == (equivalent relationship inside conceptmap blocks)
	tArrow       // ->
	tBangEq      // !=
	tCardinality // M6.10: e.g. "0..1" / "0..*" / "1..*"
	tLAngleAngle // M6.10: <<
	tRAngleAngle // M6.10: >>
	tLt          // < (FHIRPath comparison inside captured expressions)
	tLe          // <=
	tGt          // >
	tGe          // >=
	tPlus        // M6.10: + (FHIRPath expressions inside (expr))
	tMinus       // M6.10: - (FHIRPath expressions inside (expr))
	tStar        // M6.10: * (FHIRPath expressions inside (expr))
	tSlash       // M6.10: / (FHIRPath expressions inside (expr))
	tDocComment  // M6.10: ///-prefixed line, body trimmed
	tEOF
	// Keywords carry their own kind for easy dispatch in the parser.
	tKwMap
	tKwUses
	tKwImports
	tKwGroup
	tKwExtends
	tKwSource
	tKwTarget
	tKwAs
	tKwWhere
	tKwCheck
	tKwLog
	tKwThen
	tKwTypes
	tKwTypeAndTypes
	tKwAlias
	tKwQueried
	tKwProduced
	tKwLet
	tKwConceptMap // M6.11: inline conceptmap declaration
	tKwPrefix     // M6.11: prefix binding inside a conceptmap block
)

var keywords = map[string]tokKind{
	"map":        tKwMap,
	"uses":       tKwUses,
	"imports":    tKwImports,
	"group":      tKwGroup,
	"extends":    tKwExtends,
	"source":     tKwSource,
	"target":     tKwTarget,
	"as":         tKwAs,
	"where":      tKwWhere,
	"check":      tKwCheck,
	"log":        tKwLog,
	"then":       tKwThen,
	"types":      tKwTypes,
	"type+types": tKwTypeAndTypes,
	"alias":      tKwAlias,
	"queried":    tKwQueried,
	"produced":   tKwProduced,
	"let":        tKwLet,
	"conceptmap": tKwConceptMap,
	"prefix":     tKwPrefix,
}

type tok struct {
	kind tokKind
	text string
	line int
}

// tokenize returns the lexeme stream. Whitespace and comments are dropped.
// Operators like `->` are matched eagerly so `-` isn't a standalone token.
func tokenize(src string) ([]tok, error) {
	var out []tok
	line := 1
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\n':
			line++
			i++
		case c == '/' && i+2 < len(src) && src[i+1] == '/' && src[i+2] == '/':
			// `///` doc comment. Body (trimmed) attaches to the next group/rule.
			j := i + 3
			for j < len(src) && src[j] != '\n' {
				j++
			}
			body := strings.TrimSpace(src[i+3 : j])
			out = append(out, tok{tDocComment, body, line})
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			j := i
			for j < len(src) && src[j] != '\n' {
				j++
			}
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			j := i + 2
			for j+1 < len(src) && (src[j] != '*' || src[j+1] != '/') {
				if src[j] == '\n' {
					line++
				}
				j++
			}
			if j+1 >= len(src) {
				return nil, fmt.Errorf("unterminated block comment starting at line %d", line)
			}
			i = j + 2
		case c == '-' && i+1 < len(src) && src[i+1] == '>':
			out = append(out, tok{tArrow, "->", line})
			i += 2
		case c == '<' && i+1 < len(src) && src[i+1] == '<':
			out = append(out, tok{tLAngleAngle, "<<", line})
			i += 2
		case c == '>' && i+1 < len(src) && src[i+1] == '>':
			out = append(out, tok{tRAngleAngle, ">>", line})
			i += 2
		// Single comparison operators. Matched AFTER `<<`/`>>` so the
		// group type-mode markers win. These appear only inside captured
		// FHIRPath expressions (where/check/inline); the FML grammar never
		// consumes them structurally — captureParenBalanced reconstructs them
		// from token text and the FHIRPath engine evaluates them.
		case c == '<' && i+1 < len(src) && src[i+1] == '=':
			out = append(out, tok{tLe, "<=", line})
			i += 2
		case c == '<':
			out = append(out, tok{tLt, "<", line})
			i++
		case c == '>' && i+1 < len(src) && src[i+1] == '=':
			out = append(out, tok{tGe, ">=", line})
			i += 2
		case c == '>':
			out = append(out, tok{tGt, ">", line})
			i++
		case c == '+':
			out = append(out, tok{tPlus, "+", line})
			i++
		case c == '-':
			out = append(out, tok{tMinus, "-", line})
			i++
		case c == '*':
			out = append(out, tok{tStar, "*", line})
			i++
		case c == '/':
			// Falls through here only when none of `///`, `//`, `/*` matched
			// above. A standalone `/` is the FHIRPath division operator.
			out = append(out, tok{tSlash, "/", line})
			i++
		case c == '`':
			// Backtick-quoted identifier. Inner text is the ident.
			end := strings.IndexByte(src[i+1:], '`')
			if end < 0 {
				return nil, fmt.Errorf("unterminated backtick identifier at line %d", line)
			}
			out = append(out, tok{tIdent, src[i+1 : i+1+end], line})
			i = i + 2 + end
		case c == '{':
			out = append(out, tok{tLBrace, "{", line})
			i++
		case c == '}':
			out = append(out, tok{tRBrace, "}", line})
			i++
		case c == '(':
			out = append(out, tok{tLParen, "(", line})
			i++
		case c == ')':
			out = append(out, tok{tRParen, ")", line})
			i++
		case c == ':':
			out = append(out, tok{tColon, ":", line})
			i++
		case c == ',':
			out = append(out, tok{tComma, ",", line})
			i++
		case c == ';':
			out = append(out, tok{tSemi, ";", line})
			i++
		case c == '.':
			out = append(out, tok{tDot, ".", line})
			i++
		case c == '=' && i+1 < len(src) && src[i+1] == '=':
			// `==` is the equivalent-relationship operator inside inline conceptmap blocks. Must be matched before single `=`.
			out = append(out, tok{tEqEq, "==", line})
			i += 2
		case c == '=':
			out = append(out, tok{tEq, "=", line})
			i++
		case c == '!' && i+1 < len(src) && src[i+1] == '=':
			out = append(out, tok{tBangEq, "!=", line})
			i += 2
		case c == '\'' || c == '"':
			end := strings.IndexByte(src[i+1:], c)
			if end < 0 {
				return nil, fmt.Errorf("unterminated string at line %d", line)
			}
			out = append(out, tok{tString, src[i+1 : i+1+end], line})
			i = i + 2 + end
		case unicode.IsDigit(rune(c)):
			j := i
			for j < len(src) && unicode.IsDigit(rune(src[j])) {
				j++
			}
			// Cardinality `min..max` (max is digits or `*`).
			if j+1 < len(src) && src[j] == '.' && src[j+1] == '.' {
				k := j + 2
				if k < len(src) && src[k] == '*' {
					out = append(out, tok{tCardinality, src[i : k+1], line})
					i = k + 1
					continue
				}
				if k < len(src) && unicode.IsDigit(rune(src[k])) {
					for k < len(src) && unicode.IsDigit(rune(src[k])) {
						k++
					}
					out = append(out, tok{tCardinality, src[i:k], line})
					i = k
					continue
				}
			}
			// Plain numeric literal (allow a single decimal point for decimals).
			if j < len(src) && src[j] == '.' && j+1 < len(src) && unicode.IsDigit(rune(src[j+1])) {
				j++
				for j < len(src) && unicode.IsDigit(rune(src[j])) {
					j++
				}
			}
			out = append(out, tok{tNumber, src[i:j], line})
			i = j
		case c == '%':
			// FHIRPath %variable reference — consume the following identifier
			// and emit a single tIdent token with the % prefix included so
			// captureParenBalanced round-trips it faithfully to the evaluator.
			j := i + 1
			for j < len(src) && isIdentPart(rune(src[j])) {
				j++
			}
			if j == i+1 {
				return nil, fmt.Errorf("unexpected character %q at line %d", c, line)
			}
			out = append(out, tok{tIdent, src[i:j], line})
			i = j
		case c == '$':
			// FHIRPath special variable ($this, $index, $total). Emitted as a
			// single tIdent including the `$` prefix so captureParenBalanced
			// round-trips it to the FHIRPath engine, which special-cases the
			// leading `$`. Mirrors the `%name` handling above.
			j := i + 1
			for j < len(src) && isIdentPart(rune(src[j])) {
				j++
			}
			if j == i+1 {
				return nil, fmt.Errorf("expected identifier after $ at line %d", line)
			}
			out = append(out, tok{tIdent, src[i:j], line})
			i = j
		case isIdentStart(rune(c)):
			j := i
			for j < len(src) && isIdentPart(rune(src[j])) {
				j++
			}
			text := src[i:j]
			if k, ok := keywords[text]; ok {
				out = append(out, tok{k, text, line})
			} else {
				out = append(out, tok{tIdent, text, line})
			}
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q at line %d", c, line)
		}
	}
	out = append(out, tok{tEOF, "", line})
	return out, nil
}

func isIdentStart(r rune) bool { return unicode.IsLetter(r) || r == '_' }
func isIdentPart(r rune) bool {
	return isIdentStart(r) || unicode.IsDigit(r) || r == '-'
}
