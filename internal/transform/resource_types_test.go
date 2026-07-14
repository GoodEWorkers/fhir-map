package transform

import "testing"

// Lock the resource-vs-datatype predicate to catch regressions in the embedded base or predicate.
func TestIsResourceType_LabResourcesAndDatatypes(t *testing.T) {
	for _, rt := range []string{"Composition", "DiagnosticReport", "Observation", "ServiceRequest", "Specimen", "Patient"} {
		if !isResourceType(rt) {
			t.Errorf("%s must be recognised as a FHIR resource type (gets resourceType stamped)", rt)
		}
	}
	for _, dt := range []string{"Reference", "Coding", "Quantity", "CodeableConcept", "Identifier", ""} {
		if isResourceType(dt) {
			t.Errorf("%s must NOT be a resource type (datatype/blank — no resourceType stamp)", dt)
		}
	}
}
