package fhirpath

import (
	"reflect"
	"testing"
)

// Minimal FHIRPath evaluator: works over Element, an interface that abstracts
// JSON-shaped values (primitive, map[string]any, or []any), so the executor can
// feed either typed Go structs or generic JSON.

func TestEval_LiteralString(t *testing.T) {
	got := mustEval(t, "'hello'", nil)
	expectEqual(t, got, []any{"hello"})
}

func TestEval_LiteralInteger(t *testing.T) {
	got := mustEval(t, "42", nil)
	expectEqual(t, got, []any{int64(42)})
}

func TestEval_LiteralBool(t *testing.T) {
	got := mustEval(t, "true", nil)
	expectEqual(t, got, []any{true})
}

// Decimal literals are float64 to match FHIR JSON (json.Unmarshal) and numeric operations.
func TestEval_LiteralDecimal(t *testing.T) {
	expectEqual(t, mustEval(t, "1.5", nil), []any{1.5})
	expectEqual(t, mustEval(t, "0.5", nil), []any{0.5})
	expectEqual(t, mustEval(t, "3.14159", nil), []any{3.14159})
}

func TestEval_Decimal_Arithmetic(t *testing.T) {
	// A float operand makes the result a float even when it lands on a whole
	// number (1.5 + 1 = 2.5; 2.5 * 2 = 5.0 stays float64, not int64).
	expectEqual(t, mustEval(t, "1.5 + 1", nil), []any{2.5})
	expectEqual(t, mustEval(t, "2.5 * 2", nil), []any{float64(5)})
}

func TestEval_Decimal_Comparison(t *testing.T) {
	expectEqual(t, mustEval(t, "2.5 > 2", nil), []any{true})
	expectEqual(t, mustEval(t, "0.5 < 1", nil), []any{true})
	expectEqual(t, mustEval(t, "1.5 >= 1.5", nil), []any{true})
}

// We use float64 precision (not arbitrary-precision BigDecimal) to match fhirpath.js's default.
// This guards against regressions like 0.1 + 0.2 unexpectedly equaling 0.3 at compile time.
func TestEval_Decimal_Float64Precision_IsDocumented(t *testing.T) {
	// a, b force runtime float64 arithmetic, not compile-time constant folding.
	a, b := 0.1, 0.2
	expectEqual(t, mustEval(t, "0.1 + 0.2", nil), []any{a + b})
	if a+b == 0.3 {
		t.Fatal("float64 0.1+0.2 unexpectedly equals 0.3; revisit the documented precision note")
	}
}

// FHIRPath spec: a leading sign is unary negation/identity, not part of the literal.
// We model it as `0 - x` / `0 + x` to reuse arithmetic (preserving int64/float64 types).
func TestEval_UnaryMinus_Integer(t *testing.T) {
	expectEqual(t, mustEval(t, "-5", nil), []any{int64(-5)})
}

func TestEval_UnaryMinus_Decimal(t *testing.T) {
	expectEqual(t, mustEval(t, "-1.5", nil), []any{-1.5})
}

func TestEval_UnaryPlus_Identity(t *testing.T) {
	expectEqual(t, mustEval(t, "+5", nil), []any{int64(5)})
	expectEqual(t, mustEval(t, "+1.5", nil), []any{1.5})
}

// Polarity binds tighter than `*`: `2 * -3` is `2 * (-3)`, `-3 * 2` is
// `(-3) * 2` — both -6.
func TestEval_UnaryMinus_PrecedenceWithMultiply(t *testing.T) {
	expectEqual(t, mustEval(t, "2 * -3", nil), []any{int64(-6)})
	expectEqual(t, mustEval(t, "-3 * 2", nil), []any{int64(-6)})
}

// Binary subtraction and unary minus disambiguate: after a binary `-` is
// taken, a second `-` parses as unary. `2 - -3` = 5.
func TestEval_BinaryMinusThenUnaryMinus(t *testing.T) {
	expectEqual(t, mustEval(t, "2 - -3", nil), []any{int64(5)})
}

func TestEval_UnaryMinus_OnFieldValue(t *testing.T) {
	subject := map[string]any{"value": int64(10)}
	expectEqual(t, mustEval(t, "-value", subject), []any{int64(-10)})
}

