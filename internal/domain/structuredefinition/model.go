// Package structuredefinition provides the domain model and persistence interface
// for FHIR StructureDefinition resources. It is intentionally minimal for v1:
// the resolver only needs URL, Type, BaseDefinition, and Kind to walk the
// baseDefinition chain. Differential/Snapshot round-trip opaquely.
package structuredefinition

import (
	"encoding/json"
	"time"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// StructureDefinition is the FHIR R5 StructureDefinition resource.
// See: https://hl7.org/fhir/R5/structuredefinition.html
type StructureDefinition struct {
	ResourceType string     `json:"resourceType"`
	ID           string     `json:"id,omitempty"`
	Meta         *fhir.Meta `json:"meta,omitempty"`

	URL         string `json:"url,omitempty"` // Required
	Version     string `json:"version,omitempty"`
	Name        string `json:"name,omitempty"` // Required
	Title       string `json:"title,omitempty"`
	Status      string `json:"status,omitempty"` // Required: draft | active | retired | unknown
	Date        string `json:"date,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	Description string `json:"description,omitempty"`

	// Kind is the StructureDefinition category (primitive-type | complex-type | resource | logical).
	Kind string `json:"kind,omitempty"`

	Abstract *bool `json:"abstract,omitempty"`

	// Type is the FHIR base type name (e.g. "Patient", "string") the resolver returns.
	Type string `json:"type,omitempty"`

	// BaseDefinition is the canonical URL of the parent StructureDefinition; resolver walks this chain to reach a concrete base type.
	BaseDefinition string `json:"baseDefinition,omitempty"`

	// Derivation is specialization | constraint (empty allowed for HL7 base types).
	Derivation string `json:"derivation,omitempty"`

	// Differential and Snapshot are opaque JSON blobs; v1 doesn't introspect element definitions.
	Differential json.RawMessage `json:"differential,omitempty"`
	Snapshot     json.RawMessage `json:"snapshot,omitempty"`

	// Internal timestamps (not serialised to the FHIR wire).
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

// GetID implements handler.Resource.
func (sd *StructureDefinition) GetID() string        { return sd.ID }
func (sd *StructureDefinition) SetID(id string)      { sd.ID = id }
func (sd *StructureDefinition) GetMeta() *fhir.Meta  { return sd.Meta }
func (sd *StructureDefinition) SetMeta(m *fhir.Meta) { sd.Meta = m }
