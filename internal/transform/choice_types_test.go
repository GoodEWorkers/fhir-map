package transform

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeChoiceTypes(t *testing.T) {
	bundle := map[string]any{
		"resourceType": "Bundle",
		"entry": []any{
			obsWrap(map[string]any{"system": "http://unitsofmeasure.org", "unit": "mg/l", "value": 4.2}), // Quantity
			obsWrap(map[string]any{"coding": []any{map[string]any{"code": "x"}}}),                        // CodeableConcept
			obsWrap("Positive"), // String
			map[string]any{"resource": map[string]any{"resourceType": "DiagnosticReport", "effective": "2026-05-30T09:00:00Z"}}, // DateTime
		},
	}
	normalizeChoiceTypes(bundle)

	res := func(i int) map[string]any {
		return bundle["entry"].([]any)[i].(map[string]any)["resource"].(map[string]any)
	}
	for _, i := range []int{0, 1, 2} {
		_, stillBase := res(i)["value"]
		assert.False(t, stillBase, "entry %d: base `value` must be renamed", i)
	}
	assert.Contains(t, res(0), "valueQuantity")
	assert.Contains(t, res(1), "valueCodeableConcept")
	assert.Contains(t, res(2), "valueString")
	assert.Contains(t, res(3), "effectiveDateTime")
}

// A non-resource object (no resourceType) is left untouched.
func TestNormalizeChoiceTypes_IgnoresNonResources(t *testing.T) {
	m := map[string]any{"value": map[string]any{"system": "s", "unit": "u"}}
	normalizeChoiceTypes(m)
	assert.Contains(t, m, "value", "no resourceType -> not a FHIR resource -> left as-is")
}

func obsWrap(value any) map[string]any {
	return map[string]any{"resource": map[string]any{"resourceType": "Observation", "value": value}}
}
