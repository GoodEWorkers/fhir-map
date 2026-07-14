package fhirpath

import "testing"

// subj is a small object used to invoke functions on path expressions
// (this parser does not allow method calls directly on literals).
func subj() map[string]any {
	return map[string]any{
		"i5":      "5",
		"d55":     "5.5",
		"abc":     "abc",
		"axc":     "aXc",
		"nope":    "nope",
		"letters": []any{"a", "b", "c"},
	}
}

// TestEval_BooleanExprs covers string comparison operators (compareStrings),
// the `or` operator (evalOr), float numeric comparison via division
// (numericFloat), and the convertsTo* / string predicate functions.
func TestEval_BooleanExprs(t *testing.T) {
	cases := []struct {
		expr    string
		subject any
		want    bool
	}{
		{"'a' < 'b'", nil, true},
		{"'b' < 'a'", nil, false},
		{"'b' <= 'b'", nil, true},
		{"'c' > 'b'", nil, true},
		{"'a' >= 'b'", nil, false},
		{"'b' >= 'b'", nil, true},
		{"true or false", nil, true},
		{"false or true", nil, true},
		{"false or false", nil, false},
		{"true or true", nil, true},
		{"10 / 4 > 2", nil, true},
		{"10 / 4 < 2", nil, false},
		{"i5.convertsToInteger()", subj(), true},
		{"nope.convertsToInteger()", subj(), false},
		{"d55.convertsToDecimal()", subj(), true},
		{"nope.convertsToDecimal()", subj(), false},
		{"i5.convertsToString()", subj(), true},
		{"abc.startsWith('ab')", subj(), true},
		{"abc.startsWith('zz')", subj(), false},
		{"abc.matches('^a.*c$')", subj(), true},
		{"abc.matches('^z')", subj(), false},
	}
	for _, c := range cases {
		got := mustEval(t, c.expr, c.subject)
		if len(got) != 1 {
			t.Fatalf("%s: expected one result, got %v", c.expr, got)
		}
		b, ok := got[0].(bool)
		if !ok {
			t.Fatalf("%s: expected bool, got %T (%v)", c.expr, got[0], got[0])
		}
		if b != c.want {
			t.Fatalf("%s = %v, want %v", c.expr, b, c.want)
		}
	}
}

// TestEval_ConversionFuncs covers toInteger / toDecimal / toString value paths.
func TestEval_ConversionFuncs(t *testing.T) {
	if got := mustEval(t, "i5.toInteger()", subj()); len(got) != 1 || got[0] != int64(5) {
		t.Fatalf("i5.toInteger() = %v", got)
	}
	if got := mustEval(t, "d55.toDecimal()", subj()); len(got) != 1 || got[0] != float64(5.5) {
		t.Fatalf("d55.toDecimal() = %v", got)
	}
	if got := mustEval(t, "i5.toString()", subj()); len(got) != 1 || got[0] != "5" {
		t.Fatalf("i5.toString() = %v", got)
	}
	// Non-convertible input yields empty per FHIRPath semantics.
	if got := mustEval(t, "abc.toInteger()", subj()); len(got) != 0 {
		t.Fatalf("abc.toInteger() should be empty, got %v", got)
	}
}

// TestEval_CollectionFuncs covers join / skip / take / replace.
func TestEval_CollectionFuncs(t *testing.T) {
	expectEqual(t, mustEval(t, "letters.join('-')", subj()), []any{"a-b-c"})
	expectEqual(t, mustEval(t, "letters.skip(1)", subj()), []any{"b", "c"})
	expectEqual(t, mustEval(t, "letters.take(2)", subj()), []any{"a", "b"})
	expectEqual(t, mustEval(t, "axc.replace('X', 'b')", subj()), []any{"abc"})
}

// typed exercises the non-string type branches of toInteger/toDecimal/
// toBoolean/stringify and float arithmetic (numericFloat).
func typed() map[string]any {
	return map[string]any{
		"ni":   5,
		"ni64": int64(7),
		"f":    3.0,
		"f2":   2.5,
		"bt":   true,
		"bf":   false,
	}
}

