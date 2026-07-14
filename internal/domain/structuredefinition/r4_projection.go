package structuredefinition

// ProjectToR4 returns sd unchanged; R4 and R5 StructureDefinitions are identical in v1.
// This is intentionally a no-op (not a stub) so handlers can call it uniformly.
func ProjectToR4(sd *StructureDefinition) *StructureDefinition {
	return sd
}
