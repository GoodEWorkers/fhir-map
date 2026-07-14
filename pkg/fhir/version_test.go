package fhir

import "testing"

// FHIRVersion must have stable numeric values to prevent iota shuffles from silently breaking wire projection.

func TestFHIRVersion_KnownVersions(t *testing.T) {
	if int(VersionR5) != 0 {
		t.Fatalf("VersionR5 stable numeric value broke: got %d, want 0", VersionR5)
	}
	if int(VersionR4) != 1 {
		t.Fatalf("VersionR4 stable numeric value broke: got %d, want 1", VersionR4)
	}
}

// String() ensures wire-format fhirVersion identifier consistency across packages.
func TestFHIRVersion_String(t *testing.T) {
	cases := map[FHIRVersion]string{
		VersionR4: "4.0.1",
		VersionR5: "5.0.0",
	}
	for v, want := range cases {
		if got := v.String(); got != want {
			t.Fatalf("FHIRVersion(%d).String() = %q, want %q", v, got, want)
		}
	}
}

// URLPrefix returns per-version handler URL prefixes; VersionR5 returns empty string as legacy R5 alias.
func TestFHIRVersion_URLPrefix(t *testing.T) {
	cases := map[FHIRVersion]string{
		VersionR5: "",
		VersionR4: "R4",
	}
	for v, want := range cases {
		if got := v.URLPrefix(); got != want {
			t.Fatalf("FHIRVersion(%d).URLPrefix() = %q, want %q", v, got, want)
		}
	}
}
