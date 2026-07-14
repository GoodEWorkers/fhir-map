package transform

import "github.com/goodeworkers/fhir-map/internal/domain/structuredefinition/hl7base"

// resourceTypeNames is the set of FHIR type names whose embedded HL7 base
// StructureDefinition has kind "resource". Built once from the embedded base.
//
// The `create` transform uses this to decide whether create('X') produces a
// resource (stamp resourceType — required for valid FHIR inside a Bundle) or a
// complex datatype such as Reference / Coding / Quantity, which must NOT carry
// resourceType.
var resourceTypeNames = func() map[string]bool {
	m := make(map[string]bool)
	for _, sd := range hl7base.BaseTypes {
		if sd != nil && sd.Kind == "resource" && sd.Type != "" {
			m[sd.Type] = true
		}
	}
	return m
}()

// isResourceType reports whether name is a FHIR resource type (vs a datatype).
func isResourceType(name string) bool { return resourceTypeNames[name] }
