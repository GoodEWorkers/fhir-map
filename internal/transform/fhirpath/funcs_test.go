package fhirpath

import "testing"

// Tests verify FHIRPath function coverage: one assertion plus edge cases per test for spec compliance.

// --- Collection: select / repeat / distinct / tail / skip / take ---

func TestEval_Select_Projection(t *testing.T) {
	subject := map[string]any{
		"item": []any{
			map[string]any{"linkId": "name", "answer": "Alice"},
			map[string]any{"linkId": "age", "answer": "42"},
		},
	}
	got := mustEval(t, "item.select(linkId)", subject)
	expectEqual(t, got, []any{"name", "age"})
}

func TestEval_Select_FlattensListResults(t *testing.T) {
	subject := map[string]any{
		"people": []any{
			map[string]any{"name": []any{"Ada", "Augusta"}},
			map[string]any{"name": []any{"Bob"}},
		},
	}
	got := mustEval(t, "people.select(name)", subject)
	expectEqual(t, got, []any{"Ada", "Augusta", "Bob"})
}

func TestEval_Repeat_FlattensNestedItems(t *testing.T) {
	// Classic Questionnaire.item recursion: each item may itself contain
	// a nested item list; .repeat(item) reaches every descendant.
	subject := map[string]any{
		"item": []any{
			map[string]any{
				"linkId": "outer",
				"item": []any{
					map[string]any{"linkId": "inner-a"},
					map[string]any{
						"linkId": "inner-b",
						"item":   []any{map[string]any{"linkId": "deep"}},
					},
				},
			},
		},
	}
	got := mustEval(t, "repeat(item).linkId", subject)
	expectEqual(t, got, []any{"outer", "inner-a", "inner-b", "deep"})
}

func TestEval_Distinct(t *testing.T) {
	subject := map[string]any{"codes": []any{"A", "B", "A", "C", "B"}}
	got := mustEval(t, "codes.distinct()", subject)
	expectEqual(t, got, []any{"A", "B", "C"})
}

func TestEval_IsDistinct(t *testing.T) {
	expectEqual(t,
		mustEval(t, "codes.isDistinct()", map[string]any{"codes": []any{"A", "B", "C"}}),
		[]any{true})
	expectEqual(t,
		mustEval(t, "codes.isDistinct()", map[string]any{"codes": []any{"A", "B", "A"}}),
		[]any{false})
}

func TestEval_Tail(t *testing.T) {
	subject := map[string]any{"xs": []any{"a", "b", "c"}}
	expectEqual(t, mustEval(t, "xs.tail()", subject), []any{"b", "c"})
	// Empty receiver → empty.
	got, _ := Eval("xs.tail()", map[string]any{"xs": []any{}})
	if len(got) != 0 {
		t.Fatalf("tail of empty must be empty; got %v", got)
	}
}

func TestEval_Skip(t *testing.T) {
	subject := map[string]any{"xs": []any{"a", "b", "c", "d"}}
	expectEqual(t, mustEval(t, "xs.skip(2)", subject), []any{"c", "d"})
	// Skipping more than length → empty.
	got, _ := Eval("xs.skip(10)", subject)
	if len(got) != 0 {
		t.Fatalf("skip past end must be empty; got %v", got)
	}
}

func TestEval_Take(t *testing.T) {
	subject := map[string]any{"xs": []any{"a", "b", "c", "d"}}
	expectEqual(t, mustEval(t, "xs.take(2)", subject), []any{"a", "b"})
	// Taking 0 → empty.
	got, _ := Eval("xs.take(0)", subject)
	if len(got) != 0 {
		t.Fatalf("take 0 must be empty; got %v", got)
	}
}

func TestEval_OfType_Primitives(t *testing.T) {
	subject := map[string]any{
		"mixed": []any{"hello", int64(42), true, "world", float64(3.14)},
	}
	expectEqual(t, mustEval(t, "mixed.ofType(string)", subject), []any{"hello", "world"})
	expectEqual(t, mustEval(t, "mixed.ofType(integer)", subject), []any{int64(42)})
	expectEqual(t, mustEval(t, "mixed.ofType(boolean)", subject), []any{true})
	expectEqual(t, mustEval(t, "mixed.ofType(decimal)", subject), []any{float64(3.14)})
}