// Regression: a hyphen-bearing HL7v2 identifier must NOT be misread as unary
// minus — `MSH-9` is still a single path token.
func TestEval_UnaryMinus_DoesNotBreakHyphenIdent(t *testing.T) {
	subject := map[string]any{"MSH-9": "ORU^R01"}
	expectEqual(t, mustEval(t, "MSH-9", subject), []any{"ORU^R01"})
}

// $index is the 0-based position of the current item in the input collection,
// available inside expression-parameter iterators (where/select/exists). Spec:
// "$this and $index ... represent the item ... and its index in the collection".
func TestEval_DollarIndex_InSelect(t *testing.T) {
	expectEqual(t, mustEval(t, "('a' | 'b' | 'c').select($index)", nil), []any{int64(0), int64(1), int64(2)})
}

func TestEval_DollarIndex_InWhere(t *testing.T) {
	// Keep items at index >= 1.
	expectEqual(t, mustEval(t, "(10 | 20 | 30).where($index >= 1)", nil), []any{int64(20), int64(30)})
}

func TestEval_DollarIndex_InExistsPredicate(t *testing.T) {
	expectEqual(t, mustEval(t, "(10 | 20 | 30).exists($index = 2)", nil), []any{true})
	expectEqual(t, mustEval(t, "(10 | 20).exists($index = 5)", nil), []any{false})
}

// Nested iterators must not clobber each other's $index: it must be restored after inner scope.
func TestEval_DollarIndex_NestedRestores(t *testing.T) {
	expectEqual(t, mustEval(t, "(5 | 6).where(($index | $index).select($this).first() = $index)", nil), []any{int64(5), int64(6)})
}

// Outside any iterator, $index is empty (FHIRPath missing = empty).
func TestEval_DollarIndex_EmptyOutsideIteration(t *testing.T) {
	expectEqual(t, mustEval(t, "$index.exists()", nil), []any{false})
}

func TestEval_FieldNavigation(t *testing.T) {
	subject := map[string]any{
		"linkId": "name",
		"answer": []any{
			map[string]any{"valueString": "Alice"},
		},
	}
	got := mustEval(t, "linkId", subject)
	expectEqual(t, got, []any{"name"})
}

func TestEval_NestedPath(t *testing.T) {
	subject := map[string]any{
		"name": map[string]any{"given": []any{"Ada", "Augusta"}},
	}
	got := mustEval(t, "name.given", subject)
	expectEqual(t, got, []any{"Ada", "Augusta"})
}

// `Patient.name.given` style: when the path's first identifier matches the
// root's resourceType, the engine skips it (so authors can write the path
// the way it appears in the FHIR spec — rooted at the resource type).
func TestEval_ResourceTypeRoot(t *testing.T) {
	subject := map[string]any{
		"resourceType": "Patient",
		"name": []any{
			map[string]any{"given": []any{"Ada"}, "family": "Lovelace"},
		},
	}
	got := mustEval(t, "Patient.name.given", subject)
	expectEqual(t, got, []any{"Ada"})
}

func TestEval_WhereWithEquality(t *testing.T) {
	subject := map[string]any{
		"item": []any{
			map[string]any{"linkId": "name", "answer": "Alice"},
			map[string]any{"linkId": "age", "answer": "42"},
			map[string]any{"linkId": "city", "answer": "Cambridge"},
		},
	}
	got := mustEval(t, "item.where(linkId = 'age')", subject)
	if len(got) != 1 {
		t.Fatalf("expected one match; got %v", got)
	}
	m, _ := got[0].(map[string]any)
	if m["answer"] != "42" {
		t.Fatalf("expected the age item; got %v", m)
	}
}

func TestEval_ExistsEmpty(t *testing.T) {
	subject := map[string]any{
		"item": []any{
			map[string]any{"linkId": "age"},
		},
	}
	exists := mustEval(t, "item.exists()", subject)
	expectEqual(t, exists, []any{true})
	empty := mustEval(t, "item.empty()", subject)
	expectEqual(t, empty, []any{false})

	missing := mustEval(t, "missing.exists()", subject)
	expectEqual(t, missing, []any{false})
	missingEmpty := mustEval(t, "missing.empty()", subject)
	expectEqual(t, missingEmpty, []any{true})
}

