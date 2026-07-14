package conceptmap

import "testing"

// R4→R5 conversion is lossy: several R4 codes collapse to one R5 code.
// The Lossy return value lets callers preserve the original code in an extension.
func TestVocab_RelationshipFromEquivalence_FullMatrix(t *testing.T) {
	cases := []struct {
		equivalence  string
		relationship string
		lossy        bool
	}{
		{"equivalent", "equivalent", false},
		{"equal", "equivalent", true},
		{"wider", "source-is-broader-than-target", false},
		{"subsumes", "source-is-broader-than-target", true},
		{"narrower", "source-is-narrower-than-target", false},
		{"specializes", "source-is-narrower-than-target", true},
		{"inexact", "related-to", true},
		{"relatedto", "related-to", false},
		{"unmatched", "not-related-to", false},
		{"disjoint", "not-related-to", true},
	}
	for _, c := range cases {
		t.Run(c.equivalence, func(t *testing.T) {
			got, lossy := RelationshipFromEquivalence(c.equivalence)
			if got != c.relationship {
				t.Fatalf("RelationshipFromEquivalence(%q) = %q, want %q", c.equivalence, got, c.relationship)
			}
			if lossy != c.lossy {
				t.Fatalf("RelationshipFromEquivalence(%q) lossy=%v, want %v", c.equivalence, lossy, c.lossy)
			}
		})
	}
}

func TestVocab_EquivalenceFromRelationship_RoundTripPrimary(t *testing.T) {
	// Pick the primary R4 spelling for each R5 relationship (the one that round-trips losslessly).
	cases := map[string]string{
		"equivalent":                     "equivalent",
		"source-is-broader-than-target":  "wider",
		"source-is-narrower-than-target": "narrower",
		"related-to":                     "relatedto",
		"not-related-to":                 "unmatched",
	}
	for rel, want := range cases {
		got := EquivalenceFromRelationship(rel)
		if got != want {
			t.Fatalf("EquivalenceFromRelationship(%q) = %q, want %q", rel, got, want)
		}
		back, _ := RelationshipFromEquivalence(got)
		if back != rel {
			t.Fatalf("R5→R4→R5 not stable for %q: ended on %q", rel, back)
		}
	}
}

func TestVocab_UnknownInputsPassThrough(t *testing.T) {
	if rel, _ := RelationshipFromEquivalence(""); rel != "" {
		t.Fatalf("empty equivalence should produce empty relationship, got %q", rel)
	}
	if eq := EquivalenceFromRelationship("nonsense"); eq != "" {
		t.Fatalf("unknown relationship should produce empty string, got %q", eq)
	}
}

// group.sourceVersion and group.targetVersion must round-trip through JSON.
func TestGroup_SourceTargetVersion_RoundTrip(t *testing.T) {
	g := Group{
		Source:        "http://hl7.org/fhir/sid/icd-10-cm",
		SourceVersion: "2024",
		Target:        "http://snomed.info/sct",
		TargetVersion: "http://snomed.info/sct/900000000000207008/version/20240301",
		Element:       []Element{{Code: "x", Target: []Target{{Code: "y", Relationship: "equivalent"}}}},
	}
	cm := &ConceptMap{
		ResourceType: "ConceptMap",
		Status:       "active",
		Group:        []Group{g},
	}
	raw, err := jsonMarshal(cm)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ConceptMap
	if err := jsonUnmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Group[0].SourceVersion != "2024" {
		t.Fatalf("group.sourceVersion lost in JSON round-trip: %#v", got.Group[0])
	}
	if got.Group[0].TargetVersion == "" {
		t.Fatalf("group.targetVersion lost in JSON round-trip: %#v", got.Group[0])
	}
}
