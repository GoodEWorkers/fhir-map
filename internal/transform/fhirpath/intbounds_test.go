package fhirpath

import (
	"math"
	"testing"
)

func TestToInt_clampsToInt32Range(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int
	}{
		{"zero", 0, 0},
		{"small positive", 5, 5},
		{"small negative", -5, -5},
		{"max int32", math.MaxInt32, math.MaxInt32},
		{"min int32", math.MinInt32, math.MinInt32},
		{"just above max int32", math.MaxInt32 + 1, math.MaxInt32},
		{"just below min int32", math.MinInt32 - 1, math.MinInt32},
		{"max int64", math.MaxInt64, math.MaxInt32},
		{"min int64", math.MinInt64, math.MinInt32},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toInt(c.in); got != c.want {
				t.Errorf("toInt(%d) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// Out-of-range indices and arguments must not overflow the int conversion or
// panic — they clamp to the collection/string bounds.
func TestEval_outOfRangeIndicesAndArgs(t *testing.T) {
	subject := map[string]any{
		"xs": []any{"a", "b", "c"},
		"s":  "hello",
	}
	const huge = "9999999999" // > math.MaxInt32

	if got := mustEval(t, "xs["+huge+"]", subject); len(got) != 0 {
		t.Errorf("index past end must be empty; got %v", got)
	}
	if got := mustEval(t, "xs.skip("+huge+")", subject); len(got) != 0 {
		t.Errorf("skip past end must be empty; got %v", got)
	}
	expectEqual(t, mustEval(t, "xs.take("+huge+")", subject), []any{"a", "b", "c"})
	if got := mustEval(t, "s.substring("+huge+")", subject); len(got) != 0 {
		t.Errorf("substring start past end must be empty; got %v", got)
	}
	expectEqual(t, mustEval(t, "s.substring(0, "+huge+")", subject), []any{"hello"})
}