func TestEval_FirstLastCount(t *testing.T) {
	subject := map[string]any{
		"names": []any{"a", "b", "c"},
	}
	first := mustEval(t, "names.first()", subject)
	expectEqual(t, first, []any{"a"})
	last := mustEval(t, "names.last()", subject)
	expectEqual(t, last, []any{"c"})
	count := mustEval(t, "names.count()", subject)
	expectEqual(t, count, []any{int64(3)})
}

func TestEval_NotAndOr(t *testing.T) {
	got := mustEval(t, "not(true)", nil)
	expectEqual(t, got, []any{false})
	got = mustEval(t, "true and false", nil)
	expectEqual(t, got, []any{false})
	got = mustEval(t, "true or false", nil)
	expectEqual(t, got, []any{true})
}

func TestEval_EqualityBetweenPathAndLiteral(t *testing.T) {
	subject := map[string]any{
		"linkId": "name",
	}
	got := mustEval(t, "linkId = 'name'", subject)
	expectEqual(t, got, []any{true})
	got = mustEval(t, "linkId = 'age'", subject)
	expectEqual(t, got, []any{false})
}

// Two-step: navigation after where, used in QR→Patient mappings.
func TestEval_DeepNavigationAfterWhere(t *testing.T) {
	subject := map[string]any{
		"item": []any{
			map[string]any{"linkId": "name", "answer": []any{
				map[string]any{"valueString": "Alice"},
			}},
			map[string]any{"linkId": "age", "answer": []any{
				map[string]any{"valueString": "42"},
			}},
		},
	}
	got := mustEval(t, "item.where(linkId = 'name').answer.valueString", subject)
	expectEqual(t, got, []any{"Alice"})
}

// Helpers

// FHIRPath spec: expressions can reference external constants via % syntax.
// Example: %DiagnosticReportTarget.identifier.value from FML executor's scope frame.
func TestEval_PercentVariable_ResolvesFromScope(t *testing.T) {
	env := map[string]any{"foo": "bar"}
	got, err := EvalIn("%foo", nil, env)
	if err != nil {
		t.Fatalf("EvalIn: %v", err)
	}
	expectEqual(t, got, []any{"bar"})
}

func TestEval_PercentVariable_WithNavigation(t *testing.T) {
	env := map[string]any{
		"dr": map[string]any{
			"identifier": []any{
				map[string]any{"value": "ABC-123"},
			},
		},
	}
	got, err := EvalIn("%dr.identifier.value", nil, env)
	if err != nil {
		t.Fatalf("EvalIn: %v", err)
	}
	expectEqual(t, got, []any{"ABC-123"})
}

// Unbound percent variables return empty (not an error); lets authors write guards like "%foo.exists()".
func TestEval_PercentVariable_UnboundReturnsEmpty(t *testing.T) {
	got, err := EvalIn("%missing", nil, map[string]any{})
	if err != nil {
		t.Fatalf("EvalIn: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unbound %%missing must return empty, got %v", got)
	}
}

// Legacy Eval(expr, subject) signature must keep working; all existing call sites use it.
func TestEval_LegacyEval_StillWorks(t *testing.T) {
	got := mustEval(t, "linkId", map[string]any{"linkId": "name"})
	expectEqual(t, got, []any{"name"})
}

// `in` and `contains` membership operators with union literals (|).
// Critical for Postman corpus patterns like: $this in ('AU' | 'BG' | ... codes).
func TestEval_UnionLiteral_BuildsCollection(t *testing.T) {
	got := mustEval(t, "('a' | 'b' | 'c')", nil)
	expectEqual(t, got, []any{"a", "b", "c"})
}

func TestEval_InOperator_MembershipInUnionLiteral_True(t *testing.T) {
	got := mustEval(t, "'BG' in ('AU' | 'BG' | 'BLB')", nil)
	expectEqual(t, got, []any{true})
}

func TestEval_InOperator_MembershipInUnionLiteral_False(t *testing.T) {
	got := mustEval(t, "'XX' in ('AU' | 'BG' | 'BLB')", nil)
	expectEqual(t, got, []any{false})
}

