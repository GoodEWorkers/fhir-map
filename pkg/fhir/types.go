// Package fhir provides shared FHIR R5 data types used across the application.
package fhir

import "encoding/json"

// ResourceType constants for FHIR resources handled by this server.
const (
	ResourceTypeConceptMap = "ConceptMap"
	ResourceTypeParameters = "Parameters"
	ResourceTypeBundle     = "Bundle"
)

// Meta represents FHIR Resource.meta
type Meta struct {
	VersionID   string   `json:"versionId,omitempty"`
	LastUpdated string   `json:"lastUpdated,omitempty"`
	Profile     []string `json:"profile,omitempty"`
}

// Coding represents FHIR Coding data type.
type Coding struct {
	System       string `json:"system,omitempty"`
	Version      string `json:"version,omitempty"`
	Code         string `json:"code,omitempty"`
	Display      string `json:"display,omitempty"`
	UserSelected *bool  `json:"userSelected,omitempty"`
}

// CodeableConcept represents FHIR CodeableConcept data type.
type CodeableConcept struct {
	Coding []Coding `json:"coding,omitempty"`
	Text   string   `json:"text,omitempty"`
}

// Identifier represents FHIR Identifier data type.
type Identifier struct {
	Use    string           `json:"use,omitempty"`
	Type   *CodeableConcept `json:"type,omitempty"`
	System string           `json:"system,omitempty"`
	Value  string           `json:"value,omitempty"`
}

// ContactDetail represents FHIR ContactDetail data type.
type ContactDetail struct {
	Name    string         `json:"name,omitempty"`
	Telecom []ContactPoint `json:"telecom,omitempty"`
}

// ContactPoint represents FHIR ContactPoint data type.
type ContactPoint struct {
	System string `json:"system,omitempty"`
	Value  string `json:"value,omitempty"`
	Use    string `json:"use,omitempty"`
	Rank   *int   `json:"rank,omitempty"`
}

// UsageContext represents FHIR UsageContext data type.
type UsageContext struct {
	Code  Coding          `json:"code"`
	Value json.RawMessage `json:"valueCodeableConcept,omitempty"`
}

// Period represents FHIR Period data type.
type Period struct {
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

// Extension represents a FHIR Extension.
type Extension struct {
	URL       string `json:"url"`
	ValueCode string `json:"valueCode,omitempty"`
}

// Narrative represents FHIR Narrative (Resource.text)
type Narrative struct {
	Status string `json:"status,omitempty"`
	Div    string `json:"div,omitempty"`
}

// RelatedArtifact represents FHIR RelatedArtifact data type.
type RelatedArtifact struct {
	Type     string `json:"type,omitempty"`
	Resource string `json:"resource,omitempty"`
}
