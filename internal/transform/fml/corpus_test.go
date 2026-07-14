package fml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// knownGaps pins fixtures whose grammar features the parser does not yet
// cover. This map is intentionally empty. When a future regression reintroduces
// a gap, add it here with the expected error substring so the suite continues
// to document the drift instead of silently skipping the fixture.
var knownGaps = map[string]string{}

// TestFML_Parse_HL7Corpus_RoundTrip walks every `*.fml` fixture vendored from
// the official HL7 FHIR v5.0.0 R5 structure-mapping test corpus and asserts
// the parser's behaviour against it. See `testdata/hl7corpus/README.md` for
// source/SHA/license provenance.
func TestFML_Parse_HL7Corpus_RoundTrip(t *testing.T) {
	const dir = "testdata/hl7corpus"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus dir: %v", err)
	}

	var found int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".fml" {
			continue
		}
		found++
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			_, parseErr := Parse(string(src))
			wantErrSubstr, isGap := knownGaps[name]
			switch {
			case !isGap && parseErr != nil:
				t.Fatalf("%s: parse failed: %v", name, parseErr)
			case isGap && parseErr == nil:
				t.Fatalf("%s: parse now succeeds — remove this fixture from knownGaps in corpus_test.go", name)
			case isGap && !strings.Contains(parseErr.Error(), wantErrSubstr):
				t.Fatalf("%s: gap error drifted.\n  want substring: %q\n  got error:      %v\n  if the underlying grammar feature now works, remove from knownGaps; otherwise update the pinned substring.",
					name, wantErrSubstr, parseErr)
			}
		})
	}
	if found == 0 {
		t.Fatal("no *.fml fixtures found in testdata/hl7corpus — vendoring drift")
	}
}
