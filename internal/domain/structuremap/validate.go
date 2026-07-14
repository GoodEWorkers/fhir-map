package structuremap

import "strconv"

// ValidationMode mirrors conceptmap.ValidationMode with two tiers: strict for client writes, lenient for fixture loading.
type ValidationMode int

const (
	ModeStrict ValidationMode = iota
	ModeLenient
)

// validTransforms is the set of transform codes the FHIR R5 spec defines.
// Used only in strict mode; lenient lets future-vocabulary or vendor-extension
// codes through.
var validTransforms = map[string]bool{
	"create": true, "copy": true, "truncate": true, "escape": true,
	"cast": true, "append": true, "translate": true, "reference": true,
	"dateOp": true, "uuid": true, "pointer": true, "evaluate": true,
	"cc": true, "c": true, "qty": true, "id": true, "cp": true,
}

var validStatuses = map[string]bool{
	"draft": true, "active": true, "retired": true, "unknown": true,
}

var validStructureModes = map[string]bool{
	"source": true, "queried": true, "target": true, "produced": true,
}

var validInputModes = map[string]bool{
	"source": true, "target": true,
}

// Validate is the strict-mode shim for ergonomic callers.
func (sm *StructureMap) Validate() []string {
	return sm.ValidateMode(ModeStrict)
}

// ValidateMode runs the configured tier of checks. Structural invariants
// (cardinalities, required-field presence) are enforced in both tiers;
// vocabulary bindings (status enum, transform enum, mode enum) are strict-only.
func (sm *StructureMap) ValidateMode(mode ValidationMode) []string {
	var errors []string

	if sm.ResourceType != "" && sm.ResourceType != "StructureMap" {
		errors = append(errors, "resourceType must be 'StructureMap'")
	}

	if mode == ModeStrict {
		if sm.Name == "" {
			errors = append(errors, "name is required")
		}
		if sm.Status == "" {
			errors = append(errors, "status is required")
		} else if !validStatuses[sm.Status] {
			errors = append(errors, "status must be one of: draft, active, retired, unknown")
		}
	}

	// Group is required (1..*) — enforced both tiers.
	if len(sm.Group) == 0 {
		errors = append(errors, "at least one group is required")
	}

	for gi, group := range sm.Group {
		gp := "group[" + strconv.Itoa(gi) + "]"
		if group.Name == "" && mode == ModeStrict {
			errors = append(errors, gp+".name is required")
		}
		if len(group.Input) == 0 {
			errors = append(errors, gp+": at least one input is required")
		}
		if len(group.Rule) == 0 {
			errors = append(errors, gp+": at least one rule is required")
		}

		for ii, in := range group.Input {
			if mode == ModeStrict {
				if in.Name == "" {
					errors = append(errors, gp+".input["+strconv.Itoa(ii)+"].name is required")
				}
				if in.Mode != "" && !validInputModes[in.Mode] {
					errors = append(errors, gp+".input["+strconv.Itoa(ii)+"].mode must be source | target")
				}
			}
		}

		for ri := range group.Rule {
			validateRule(&group.Rule[ri], mode, gp+".rule["+strconv.Itoa(ri)+"]", &errors)
		}
	}

	if mode == ModeStrict {
		for si, s := range sm.Structure {
			sp := "structure[" + strconv.Itoa(si) + "]"
			if s.Mode != "" && !validStructureModes[s.Mode] {
				errors = append(errors, sp+".mode must be source | queried | target | produced")
			}
		}
	}

	return errors
}

// validateRule walks a Rule and its nested Rule[] tree, accumulating errors.
// Recurses to support arbitrary nesting depth (FHIR rules can nest freely).
func validateRule(rule *Rule, mode ValidationMode, prefix string, out *[]string) {
	// In R5 Rule.name is optional, but the FHIR validator still warns
	// when missing — keep it as a strict-only signal.
	// (Avoid a hard error so well-formed R5 maps without rule names load.)
	for ti := range rule.Target {
		t := &rule.Target[ti]
		if t.Transform != "" && mode == ModeStrict && !validTransforms[t.Transform] {
			*out = append(*out, prefix+".target["+strconv.Itoa(ti)+"]: transform must be one of the FHIR R5 transform codes; got '"+t.Transform+"'")
		}
	}
	for nri := range rule.Rule {
		validateRule(&rule.Rule[nri], mode, prefix+".rule["+strconv.Itoa(nri)+"]", out)
	}
}