func TestEval_OfType_Resource(t *testing.T) {
	subject := map[string]any{
		"entries": []any{
			map[string]any{"resourceType": "Patient", "id": "p1"},
			map[string]any{"resourceType": "Observation", "id": "o1"},
			map[string]any{"resourceType": "Patient", "id": "p2"},
		},
	}
	got := mustEval(t, "entries.ofType(Patient).id", subject)
	expectEqual(t, got, []any{"p1", "p2"})
}

func TestEval_Aggregate_Sum(t *testing.T) {
	// $total starts at the init value, then each element adds its value.
	subject := map[string]any{"xs": []any{int64(1), int64(2), int64(3)}}
	got := mustEval(t, "xs.aggregate($this + $total, 0)", subject)
	expectEqual(t, got, []any{int64(6)})
}

func TestEval_Aggregate_NoInit(t *testing.T) {
	// Without an init the accumulator starts as empty; once a value flows
	// in, subsequent iterations can use it. We test the most common case
	// (running concat) since that's what mapping authors usually want.
	subject := map[string]any{"xs": []any{"a", "b", "c"}}
	got := mustEval(t, "xs.aggregate(iif($total.empty(), $this, $total & '-' & $this))", subject)
	expectEqual(t, got, []any{"a-b-c"})
}

// --- String: endsWith / indexOf / trim / split / join ---

func TestEval_EndsWith(t *testing.T) {
	subject := map[string]any{"name": "Lovelace"}
	expectEqual(t, mustEval(t, "name.endsWith('lace')", subject), []any{true})
	expectEqual(t, mustEval(t, "name.endsWith('xyz')", subject), []any{false})
}

func TestEval_IndexOf(t *testing.T) {
	subject := map[string]any{"s": "abcdef"}
	expectEqual(t, mustEval(t, "s.indexOf('cd')", subject), []any{int64(2)})
	// Not found → -1.
	expectEqual(t, mustEval(t, "s.indexOf('zz')", subject), []any{int64(-1)})
}

func TestEval_Trim(t *testing.T) {
	subject := map[string]any{"s": "  hello world  "}
	expectEqual(t, mustEval(t, "s.trim()", subject), []any{"hello world"})
}

func TestEval_Split(t *testing.T) {
	subject := map[string]any{"s": "a,b,c"}
	got := mustEval(t, "s.split(',')", subject)
	expectEqual(t, got, []any{"a", "b", "c"})
}

func TestEval_Join_WithSeparator(t *testing.T) {
	subject := map[string]any{"xs": []any{"a", "b", "c"}}
	expectEqual(t, mustEval(t, "xs.join('-')", subject), []any{"a-b-c"})
}

func TestEval_Join_NoSeparator(t *testing.T) {
	subject := map[string]any{"xs": []any{"a", "b", "c"}}
	expectEqual(t, mustEval(t, "xs.join()", subject), []any{"abc"})
}

// --- Conversion: to* / convertsTo* ---

func TestEval_ToInteger(t *testing.T) {
	expectEqual(t, mustEval(t, "v.toInteger()", map[string]any{"v": "42"}), []any{int64(42)})
	expectEqual(t, mustEval(t, "v.toInteger()", map[string]any{"v": int64(7)}), []any{int64(7)})
	expectEqual(t, mustEval(t, "v.toInteger()", map[string]any{"v": true}), []any{int64(1)})
	// Not convertible → empty (not error, per spec).
	got, _ := Eval("v.toInteger()", map[string]any{"v": "not-a-number"})
	if len(got) != 0 {
		t.Fatalf("toInteger of non-numeric must be empty; got %v", got)
	}
}

func TestEval_ToDecimal(t *testing.T) {
	expectEqual(t, mustEval(t, "v.toDecimal()", map[string]any{"v": "3.14"}), []any{float64(3.14)})
	expectEqual(t, mustEval(t, "v.toDecimal()", map[string]any{"v": int64(7)}), []any{float64(7)})
}

func TestEval_ToBoolean(t *testing.T) {
	expectEqual(t, mustEval(t, "v.toBoolean()", map[string]any{"v": "true"}), []any{true})
	expectEqual(t, mustEval(t, "v.toBoolean()", map[string]any{"v": "false"}), []any{false})
	expectEqual(t, mustEval(t, "v.toBoolean()", map[string]any{"v": int64(1)}), []any{true})
	expectEqual(t, mustEval(t, "v.toBoolean()", map[string]any{"v": int64(0)}), []any{false})
}

