package fhir

import "testing"

func findPart(parts []Parameter, name string) *Parameter {
	for i := range parts {
		if parts[i].Name == name {
			return &parts[i]
		}
	}
	return nil
}

func TestNewTranslateResponse_R5(t *testing.T) {
	matches := []TranslateMatch{{
		Relationship: "equivalent",
		Concept:      &Coding{System: "http://tgt", Code: "X", Display: "X disp"},
		OriginMap:    "http://maps/a",
		DependsOn: []TranslateMatchDependency{
			{Attribute: "http://attr/code", ValueCode: "c1"},
			{Attribute: "http://attr/str", ValueString: "s1"},
		},
		Product: []TranslateMatchDependency{
			{Attribute: "http://attr/coding", ValueCoding: &Coding{System: "s", Code: "p1"}},
		},
	}}
	p := NewTranslateResponse(true, "matched", matches)

	if p.ResourceType != ResourceTypeParameters {
		t.Fatalf("resourceType = %q", p.ResourceType)
	}
	res := findPart(p.Parameter, "result")
	if res == nil || res.ValueBoolean == nil || !*res.ValueBoolean {
		t.Fatalf("result param missing/false: %+v", res)
	}
	if findPart(p.Parameter, "message") == nil {
		t.Fatalf("message param missing")
	}
	match := findPart(p.Parameter, "match")
	if match == nil {
		t.Fatalf("match param missing")
	}
	// R5 uses the `relationship` qualifier part.
	if rel := findPart(match.Part, "relationship"); rel == nil || rel.ValueCode != "equivalent" {
		t.Fatalf("expected relationship=equivalent, got %+v", rel)
	}
	if c := findPart(match.Part, "concept"); c == nil || c.ValueCoding == nil || c.ValueCoding.Code != "X" {
		t.Fatalf("concept part wrong: %+v", c)
	}
	if findPart(match.Part, "originMap") == nil {
		t.Fatalf("originMap part missing")
	}
	if findPart(match.Part, "dependsOn") == nil || findPart(match.Part, "product") == nil {
		t.Fatalf("dependsOn/product parts missing")
	}
}

func TestNewTranslateResponseFor_R4_WithIssue(t *testing.T) {
	matches := []TranslateMatch{{
		Relationship: "source-is-broader-than-target",
		Concept:      &Coding{System: "http://tgt", Code: "Y"},
	}}
	issue := TranslateIssue{Code: "not-found", Diagnostics: "no map"}
	p := NewTranslateResponseFor(false, "", matches, VersionR4, issue)

	if findPart(p.Parameter, "message") != nil {
		t.Fatalf("empty message should not produce a message param")
	}
	if findPart(p.Parameter, "issue") == nil {
		t.Fatalf("issue param missing")
	}
	match := findPart(p.Parameter, "match")
	if match == nil {
		t.Fatalf("match param missing")
	}
	// R4 uses the `equivalence` qualifier with translated wire codes.
	eq := findPart(match.Part, "equivalence")
	if eq == nil || eq.ValueCode != "wider" {
		t.Fatalf("expected equivalence=wider (R4 wire), got %+v", eq)
	}
}

func TestRelationshipToEquivalenceR4Wire(t *testing.T) {
	cases := map[string]string{
		"equivalent":                     "equivalent",
		"source-is-broader-than-target":  "wider",
		"source-is-narrower-than-target": "narrower",
		"related-to":                     "relatedto",
		"not-related-to":                 "unmatched",
		"something-else":                 "something-else", // default passthrough
	}
	for in, want := range cases {
		if got := relationshipToEquivalenceR4Wire(in); got != want {
			t.Fatalf("relationshipToEquivalenceR4Wire(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFHIRVersion_StringAndURLPrefix(t *testing.T) {
	if VersionR4.String() != "4.0.1" || VersionR5.String() != "5.0.0" {
		t.Fatalf("version String() wrong: R4=%q R5=%q", VersionR4.String(), VersionR5.String())
	}
	if VersionR4.URLPrefix() != "R4" || VersionR5.URLPrefix() != "" {
		t.Fatalf("URLPrefix wrong: R4=%q R5=%q", VersionR4.URLPrefix(), VersionR5.URLPrefix())
	}
}
