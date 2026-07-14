// Package conceptmap provides the domain model and business logic for FHIR R5 ConceptMap resources.
package conceptmap

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// ConceptMap represents a FHIR R5 ConceptMap resource.
// See: https://hl7.org/fhir/R5/conceptmap.html
type ConceptMap struct {
	ResourceType string           `json:"resourceType"`
	ID           string           `json:"id,omitempty"`
	Meta         *fhir.Meta       `json:"meta,omitempty"`
	Text         *fhir.Narrative  `json:"text,omitempty"`
	Extension    []fhir.Extension `json:"extension,omitempty"`

	// Canonical metadata
	URL                    string                 `json:"url,omitempty"`
	Identifier             []fhir.Identifier      `json:"identifier,omitempty"`
	Version                string                 `json:"version,omitempty"`
	VersionAlgorithmString string                 `json:"versionAlgorithmString,omitempty"`
	VersionAlgorithmCoding *fhir.Coding           `json:"versionAlgorithmCoding,omitempty"`
	Name                   string                 `json:"name,omitempty"`
	Title                  string                 `json:"title,omitempty"`
	Status                 string                 `json:"status"` // Required: draft | active | retired | unknown
	Experimental           *bool                  `json:"experimental,omitempty"`
	Date                   string                 `json:"date,omitempty"`
	Publisher              string                 `json:"publisher,omitempty"`
	Contact                []fhir.ContactDetail   `json:"contact,omitempty"`
	Description            string                 `json:"description,omitempty"`
	UseContext             []fhir.UsageContext    `json:"useContext,omitempty"`
	Jurisdiction           []fhir.CodeableConcept `json:"jurisdiction,omitempty"`
	Purpose                string                 `json:"purpose,omitempty"`
	Copyright              string                 `json:"copyright,omitempty"`
	CopyrightLabel         string                 `json:"copyrightLabel,omitempty"`
	ApprovalDate           string                 `json:"approvalDate,omitempty"`
	LastReviewDate         string                 `json:"lastReviewDate,omitempty"`
	EffectivePeriod        *fhir.Period           `json:"effectivePeriod,omitempty"`
	Topic                  []fhir.CodeableConcept `json:"topic,omitempty"`
	Author                 []fhir.ContactDetail   `json:"author,omitempty"`
	Editor                 []fhir.ContactDetail   `json:"editor,omitempty"`
	Reviewer               []fhir.ContactDetail   `json:"reviewer,omitempty"`
	Endorser               []fhir.ContactDetail   `json:"endorser,omitempty"`
	RelatedArtifact        []fhir.RelatedArtifact `json:"relatedArtifact,omitempty"`

	// ConceptMap-specific content
	Property             []Property            `json:"property,omitempty"`
	AdditionalAttribute  []AdditionalAttribute `json:"additionalAttribute,omitempty"`
	SourceScopeURI       string                `json:"sourceScopeUri,omitempty"`
	SourceScopeCanonical string                `json:"sourceScopeCanonical,omitempty"`
	TargetScopeURI       string                `json:"targetScopeUri,omitempty"`
	TargetScopeCanonical string                `json:"targetScopeCanonical,omitempty"`
	Group                []Group               `json:"group,omitempty"`

	// Internal fields (not serialized to JSON)
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

// Property defines a slot for additional information about a mapping.
type Property struct {
	Code        string `json:"code"`
	URI         string `json:"uri,omitempty"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"` // Coding | string | integer | boolean | dateTime | decimal | code
	System      string `json:"system,omitempty"`
}

// AdditionalAttribute defines additional data elements for mappings.
type AdditionalAttribute struct {
	Code        string `json:"code"`
	URI         string `json:"uri,omitempty"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"` // code | Coding | string | boolean | Quantity
}

// Group represents a set of mappings sharing the same source and target systems.
type Group struct {
	Source        string    `json:"source,omitempty"`
	SourceVersion string    `json:"sourceVersion,omitempty"`
	Target        string    `json:"target,omitempty"`
	TargetVersion string    `json:"targetVersion,omitempty"`
	Element       []Element `json:"element"`
	Unmapped      *Unmapped `json:"unmapped,omitempty"`
}

// Element represents a mapping for a single source concept.
type Element struct {
	Code     string   `json:"code,omitempty"`
	Display  string   `json:"display,omitempty"`
	ValueSet string   `json:"valueSet,omitempty"`
	NoMap    *bool    `json:"noMap,omitempty"`
	Comment  string   `json:"comment,omitempty"`
	Target   []Target `json:"target,omitempty"`
}

// Target represents a concept in the target system that a source concept maps to.
//
// Relationship is the canonical R5 vocabulary the engine reasons over.
// Equivalence is the R4-only wire-format alias accepted on input and emitted
// by the R4 URL tree on output. It is never persisted — see ProjectToR4 /
// CanonicaliseFromR4 below.
type Target struct {
	Code         string           `json:"code,omitempty"`
	Display      string           `json:"display,omitempty"`
	ValueSet     string           `json:"valueSet,omitempty"`
	Relationship string           `json:"relationship,omitempty"` // R5: related-to | equivalent | source-is-narrower-than-target | source-is-broader-than-target | not-related-to
	Equivalence  string           `json:"equivalence,omitempty"`  // R4 wire-only alias; canonicalised to Relationship on ingress
	Comment      string           `json:"comment,omitempty"`
	Property     []TargetProperty `json:"property,omitempty"`
	DependsOn    []DependsOn      `json:"dependsOn,omitempty"`
	Product      []DependsOn      `json:"product,omitempty"`
}

// TargetProperty provides additional information about a source->target mapping.
type TargetProperty struct {
	Code  string          `json:"code"`
	Value json.RawMessage `json:"value,omitempty"` // polymorphic: valueCoding, valueString, valueInteger, etc.
	// Typed value accessors set during deserialization
	ValueCoding  *fhir.Coding `json:"valueCoding,omitempty"`
	ValueString  string       `json:"valueString,omitempty"`
	ValueInteger *int         `json:"valueInteger,omitempty"`
	ValueBoolean *bool        `json:"valueBoolean,omitempty"`
	ValueCode    string       `json:"valueCode,omitempty"`
}

// DependsOn represents a dependency or product for a target mapping.
type DependsOn struct {
	Attribute   string          `json:"attribute"`
	Value       json.RawMessage `json:"value,omitempty"`
	ValueCode   string          `json:"valueCode,omitempty"`
	ValueCoding *fhir.Coding    `json:"valueCoding,omitempty"`
	ValueString string          `json:"valueString,omitempty"`
}

// Unmapped defines what happens when a source concept has no mapping.
type Unmapped struct {
	Mode         string `json:"mode"` // use-source-code | fixed | other-map
	Code         string `json:"code,omitempty"`
	Display      string `json:"display,omitempty"`
	Relationship string `json:"relationship,omitempty"`
	OtherMap     string `json:"otherMap,omitempty"`
}

// ValidationMode selects which checks Validate runs.
//
// The two-tier validator allows official FHIR fixtures (some of which violate
// their own spec) to load via lenient mode while user-submitted writes get
// the strict check.
type ValidationMode int

const (
	// ModeStrict runs every check — structural invariants plus full
	// vocabulary/binding checks (status, relationship, unmapped.mode).
	// This is the default for HTTP create/update.
	ModeStrict ValidationMode = iota

	// ModeLenient runs structural-only checks: required cardinalities and
	// mutually-exclusive fields. Skips status enumeration, relationship
	// vocabulary check, and unmapped.mode enumeration so a fixture that
	// uses an R4 vocabulary (`subsumes`, `wider`, ...) still loads.
	ModeLenient
)

// GetID / SetID / GetMeta / SetMeta implement handler.Resource so ConceptMap
// can be used with the generic ResourceHandler[T].
func (cm *ConceptMap) GetID() string        { return cm.ID }
func (cm *ConceptMap) SetID(id string)      { cm.ID = id }
func (cm *ConceptMap) GetMeta() *fhir.Meta  { return cm.Meta }
func (cm *ConceptMap) SetMeta(m *fhir.Meta) { cm.Meta = m }

// Validate runs the strict-mode check and returns the list of errors
// (empty if valid). Kept as the no-arg shim so existing callers stay correct.
func (cm *ConceptMap) Validate() []string {
	return cm.ValidateMode(ModeStrict)
}

// ValidateMode runs the configured tier of checks. See ValidationMode docs
// for what each tier does and does not enforce.
func (cm *ConceptMap) ValidateMode(mode ValidationMode) []string {
	var errors []string

	if cm.ResourceType != "" && cm.ResourceType != "ConceptMap" {
		errors = append(errors, "resourceType must be 'ConceptMap'")
	}

	if mode == ModeStrict {
		if cm.Status == "" {
			errors = append(errors, "status is required")
		} else {
			validStatuses := map[string]bool{
				"draft": true, "active": true, "retired": true, "unknown": true,
			}
			if !validStatuses[cm.Status] {
				errors = append(errors, "status must be one of: draft, active, retired, unknown")
			}
		}
	}

	for i, group := range cm.Group {
		if len(group.Element) == 0 {
			errors = append(errors, "group["+itoa(i)+"].element is required and must not be empty")
		}
		for j, elem := range group.Element {
			if elem.Code == "" && elem.ValueSet == "" {
				errors = append(errors, "group["+itoa(i)+"].element["+itoa(j)+"]: either code or valueSet must be present")
			}
			if elem.Code != "" && elem.ValueSet != "" {
				errors = append(errors, "group["+itoa(i)+"].element["+itoa(j)+"]: code and valueSet are mutually exclusive")
			}
			for k := range elem.Target {
				target := &elem.Target[k]
				// Even lenient mode requires *some* qualifier (either R5
				// relationship or R4 equivalence) on a target — a target row
				// with neither cannot be canonicalised.
				if target.Relationship == "" && target.Equivalence == "" {
					errors = append(errors, "group["+itoa(i)+"].element["+itoa(j)+"].target["+itoa(k)+"]: relationship is required")
					continue
				}
				if mode == ModeStrict && target.Relationship != "" {
					validRelationships := map[string]bool{
						"related-to": true, "equivalent": true,
						"source-is-narrower-than-target": true,
						"source-is-broader-than-target":  true,
						"not-related-to":                 true,
					}
					if !validRelationships[target.Relationship] {
						errors = append(errors, "group["+itoa(i)+"].element["+itoa(j)+"].target["+itoa(k)+"]: invalid relationship")
					}
				}
			}
		}
		if group.Unmapped != nil && mode == ModeStrict {
			validModes := map[string]bool{
				"use-source-code": true, "fixed": true, "other-map": true,
			}
			if !validModes[group.Unmapped.Mode] {
				errors = append(errors, "group["+itoa(i)+"].unmapped.mode must be one of: use-source-code, fixed, other-map")
			}
		}
	}

	return errors
}

// itoa formats an array index for human-readable validation error messages.
// Why: the previous one-liner only worked for single-digit values; itoa(10)
// produced ":", itoa(11) ";" etc. — see TestValidate_GroupIndexLabel_BeyondNine.
func itoa(i int) string {
	return strconv.Itoa(i)
}
