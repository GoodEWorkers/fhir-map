package fhirpath

import "testing"

// $this is the FHIRPath operator; mapping suites use it heavily in source.condition expressions.
func TestEval_DollarThis_Exists(t *testing.T) {
	got := mustEval(t, "$this.exists()", "anything")
	expectEqual(t, got, []any{true})
}

func TestEval_DollarThis_EqualsLiteral(t *testing.T) {
	got := mustEval(t, "$this = 'ORU'", "ORU")
	expectEqual(t, got, []any{true})
	got = mustEval(t, "$this = 'ORU'", "ADT")
	expectEqual(t, got, []any{false})
}

func TestEval_DollarThis_AndEquality(t *testing.T) {
	got := mustEval(t, "$this.exists() and $this = 'ORU'", "ORU")
	expectEqual(t, got, []any{true})
}
