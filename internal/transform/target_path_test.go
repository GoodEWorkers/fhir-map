package transform

import (
	"reflect"
	"testing"
)

// N>1 writes to the same bare key accumulate into an ordered []any.
func TestWriteTargetElement_RepeatedBareWrite_PromotesToList(t *testing.T) {
	holder := map[string]any{}
	writeTargetElement(holder, "OBR", "OBR|0001|seg1", true, "")
	writeTargetElement(holder, "OBR", "OBR|0002|seg2", true, "")
	writeTargetElement(holder, "OBR", "OBR|0003|seg3", true, "")

	want := []any{"OBR|0001|seg1", "OBR|0002|seg2", "OBR|0003|seg3"}
	got, ok := holder["OBR"].([]any)
	if !ok {
		t.Fatalf("OBR: want []any of 3 segments, got %T = %v", holder["OBR"], holder["OBR"])
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OBR list mismatch:\n want %v\n got  %v", want, got)
	}
}

// A single bare write stays a scalar (MSH-1, PID-5, … must not become lists).
func TestWriteTargetElement_SingleBareWrite_StaysScalar(t *testing.T) {
	holder := map[string]any{}
	writeTargetElement(holder, "MSH-1", `^~\&`, true, "")

	if got, ok := holder["MSH-1"].(string); !ok || got != `^~\&` {
		t.Fatalf("MSH-1: want scalar string %q, got %T = %v", `^~\&`, holder["MSH-1"], holder["MSH-1"])
	}
}

// List-mode markers ([+]/[=]) are unaffected by the promote-on-repeat path.
func TestWriteTargetElement_ListModeMarker_Unaffected(t *testing.T) {
	holder := map[string]any{}
	writeTargetElement(holder, "entry[+].resource", "first", false, "")
	writeTargetElement(holder, "entry[+].resource", "second", false, "")

	lst, ok := holder["entry"].([]any)
	if !ok || len(lst) != 2 {
		t.Fatalf("entry: want []any len 2, got %T = %v", holder["entry"], holder["entry"])
	}
}
