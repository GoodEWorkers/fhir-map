package structuredefinition

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStructureDefinition_JSONRoundTrip(t *testing.T) {
	sd := &StructureDefinition{
		ResourceType:   "StructureDefinition",
		URL:            "http://example.org/StructureDefinition/MyProfile",
		Version:        "1.0.0",
		Name:           "MyProfile",
		Title:          "My Profile",
		Status:         "active",
		Kind:           "resource",
		Type:           "Patient",
		BaseDefinition: "http://hl7.org/fhir/StructureDefinition/Patient",
		Derivation:     "constraint",
	}

	raw, err := json.Marshal(sd)
	require.NoError(t, err)

	var got StructureDefinition
	require.NoError(t, json.Unmarshal(raw, &got))

	assert.Equal(t, sd.URL, got.URL)
	assert.Equal(t, sd.Status, got.Status)
	assert.Equal(t, sd.Kind, got.Kind)
	assert.Equal(t, sd.Type, got.Type)
	assert.Equal(t, sd.BaseDefinition, got.BaseDefinition)
	assert.Equal(t, sd.Derivation, got.Derivation)
}

func TestStructureDefinition_DifferentialAndSnapshot_OpaqueRoundTrip(t *testing.T) {
	raw := `{"resourceType":"StructureDefinition","url":"http://example.org/sd","name":"Test","status":"active","kind":"resource","type":"Patient","differential":{"element":[{"id":"Patient.id","path":"Patient.id"}]},"snapshot":{"element":[{"id":"Patient","path":"Patient"}]}}`
	var sd StructureDefinition
	require.NoError(t, json.Unmarshal([]byte(raw), &sd))
	assert.NotNil(t, sd.Differential)
	assert.NotNil(t, sd.Snapshot)

	out, err := json.Marshal(sd)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(out, &m))
	_, hasDiff := m["differential"]
	assert.True(t, hasDiff, "differential must round-trip opaquely")
}

func TestStructureDefinition_InternalTimestamps_NotSerialized(t *testing.T) {
	sd := &StructureDefinition{
		ResourceType: "StructureDefinition",
		Name:         "Test",
		Status:       "active",
		URL:          "http://example.org/sd/test",
		Type:         "Patient",
	}
	raw, err := json.Marshal(sd)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	_, hasCreatedAt := m["createdAt"]
	_, hasUpdatedAt := m["updatedAt"]
	assert.False(t, hasCreatedAt, "createdAt must not be in JSON")
	assert.False(t, hasUpdatedAt, "updatedAt must not be in JSON")
}

func TestValidate_Strict_RequiresURL(t *testing.T) {
	sd := &StructureDefinition{ResourceType: "StructureDefinition", Name: "X", Status: "active", Type: "Patient"}
	errs := sd.ValidateMode(ModeStrict)
	assert.True(t, containsAny(errs, "url"), "strict should flag missing url; got %v", errs)
}

func TestValidate_Strict_RequiresName(t *testing.T) {
	sd := &StructureDefinition{ResourceType: "StructureDefinition", URL: "http://x.org/sd", Status: "active", Type: "Patient"}
	errs := sd.ValidateMode(ModeStrict)
	assert.True(t, containsAny(errs, "name"), "strict should flag missing name; got %v", errs)
}

func TestValidate_Strict_RequiresStatus(t *testing.T) {
	sd := &StructureDefinition{ResourceType: "StructureDefinition", URL: "http://x.org/sd", Name: "X", Type: "Patient"}
	errs := sd.ValidateMode(ModeStrict)
	assert.True(t, containsAny(errs, "status"), "strict should flag missing status; got %v", errs)
}

func TestValidate_Strict_RequiresType(t *testing.T) {
	sd := &StructureDefinition{ResourceType: "StructureDefinition", URL: "http://x.org/sd", Name: "X", Status: "active"}
	errs := sd.ValidateMode(ModeStrict)
	assert.True(t, containsAny(errs, "type"), "strict should flag missing type; got %v", errs)
}

func TestValidate_Strict_RejectsInvalidStatus(t *testing.T) {
	sd := &StructureDefinition{ResourceType: "StructureDefinition", URL: "http://x.org/sd", Name: "X", Status: "bogus", Type: "Patient"}
	errs := sd.ValidateMode(ModeStrict)
	assert.True(t, containsAny(errs, "status"), "strict should reject invalid status; got %v", errs)
}

func TestValidate_Strict_RejectsInvalidKind(t *testing.T) {
	sd := &StructureDefinition{ResourceType: "StructureDefinition", URL: "http://x.org/sd", Name: "X", Status: "active", Kind: "not-valid", Type: "Patient"}
	errs := sd.ValidateMode(ModeStrict)
	assert.True(t, containsAny(errs, "kind"), "strict should reject invalid kind; got %v", errs)
}

func TestValidate_Strict_RejectsInvalidDerivation(t *testing.T) {
	sd := &StructureDefinition{ResourceType: "StructureDefinition", URL: "http://x.org/sd", Name: "X", Status: "active", Type: "Patient", Derivation: "invalid"}
	errs := sd.ValidateMode(ModeStrict)
	assert.True(t, containsAny(errs, "derivation"), "strict should reject invalid derivation; got %v", errs)
}

func TestValidate_Strict_AllowsEmptyDerivation(t *testing.T) {
	sd := &StructureDefinition{ResourceType: "StructureDefinition", URL: "http://x.org/sd", Name: "X", Status: "active", Type: "Patient", Derivation: ""}
	errs := sd.ValidateMode(ModeStrict)
	for _, e := range errs {
		assert.NotContains(t, e, "derivation", "empty derivation should be allowed for HL7 base types")
	}
}

func TestValidate_Lenient_SkipsVocabularyChecks(t *testing.T) {
	sd := &StructureDefinition{
		ResourceType: "StructureDefinition",
		URL:          "http://x.org/sd",
		Name:         "X",
		Status:       "bogus-status",
		Kind:         "not-valid-kind",
		Type:         "Patient",
		Derivation:   "invalid-derivation",
	}
	errs := sd.ValidateMode(ModeLenient)
	assert.Len(t, errs, 0, "lenient mode should skip vocabulary checks; got %v", errs)
}

func TestValidate_WrongResourceType(t *testing.T) {
	sd := &StructureDefinition{ResourceType: "Patient", URL: "http://x.org/sd", Name: "X", Status: "active", Type: "Patient"}
	errs := sd.ValidateMode(ModeStrict)
	assert.True(t, containsAny(errs, "resourceType"), "wrong resourceType should be flagged; got %v", errs)
}

func TestValidate_NoArgShimIsStrict(t *testing.T) {
	sd := &StructureDefinition{ResourceType: "StructureDefinition"}
	assert.NotEmpty(t, sd.Validate(), "Validate() with no args must be strict")
}

func TestProjectToR4_IsPassThrough(t *testing.T) {
	sd := &StructureDefinition{
		ResourceType: "StructureDefinition",
		URL:          "http://example.org/sd",
		Name:         "X",
		Status:       "active",
		Type:         "Patient",
	}
	result := ProjectToR4(sd)
	assert.Same(t, sd, result, "ProjectToR4 must return the same pointer (no-op)")
}

func containsAny(errs []string, needle string) bool {
	for _, e := range errs {
		if len(e) >= len(needle) {
			for i := 0; i+len(needle) <= len(e); i++ {
				if e[i:i+len(needle)] == needle {
					return true
				}
			}
		}
	}
	return false
}