func TestEval_TypedConversions(t *testing.T) {
	if got := mustEval(t, "ni.toDecimal()", typed()); len(got) != 1 || got[0] != float64(5) {
		t.Fatalf("ni.toDecimal() = %v", got)
	}
	if got := mustEval(t, "f.toInteger()", typed()); len(got) != 1 || got[0] != int64(3) {
		t.Fatalf("f.toInteger() = %v", got)
	}
	// Non-whole float is not convertible to integer → empty.
	if got := mustEval(t, "f2.toInteger()", typed()); len(got) != 0 {
		t.Fatalf("f2.toInteger() should be empty, got %v", got)
	}
	if got := mustEval(t, "bt.toInteger()", typed()); len(got) != 1 || got[0] != int64(1) {
		t.Fatalf("bt.toInteger() = %v", got)
	}
	if got := mustEval(t, "bf.toInteger()", typed()); len(got) != 1 || got[0] != int64(0) {
		t.Fatalf("bf.toInteger() = %v", got)
	}
	if got := mustEval(t, "bt.toDecimal()", typed()); len(got) != 1 || got[0] != float64(1) {
		t.Fatalf("bt.toDecimal() = %v", got)
	}
	if got := mustEval(t, "ni64.toString()", typed()); len(got) != 1 || got[0] != "7" {
		t.Fatalf("ni64.toString() = %v", got)
	}
}

// TestEval_DateAndEquality covers date conversion predicates, collection
// equality (deepEqual), and a few string functions.
func TestEval_DateAndEquality(t *testing.T) {
	s := map[string]any{
		"dstr":    "2020-01-01",
		"dtstr":   "2020-01-01T12:00:00Z",
		"bad":     "not-a-date",
		"letters": []any{"a", "b", "c"},
		"word":    "hello",
	}
	boolCases := []struct {
		expr string
		want bool
	}{
		{"dstr.convertsToDate()", true},
		{"bad.convertsToDate()", false},
		{"dtstr.convertsToDateTime()", true},
		{"letters = letters", true},
		{"letters = ('a' | 'b')", false},
		{"word.contains('ell')", true},
		{"word.endsWith('lo')", true},
		{"word.length() = 5", true},
	}
	for _, c := range boolCases {
		got := mustEval(t, c.expr, s)
		if len(got) != 1 {
			t.Fatalf("%s: expected one result, got %v", c.expr, got)
		}
		b, ok := got[0].(bool)
		if !ok {
			t.Fatalf("%s: expected bool, got %T", c.expr, got[0])
		}
		if b != c.want {
			t.Fatalf("%s = %v, want %v", c.expr, b, c.want)
		}
	}
	// toDate yields a value for a valid date and empty for garbage.
	if got := mustEval(t, "dstr.toDate()", s); len(got) != 1 {
		t.Fatalf("dstr.toDate() should yield one value, got %v", got)
	}
	if got := mustEval(t, "bad.toDate()", s); len(got) != 0 {
		t.Fatalf("bad.toDate() should be empty, got %v", got)
	}
	expectEqual(t, mustEval(t, "word.substring(1, 3)", s), []any{"ell"})
	expectEqual(t, mustEval(t, "word.substring(3)", s), []any{"lo"})
}

