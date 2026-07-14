package handler

import (
	"fmt"
	"strings"
)

// OutputIssue is a single PHI-safe finding from validating a $transform result.
// Detail must report structural facts only (missing resourceType, malformed
// shape) and never echo the resource's instance data.
type OutputIssue struct {
	Severity string // FHIR issue severity: "error" | "warning"
	Code     string // FHIR issue code: e.g. "structure"
	Detail   string // PHI-conservative diagnostic
}

// OutputValidator validates a $transform result before it is returned to the
// caller. The in-process structural validator below is v1; an official/remote FHIR validator
// can be dropped in behind this interface without touching the handler.
//
// Implementations MUST keep OutputIssue.Detail PHI-conservative.
type OutputValidator interface {
	ValidateOutput(result any) []OutputIssue
}

// structuralOutputValidator confirms the result is a well-formed FHIR resource
// (a JSON object with non-empty resourceType) and that Bundle entries do too.
// HL7v2-shaped output (ER7) is exempt: it is not FHIR.
type structuralOutputValidator struct{}

// NewStructuralOutputValidator returns the in-process structural OutputValidator.
func NewStructuralOutputValidator() OutputValidator { return structuralOutputValidator{} }

func (structuralOutputValidator) ValidateOutput(result any) []OutputIssue {
	m, ok := result.(map[string]any)
	if !ok {
		return []OutputIssue{{
			Severity: "error", Code: "structure",
			Detail: "transform output is not a FHIR resource object",
		}}
	}
	if isHL7v2Shape(m) {
		return nil // ER7 target — not FHIR; structural FHIR checks do not apply
	}
	if rt, _ := m["resourceType"].(string); rt == "" {
		// Can't say anything more useful without a type.
		return []OutputIssue{{
			Severity: "error", Code: "structure",
			Detail: "transform output is missing a resourceType",
		}}
	} else if rt == "Bundle" {
		return validateBundleEntries(m)
	}
	return nil
}

// validateBundleEntries checks that each Bundle entry with an inline resource
// has a non-empty resourceType (per FHIR §6.3).
func validateBundleEntries(bundle map[string]any) []OutputIssue {
	entries, ok := bundle["entry"].([]any)
	if !ok {
		return nil
	}
	var issues []OutputIssue
	for i, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		res, ok := em["resource"]
		if !ok {
			continue
		}
		rm, ok := res.(map[string]any)
		if !ok {
			issues = append(issues, OutputIssue{
				Severity: "error", Code: "structure",
				Detail: fmt.Sprintf("Bundle entry[%d] resource is not an object", i),
			})
			continue
		}
		if rt, _ := rm["resourceType"].(string); rt == "" {
			issues = append(issues, OutputIssue{
				Severity: "error", Code: "structure",
				Detail: fmt.Sprintf("Bundle entry[%d] resource is missing a resourceType", i),
			})
		}
	}
	return issues
}

// aggregateOutputIssues joins a non-empty issue list into a single diagnostic.
func aggregateOutputIssues(issues []OutputIssue) (code, detail string) {
	if len(issues) == 0 {
		return "", ""
	}
	parts := make([]string, 0, len(issues))
	for _, is := range issues {
		parts = append(parts, is.Detail)
	}
	return issues[0].Code, strings.Join(parts, "; ")
}