func TestEval_ToDate_Passthrough(t *testing.T) {
	// The FHIRPath spec returns a Date scalar; in our JSON-shaped world a
	// date is already a string. We round-trip valid dates and return
	// empty for unparseable input.
	expectEqual(t, mustEval(t, "v.toDate()", map[string]any{"v": "2026-05-17"}), []any{"2026-05-17"})
	got, _ := Eval("v.toDate()", map[string]any{"v": "garbage"})
	if len(got) != 0 {
		t.Fatalf("toDate of garbage must be empty; got %v", got)
	}
}

func TestEval_ToDateTime_Passthrough(t *testing.T) {
	expectEqual(t,
		mustEval(t, "v.toDateTime()", map[string]any{"v": "2026-05-17T10:30:00+02:00"}),
		[]any{"2026-05-17T10:30:00+02:00"})
}

func TestEval_ToTime_Passthrough(t *testing.T) {
	expectEqual(t, mustEval(t, "v.toTime()", map[string]any{"v": "10:30:00"}), []any{"10:30:00"})
}

func TestEval_ConvertsToInteger(t *testing.T) {
	expectEqual(t, mustEval(t, "v.convertsToInteger()", map[string]any{"v": "42"}), []any{true})
	expectEqual(t, mustEval(t, "v.convertsToInteger()", map[string]any{"v": "abc"}), []any{false})
}

func TestEval_ConvertsToDecimal(t *testing.T) {
	expectEqual(t, mustEval(t, "v.convertsToDecimal()", map[string]any{"v": "3.14"}), []any{true})
	expectEqual(t, mustEval(t, "v.convertsToDecimal()", map[string]any{"v": "abc"}), []any{false})
}

func TestEval_ConvertsToString(t *testing.T) {
	// Everything that isn't empty converts to string.
	expectEqual(t, mustEval(t, "v.convertsToString()", map[string]any{"v": int64(7)}), []any{true})
	expectEqual(t, mustEval(t, "v.convertsToString()", map[string]any{"v": "x"}), []any{true})
}

func TestEval_ConvertsToBoolean(t *testing.T) {
	expectEqual(t, mustEval(t, "v.convertsToBoolean()", map[string]any{"v": "true"}), []any{true})
	expectEqual(t, mustEval(t, "v.convertsToBoolean()", map[string]any{"v": "yes"}), []any{false})
}

func TestEval_ConvertsToDate(t *testing.T) {
	expectEqual(t, mustEval(t, "v.convertsToDate()", map[string]any{"v": "2026-05-17"}), []any{true})
	expectEqual(t, mustEval(t, "v.convertsToDate()", map[string]any{"v": "garbage"}), []any{false})
}

func TestEval_ConvertsToDateTime(t *testing.T) {
	expectEqual(t, mustEval(t, "v.convertsToDateTime()", map[string]any{"v": "2026-05-17T10:30:00Z"}), []any{true})
	expectEqual(t, mustEval(t, "v.convertsToDateTime()", map[string]any{"v": "garbage"}), []any{false})
}

func TestEval_ConvertsToTime(t *testing.T) {
	expectEqual(t, mustEval(t, "v.convertsToTime()", map[string]any{"v": "10:30:00"}), []any{true})
	expectEqual(t, mustEval(t, "v.convertsToTime()", map[string]any{"v": "garbage"}), []any{false})
}

// --- Date scalars: now / today ---
// Tests check shape (length + separator placement) to detect formatter regressions without freezing the clock.

func TestEval_Today_Shape(t *testing.T) {
	got := mustEval(t, "today()", nil)
	if len(got) != 1 {
		t.Fatalf("today() must return 1 element; got %v", got)
	}
	s, ok := got[0].(string)
	if !ok || len(s) != len("2026-05-17") {
		t.Fatalf("today() should be YYYY-MM-DD; got %q", got[0])
	}
	if s[4] != '-' || s[7] != '-' {
		t.Fatalf("today() malformed: %q", s)
	}
}

func TestEval_Now_Shape(t *testing.T) {
	got := mustEval(t, "now()", nil)
	if len(got) != 1 {
		t.Fatalf("now() must return 1 element; got %v", got)
	}
	s, ok := got[0].(string)
	if !ok || len(s) < len("2026-05-17T10:30:00Z") {
		t.Fatalf("now() should be ISO-8601 dateTime; got %q", got[0])
	}
	if s[4] != '-' || s[7] != '-' || s[10] != 'T' {
		t.Fatalf("now() malformed: %q", s)
	}
}
