package conceptmap

import (
	"strings"
	"testing"
)

// TestValidate_GroupIndexLabel_BeyondNine verifies group indices format correctly with strconv.Itoa beyond single digits.
func TestValidate_GroupIndexLabel_BeyondNine(t *testing.T) {
	cm := &ConceptMap{
		ResourceType: "ConceptMap",
		Status:       "active",
	}
	for i := 0; i < 11; i++ {
		g := Group{
			Source: "http://example.org/src",
			Target: "http://example.org/tgt",
		}
		if i < 10 {
			g.Element = []Element{{Code: "x", Target: []Target{{Code: "y", Relationship: "equivalent"}}}}
		}
		cm.Group = append(cm.Group, g)
	}

	errs := cm.Validate()
	if len(errs) == 0 {
		t.Fatalf("expected at least one validation error for group[10]; got none")
	}
	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, "group[10]") {
		t.Fatalf("expected error to reference group[10]; got:\n%s", joined)
	}
}

// TestValidate_ElementAndTargetIndexLabels_BeyondNine verifies element and target indices format correctly beyond single digits.
func TestValidate_ElementAndTargetIndexLabels_BeyondNine(t *testing.T) {
	g := Group{
		Source: "http://example.org/src",
		Target: "http://example.org/tgt",
	}
	for i := 0; i < 11; i++ {
		e := Element{Code: "src" + string(rune('a'+i))}
		if i < 10 {
			e.Target = []Target{{Code: "tgt", Relationship: "equivalent"}}
		} else {
			for j := 0; j < 11; j++ {
				rel := "equivalent"
				if j == 10 {
					rel = "bogus-relationship"
				}
				e.Target = append(e.Target, Target{Code: "tgt", Relationship: rel})
			}
		}
		g.Element = append(g.Element, e)
	}
	cm := &ConceptMap{
		ResourceType: "ConceptMap",
		Status:       "active",
		Group:        []Group{g},
	}

	errs := cm.Validate()
	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, "element[10]") {
		t.Fatalf("expected error to reference element[10]; got:\n%s", joined)
	}
	if !strings.Contains(joined, "target[10]") {
		t.Fatalf("expected error to reference target[10]; got:\n%s", joined)
	}
}