func TestEval_InOperator_DollarThis(t *testing.T) {
	got, err := Eval("$this in ('A' | 'B')", "B")
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	expectEqual(t, got, []any{true})
}

// `contains` is the inverse: collection contains value (not value in collection).
func TestEval_ContainsOperator_Inverse(t *testing.T) {
	got := mustEval(t, "('A' | 'B') contains 'A'", nil)
	expectEqual(t, got, []any{true})
	got = mustEval(t, "('A' | 'B') contains 'Z'", nil)
	expectEqual(t, got, []any{false})
}

// The method form `.contains()` (string-substring) must still work alongside the new infix operator.
func TestEval_ContainsMethod_StillWorks(t *testing.T) {
	subject := map[string]any{"greeting": "hello world"}
	got := mustEval(t, "greeting.contains('world')", subject)
	expectEqual(t, got, []any{true})
}

// `.not()` chained method and `.exists(predicate)` with embedded filter.
// Used in Postman dedup-guard: %X.coding.exists(code='Y' and system='Z').not()
func TestEval_NotMethod_OnEqualityResult(t *testing.T) {
	got := mustEval(t, "('x' = 'y').not()", nil)
	expectEqual(t, got, []any{true})

	got = mustEval(t, "('x' = 'x').not()", nil)
	expectEqual(t, got, []any{false})
}

func TestEval_NotMethod_OnExistsChain(t *testing.T) {
	subject := map[string]any{"items": []any{}}
	got := mustEval(t, "items.exists().not()", subject)
	expectEqual(t, got, []any{true})
}

func TestEval_ExistsWithPredicate_MatchesAny(t *testing.T) {
	subject := map[string]any{
		"items": []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		},
	}
	got := mustEval(t, "items.exists(id = 'b')", subject)
	expectEqual(t, got, []any{true})
}

func TestEval_ExistsWithPredicate_NoMatch(t *testing.T) {
	subject := map[string]any{
		"items": []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
	}
	got := mustEval(t, "items.exists(id = 'zzz')", subject)
	expectEqual(t, got, []any{false})
}

// Real Postman dedup-guard pattern: check coding.exists(code='X' and system='Y').not()
func TestEval_ExistsWithPredicate_AndCondition(t *testing.T) {
	subject := map[string]any{
		"category": map[string]any{
			"coding": []any{
				map[string]any{"code": "26436-6", "system": "http://loinc.org"},
			},
		},
	}
	got := mustEval(t, "category.coding.exists(code = '26436-6' and system = 'http://loinc.org').not()", subject)
	expectEqual(t, got, []any{false})

	subject2 := map[string]any{"category": map[string]any{"coding": []any{}}}
	got = mustEval(t, "category.coding.exists(code = '26436-6' and system = 'http://loinc.org').not()", subject2)
	expectEqual(t, got, []any{true})
}

// Inequality, not-equal, equivalence operators, plus iif/length/substring/matches functions.
func TestEval_Inequality_Numeric(t *testing.T) {
	expectEqual(t, mustEval(t, "1 < 2", nil), []any{true})
	expectEqual(t, mustEval(t, "2 < 2", nil), []any{false})
	expectEqual(t, mustEval(t, "2 <= 2", nil), []any{true})
	expectEqual(t, mustEval(t, "3 > 2", nil), []any{true})
	expectEqual(t, mustEval(t, "2 >= 3", nil), []any{false})
}

func TestEval_NotEqual(t *testing.T) {
	expectEqual(t, mustEval(t, "1 != 2", nil), []any{true})
	expectEqual(t, mustEval(t, "'a' != 'a'", nil), []any{false})
}

func TestEval_Equivalence_LooseEquality(t *testing.T) {
	// `~` is loose equality: case-insensitive for strings.
	expectEqual(t, mustEval(t, "'AbC' ~ 'abc'", nil), []any{true})
	expectEqual(t, mustEval(t, "'AbC' !~ 'xxx'", nil), []any{true})
}

