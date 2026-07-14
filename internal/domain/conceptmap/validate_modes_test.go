package conceptmap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Strict mode catches every spec violation; Lenient skips vocabulary checks
// so official FHIR ConceptMap examples (some of which violate their own spec)
// can still load from the fixture path.

func TestValidate_Strict_StillCatchesMissingStatus(t *testing.T) {
	cm := &ConceptMap{
		ResourceType: "ConceptMap",
		Group: []Group{{
			Source: "http://src", Target: "http://tgt",
			Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "equivalent"}}}},
		}},
	}
	errs := cm.ValidateMode(ModeStrict)
	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, "status is required") {
		t.Fatalf("strict mode should flag missing status; got: %s", joined)
	}
}

func TestValidate_Lenient_LetsMissingStatusThrough(t *testing.T) {
	cm := &ConceptMap{
		ResourceType: "ConceptMap",
		Group: []Group{{
			Source: "http://src", Target: "http://tgt",
			Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "equivalent"}}}},
		}},
	}
	errs := cm.ValidateMode(ModeLenient)
	for _, e := range errs {
		if strings.Contains(e, "status") {
			t.Fatalf("lenient mode should not flag missing status; got: %v", errs)
		}
	}
}

// Strict catches invalid relationship values; Lenient only enforces structural invariants.
func TestValidate_Strict_CatchesInvalidRelationship(t *testing.T) {
	cm := &ConceptMap{
		ResourceType: "ConceptMap",
		Status:       "active",
		Group: []Group{{
			Source: "http://s", Target: "http://t",
			Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "bogus-relationship"}}}},
		}},
	}
	errs := cm.ValidateMode(ModeStrict)
	if !strings.Contains(strings.Join(errs, "\n"), "invalid relationship") {
		t.Fatalf("strict should flag invalid relationship; got: %v", errs)
	}
}

// Lenient ignores relationship vocabulary: R4 `equivalence` values pass even though R5 wants `relationship`.
func TestValidate_Lenient_TolerantOfRelationshipVocabulary(t *testing.T) {
	cm := &ConceptMap{
		ResourceType: "ConceptMap",
		Status:       "draft",
		Group: []Group{{
			Source: "http://s", Target: "http://t",
			Element: []Element{{Code: "A", Target: []Target{
				{Code: "B", Relationship: "subsumes"}, // R4-only spelling
			}}},
		}},
	}
	errs := cm.ValidateMode(ModeLenient)
	for _, e := range errs {
		if strings.Contains(e, "relationship") {
			t.Fatalf("lenient mode should not reject R4 equivalence vocab; got: %v", errs)
		}
	}
}

// Lenient mode still enforces structural invariants: mutually-exclusive fields, code+valueSet exclusivity, etc.
func TestValidate_Lenient_StillEnforcesStructuralInvariants(t *testing.T) {
	cm := &ConceptMap{
		ResourceType: "ConceptMap",
		Status:       "active",
		Group: []Group{{
			Source: "http://s", Target: "http://t",
			Element: []Element{{
				Code:     "A",
				ValueSet: "http://example.org/vs/foo", // mutually exclusive with Code
				Target:   []Target{{Code: "B", Relationship: "equivalent"}},
			}},
		}},
	}
	errs := cm.ValidateMode(ModeLenient)
	if !strings.Contains(strings.Join(errs, "\n"), "mutually exclusive") {
		t.Fatalf("lenient must still flag the code+valueSet structural violation; got: %v", errs)
	}
}

// Property test: all official ConceptMap fixtures must load via Lenient mode; Strict may reject some.
func TestValidate_Lenient_AcceptsAllOfficialFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "docs", "conceptmap-*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no fixtures found")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			var cm ConceptMap
			if err := json.Unmarshal(raw, &cm); err != nil {
				t.Fatalf("unmarshal %s: %v", f, err)
			}
			errs := cm.ValidateMode(ModeLenient)
			if len(errs) != 0 {
				t.Fatalf("lenient mode unexpectedly rejected %s:\n  - %s",
					filepath.Base(f), strings.Join(errs, "\n  - "))
			}
		})
	}
}

// Validate() with no args is strict for backward compatibility with existing callers.
func TestValidate_NoArgsShimIsStrict(t *testing.T) {
	cm := &ConceptMap{ResourceType: "ConceptMap"} // missing status
	if errs := cm.Validate(); len(errs) == 0 {
		t.Fatalf("Validate() with no args must remain strict; expected status error")
	}
}
