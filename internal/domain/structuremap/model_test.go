package structuremap

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStructureMap_JSONRoundTrip verifies that the domain model round-trips through JSON,
// preserving every field we currently serve via CRUD.
func TestStructureMap_JSONRoundTrip(t *testing.T) {
	sm := &StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/StructureMap/qr-to-patient",
		Version:      "1.0.0",
		Name:         "QuestionnaireResponseToPatient",
		Status:       "active",
		Structure: []Structure{
			{
				URL:   "http://hl7.org/fhir/StructureDefinition/QuestionnaireResponse",
				Mode:  "source",
				Alias: "QR",
			},
			{
				URL:   "http://hl7.org/fhir/StructureDefinition/Patient",
				Mode:  "target",
				Alias: "P",
			},
		},
		Import: []string{"http://example.org/StructureMap/helpers"},
		Group: []Group{
			{
				Name:     "MapQRtoPatient",
				TypeMode: "types",
				Input: []Input{
					{Name: "qr", Type: "QR", Mode: "source"},
					{Name: "patient", Type: "P", Mode: "target"},
				},
				Rule: []Rule{
					{
						Name: "name",
						Source: []Source{{
							Context:   "qr",
							Element:   "item",
							Variable:  "i",
							Condition: "i.linkId = 'name'",
						}},
						Target: []Target{{
							Context:   "patient",
							Element:   "name",
							Variable:  "n",
							Transform: "create",
						}},
						Rule: []Rule{
							{
								Name:   "given",
								Source: []Source{{Context: "i", Element: "answer.value", Variable: "v"}},
								Target: []Target{{
									Context:   "n",
									Element:   "given",
									Transform: "copy",
									Parameter: []Parameter{{ValueID: "v"}},
								}},
							},
						},
					},
				},
			},
		},
	}

	raw, err := json.Marshal(sm)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got StructureMap
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.URL != sm.URL || got.Status != sm.Status {
		t.Fatalf("canonical fields lost: %+v", got)
	}
	if len(got.Group) != 1 || len(got.Group[0].Rule) != 1 {
		t.Fatalf("group/rule round-trip failed: %+v", got.Group)
	}
	if got.Group[0].Rule[0].Source[0].Condition != "i.linkId = 'name'" {
		t.Fatalf("Source.Condition lost: %#v", got.Group[0].Rule[0].Source[0])
	}
	if got.Group[0].Rule[0].Target[0].Transform != "create" {
		t.Fatalf("Target.Transform lost: %#v", got.Group[0].Rule[0].Target[0])
	}
	if len(got.Group[0].Rule[0].Rule) != 1 ||
		got.Group[0].Rule[0].Rule[0].Target[0].Parameter[0].ValueID != "v" {
		t.Fatalf("nested rule + parameter lost: %#v", got.Group[0].Rule[0].Rule)
	}
}

// TestStructureMap_VersionAlgorithmString_RoundTrip verifies R5 versionAlgorithm[x] survives persistence
// and can be stripped on the R4 wire.
func TestStructureMap_VersionAlgorithmString_RoundTrip(t *testing.T) {
	sm := &StructureMap{
		ResourceType:           "StructureMap",
		Name:                   "VerAlgTest",
		Status:                 "active",
		VersionAlgorithmString: "semver",
		Group: []Group{{
			Name:  "g",
			Input: []Input{{Name: "src", Mode: "source"}},
			Rule:  []Rule{{Name: "r"}},
		}},
	}
	raw, err := json.Marshal(sm)
	require.NoError(t, err)

	var got StructureMap
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "semver", got.VersionAlgorithmString, "versionAlgorithmString must survive marshal/unmarshal")
	assert.Nil(t, got.VersionAlgorithmCoding, "VersionAlgorithmCoding absent — must remain nil")
}

// FHIR's defaultValue[x] choice-type convention: wire form uses
// defaultValueString / defaultValueInteger / defaultValueBoolean, NOT a flat `defaultValue` key.
func TestSource_UnmarshalJSON_DefaultValueString(t *testing.T) {
	raw := `{"context": "src", "defaultValueString": "FALLBACK"}`
	var s Source
	require.NoError(t, json.Unmarshal([]byte(raw), &s))
	assert.Equal(t, "src", s.Context)
	assert.Equal(t, "String", s.DefaultValueType)
	assert.JSONEq(t, `"FALLBACK"`, string(s.DefaultValue))
}

func TestSource_UnmarshalJSON_DefaultValueInteger(t *testing.T) {
	raw := `{"context": "src", "defaultValueInteger": 42}`
	var s Source
	require.NoError(t, json.Unmarshal([]byte(raw), &s))
	assert.Equal(t, "Integer", s.DefaultValueType)
	assert.JSONEq(t, `42`, string(s.DefaultValue))
}

func TestSource_UnmarshalJSON_DefaultValueBoolean(t *testing.T) {
	raw := `{"context": "src", "defaultValueBoolean": true}`
	var s Source
	require.NoError(t, json.Unmarshal([]byte(raw), &s))
	assert.Equal(t, "Boolean", s.DefaultValueType)
	assert.JSONEq(t, `true`, string(s.DefaultValue))
}

// Backward compatibility: the bare `defaultValue` key still works for direct struct construction.
func TestSource_UnmarshalJSON_DefaultValueBareKey(t *testing.T) {
	raw := `{"context": "src", "defaultValue": "FLAT"}`
	var s Source
	require.NoError(t, json.Unmarshal([]byte(raw), &s))
	assert.Equal(t, "", s.DefaultValueType)
	assert.JSONEq(t, `"FLAT"`, string(s.DefaultValue))
}