func TestEval_Iif(t *testing.T) {
	expectEqual(t, mustEval(t, "iif(true, 'yes', 'no')", nil), []any{"yes"})
	expectEqual(t, mustEval(t, "iif(false, 'yes', 'no')", nil), []any{"no"})
	// iif on a path-derived boolean.
	subject := map[string]any{"flag": true, "a": "A", "b": "B"}
	expectEqual(t, mustEval(t, "iif(flag, a, b)", subject), []any{"A"})
}

func TestEval_Length(t *testing.T) {
	expectEqual(t, mustEval(t, "name.length()", map[string]any{"name": "hello"}), []any{int64(5)})
}

func TestEval_Substring(t *testing.T) {
	// substring(start) and substring(start, length).
	subject := map[string]any{"s": "abcdef"}
	expectEqual(t, mustEval(t, "s.substring(2)", subject), []any{"cdef"})
	expectEqual(t, mustEval(t, "s.substring(1, 3)", subject), []any{"bcd"})
}

func TestEval_Matches(t *testing.T) {
	subject := map[string]any{"v": "2026-05-16"}
	expectEqual(t, mustEval(t, "v.matches('^[0-9]{4}-[0-9]{2}-[0-9]{2}$')", subject), []any{true})
	expectEqual(t, mustEval(t, "v.matches('^abc$')", subject), []any{false})
}

// Arithmetic operators (+, -, *, /, mod, div) and string concat (&).
// Critical: the lexer's hyphen-in-identifier rule (for 30+ HL7v2 paths like MSH-3-2) must not break.
func TestEval_Add_Integers(t *testing.T) {
	expectEqual(t, mustEval(t, "1 + 2", nil), []any{int64(3)})
	expectEqual(t, mustEval(t, "10 + 5 + 3", nil), []any{int64(18)})
}

func TestEval_Subtract_WithSpaces(t *testing.T) {
	expectEqual(t, mustEval(t, "5 - 2", nil), []any{int64(3)})
	expectEqual(t, mustEval(t, "10 - 3 - 2", nil), []any{int64(5)})
}

func TestEval_Multiply_DivideMod(t *testing.T) {
	expectEqual(t, mustEval(t, "4 * 3", nil), []any{int64(12)})
	expectEqual(t, mustEval(t, "10 mod 3", nil), []any{int64(1)})
	expectEqual(t, mustEval(t, "10 div 3", nil), []any{int64(3)})
}

func TestEval_Arithmetic_RespectsPrecedence(t *testing.T) {
	// `*` binds tighter than `+`/`-`.
	expectEqual(t, mustEval(t, "2 + 3 * 4", nil), []any{int64(14)})
	expectEqual(t, mustEval(t, "(2 + 3) * 4", nil), []any{int64(20)})
}

func TestEval_Concat_Ampersand(t *testing.T) {
	expectEqual(t, mustEval(t, "'hello' & ' ' & 'world'", nil), []any{"hello world"})
}

// CRITICAL regression guard: hyphen-bearing identifiers (HL7v2 paths like MSH-3-2) lex as single tokens.
// If this breaks, every HL7v2 transform in the Postman corpus fails.
func TestEval_HyphenIdent_StillSingleToken_AfterArithmetic(t *testing.T) {
	subject := map[string]any{
		"MSH-3":   "LMX5",
		"MSH-3-2": "subfield",
		"OBR-10":  "primary",
	}
	expectEqual(t, mustEval(t, "MSH-3", subject), []any{"LMX5"})
	expectEqual(t, mustEval(t, "OBR-10", subject), []any{"primary"})
	expectEqual(t, mustEval(t, "MSH-3-2", subject), []any{"subfield"})
}

// Arithmetic works alongside hyphen-idents: whitespace forces lexer to close the identifier before `-`.
func TestEval_HyphenIdent_PlusArithmetic_Disambiguates(t *testing.T) {
	subject := map[string]any{
		"OBR-10": int64(42),
	}
	expectEqual(t, mustEval(t, "OBR-10 - 2", subject), []any{int64(40)})
}

// mustEval parses + evaluates an expression. Fails the test on any error.
func mustEval(t *testing.T, expr string, subject any) []any {
	t.Helper()
	result, err := Eval(expr, subject)
	if err != nil {
		t.Fatalf("Eval(%q): %v", expr, err)
	}
	return result
}

func expectEqual(t *testing.T, got, want []any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