// TestEval_ToBooleanAndStringify covers toBoolean's accepted-token branches and
// stringify over non-string types.
func TestEval_ToBooleanAndStringify(t *testing.T) {
	s := map[string]any{
		"tt": "true", "ff": "false", "tc": "t", "fc": "f",
		"one": "1", "zero": "0", "yes": "yes",
		"bt": true, "ni": 5,
	}
	boolCases := []struct {
		expr string
		want bool
	}{
		{"tt.toBoolean()", true},
		{"ff.toBoolean()", false},
		{"tc.toBoolean()", true},
		{"fc.toBoolean()", false},
		{"one.toBoolean()", true},
		{"zero.toBoolean()", false},
	}
	for _, c := range boolCases {
		got := mustEval(t, c.expr, s)
		if len(got) != 1 {
			t.Fatalf("%s: expected one result, got %v", c.expr, got)
		}
		if b, ok := got[0].(bool); !ok || b != c.want {
			t.Fatalf("%s = %v (%T), want %v", c.expr, got[0], got[0], c.want)
		}
	}
	// Non-conformant boolean token → empty.
	if got := mustEval(t, "yes.toBoolean()", s); len(got) != 0 {
		t.Fatalf("yes.toBoolean() should be empty, got %v", got)
	}
	if got := mustEval(t, "bt.toString()", s); len(got) != 1 || got[0] != "true" {
		t.Fatalf("bt.toString() = %v", got)
	}
	if got := mustEval(t, "ni.toString()", s); len(got) != 1 || got[0] != "5" {
		t.Fatalf("ni.toString() = %v", got)
	}
}

// TestEval_NestedNavigation exercises navigate() across resourceType matching,
// slice traversal, and nested map/slice flattening, plus where/select/exists
// with predicates over real nested data.
func TestEval_NestedNavigation(t *testing.T) {
	patient := map[string]any{
		"resourceType": "Patient",
		"active":       true,
		"name": []any{
			map[string]any{"use": "official", "family": "Doe", "given": []any{"Jane", "Q"}},
			map[string]any{"use": "nickname", "family": "D", "given": []any{"Janie"}},
		},
		"telecom": []any{
			map[string]any{"system": "phone", "value": "555-1"},
			map[string]any{"system": "email", "value": "a@b.c"},
		},
	}

	expectEqual(t, mustEval(t, "Patient.name.given", patient), []any{"Jane", "Q", "Janie"})
	expectEqual(t, mustEval(t, "Patient.name.family", patient), []any{"Doe", "D"})
	expectEqual(t, mustEval(t, "name.where(use = 'official').family", patient), []any{"Doe"})
	expectEqual(t, mustEval(t, "telecom.where(system = 'email').value", patient), []any{"a@b.c"})
	expectEqual(t, mustEval(t, "name.select(use)", patient), []any{"official", "nickname"})
	expectEqual(t, mustEval(t, "name.given.first()", patient), []any{"Jane"})
	expectEqual(t, mustEval(t, "name.given.last()", patient), []any{"Janie"})

	// count + exists-with-predicate return scalars
	if got := mustEval(t, "name.count() = 2", patient); len(got) != 1 || got[0] != true {
		t.Fatalf("name.count() = 2 → %v", got)
	}
	if got := mustEval(t, "name.exists(use = 'nickname')", patient); len(got) != 1 || got[0] != true {
		t.Fatalf("exists(use=nickname) → %v", got)
	}
	if got := mustEval(t, "telecom.where(system = 'fax').exists()", patient); len(got) != 1 || got[0] != false {
		t.Fatalf("where(fax).exists() → %v", got)
	}
}

func TestEval_AndAndFloatArith(t *testing.T) {
	cases := []struct {
		expr    string
		subject any
		want    bool
	}{
		{"true and false", nil, false},
		{"false and false", nil, false},
		{"true and true", nil, true},
		// float arithmetic flows through numericFloat
		{"f + f2 > 5", typed(), true},  // 3.0 + 2.5 = 5.5
		{"f * f2 < 10", typed(), true}, // 7.5
		{"f - f2 < 1", typed(), true},  // 0.5
	}
	for _, c := range cases {
		got := mustEval(t, c.expr, c.subject)
		if len(got) != 1 {
			t.Fatalf("%s: expected one result, got %v", c.expr, got)
		}
		b, ok := got[0].(bool)
		if !ok {
			t.Fatalf("%s: expected bool, got %T", c.expr, got[0])
		}
		if b != c.want {
			t.Fatalf("%s = %v, want %v", c.expr, b, c.want)
		}
	}
}
