package structuredefinition

// ValidationMode mirrors structuremap.ValidationMode with two tiers: strict for client writes, lenient for fixture loading.
type ValidationMode int

const (
	ModeStrict ValidationMode = iota
	ModeLenient
)

var validStatuses = map[string]bool{
	"draft": true, "active": true, "retired": true, "unknown": true,
}

var validKinds = map[string]bool{
	"primitive-type": true, "complex-type": true, "resource": true, "logical": true,
}

var validDerivations = map[string]bool{
	"specialization": true, "constraint": true, "": true,
}

// Validate is the strict-mode shim for ergonomic callers.
func (sd *StructureDefinition) Validate() []string {
	return sd.ValidateMode(ModeStrict)
}

// ValidateMode runs the configured tier of checks: structural invariants (required fields) in both tiers, vocabulary bindings only in strict mode.
func (sd *StructureDefinition) ValidateMode(mode ValidationMode) []string {
	var errs []string

	if sd.ResourceType != "" && sd.ResourceType != "StructureDefinition" {
		errs = append(errs, "resourceType must be 'StructureDefinition'")
	}

	if mode == ModeStrict {
		if sd.URL == "" {
			errs = append(errs, "url is required")
		}
		if sd.Name == "" {
			errs = append(errs, "name is required")
		}
		if sd.Status == "" {
			errs = append(errs, "status is required")
		} else if !validStatuses[sd.Status] {
			errs = append(errs, "status must be one of: draft, active, retired, unknown")
		}
		if sd.Kind != "" && !validKinds[sd.Kind] {
			errs = append(errs, "kind must be one of: primitive-type, complex-type, resource, logical")
		}
		if sd.Type == "" {
			errs = append(errs, "type is required")
		}
		if !validDerivations[sd.Derivation] {
			errs = append(errs, "derivation must be one of: specialization, constraint (or empty)")
		}
	}

	return errs
}