func TestSource_MarshalJSON_RoundTripsTypedDefault(t *testing.T) {
	raw := `{"context": "src", "defaultValueString": "FALLBACK"}`
	var s Source
	require.NoError(t, json.Unmarshal([]byte(raw), &s))
	out, err := json.Marshal(s)
	require.NoError(t, err)
	var roundTrip map[string]any
	require.NoError(t, json.Unmarshal(out, &roundTrip))
	assert.Equal(t, "FALLBACK", roundTrip["defaultValueString"])
	_, hasFlat := roundTrip["defaultValue"]
	assert.False(t, hasFlat, "must NOT emit the flat key when the typed form was supplied")
}

func TestSource_MarshalJSON_OmitsDefaultWhenAbsent(t *testing.T) {
	s := Source{Context: "src"}
	out, err := json.Marshal(s)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "defaultValue")
}

// TestValidate_Strict_RejectsMissingName verifies strict mode catches missing required fields;
// lenient mode skips vocabulary-binding checks.
func TestValidate_Strict_RejectsMissingName(t *testing.T) {
	sm := &StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/sm/no-name",
		Status:       "active",
		Group:        []Group{{Name: "g", Input: []Input{{Name: "src", Mode: "source"}}, Rule: []Rule{{Name: "r"}}}},
	}
	errs := sm.ValidateMode(ModeStrict)
	if !containsAny(errs, "name") {
		t.Fatalf("strict mode should flag missing top-level name; got %v", errs)
	}
}

func TestValidate_Strict_RejectsMissingStatus(t *testing.T) {
	sm := &StructureMap{
		ResourceType: "StructureMap",
		Name:         "X",
		URL:          "http://example.org/sm/no-status",
		Group:        []Group{{Name: "g", Input: []Input{{Name: "src", Mode: "source"}}, Rule: []Rule{{Name: "r"}}}},
	}
	errs := sm.ValidateMode(ModeStrict)
	if !containsAny(errs, "status") {
		t.Fatalf("strict mode should flag missing status; got %v", errs)
	}
}

// Structural requirements (group, inputs, rules) are enforced even in lenient mode.
func TestValidate_StructuralInvariants_BothModes(t *testing.T) {
	cases := []struct {
		name string
		sm   *StructureMap
		want string
	}{
		{
			name: "empty group list",
			sm:   &StructureMap{ResourceType: "StructureMap", Name: "X", Status: "active"},
			want: "group",
		},
		{
			name: "group with no inputs",
			sm: &StructureMap{
				ResourceType: "StructureMap", Name: "X", Status: "active",
				Group: []Group{{Name: "g", Rule: []Rule{{Name: "r"}}}},
			},
			want: "input",
		},
		{
			name: "group with no rules",
			sm: &StructureMap{
				ResourceType: "StructureMap", Name: "X", Status: "active",
				Group: []Group{{Name: "g", Input: []Input{{Name: "src", Mode: "source"}}}},
			},
			want: "rule",
		},
	}
	for _, c := range cases {
		t.Run(c.name+"/strict", func(t *testing.T) {
			errs := c.sm.ValidateMode(ModeStrict)
			if !containsAny(errs, c.want) {
				t.Fatalf("strict mode missed %q; got %v", c.want, errs)
			}
		})
		t.Run(c.name+"/lenient", func(t *testing.T) {
			errs := c.sm.ValidateMode(ModeLenient)
			if !containsAny(errs, c.want) {
				t.Fatalf("lenient mode must STILL enforce structural invariant %q; got %v", c.want, errs)
			}
		})
	}
}

// Lenient skips vocabulary checks to allow future-vocabulary and vendor-extension fixtures.
func TestValidate_TransformVocabulary_StrictVsLenient(t *testing.T) {
	sm := &StructureMap{
		ResourceType: "StructureMap",
		Name:         "X",
		URL:          "http://example.org/sm/x",
		Status:       "active",
		Group: []Group{{
			Name: "g",
			Input: []Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []Rule{{
				Name:   "r",
				Source: []Source{{Context: "src"}},
				Target: []Target{{Context: "tgt", Transform: "not-a-real-transform"}},
			}},
		}},
	}
	if !containsAny(sm.ValidateMode(ModeStrict), "transform") {
		t.Fatalf("strict should reject bogus transform code")
	}
	for _, e := range sm.ValidateMode(ModeLenient) {
		if e == "" {
			continue
		}
		// no error about transform should appear
		if c := stringContains(e, "transform"); c {
			t.Fatalf("lenient should not flag transform vocab; got %v", sm.ValidateMode(ModeLenient))
		}
	}
}

// Validate() is the strict-mode shim for ergonomic callers.
func TestValidate_NoArgShimIsStrict(t *testing.T) {
	sm := &StructureMap{ResourceType: "StructureMap"} // many issues
	if len(sm.Validate()) == 0 {
		t.Fatal("Validate() with no args must be strict; expected errors for missing required fields")
	}
}

func containsAny(errs []string, needle string) bool {
	for _, e := range errs {
		if stringContains(e, needle) {
			return true
		}
	}
	return false
}

func stringContains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		(len(haystack) > len(needle) && (haystack[:len(needle)] == needle ||
			haystack[len(haystack)-len(needle):] == needle ||
			indexOf(haystack, needle) >= 0)))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
