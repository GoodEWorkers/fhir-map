// Package structuremap provides the domain model and persistence interface
// for FHIR StructureMap resources.
package structuremap

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// StructureMap is the FHIR R5 StructureMap resource (https://hl7.org/fhir/R5/structuremap.html).
type StructureMap struct {
	ResourceType string           `json:"resourceType"`
	ID           string           `json:"id,omitempty"`
	Meta         *fhir.Meta       `json:"meta,omitempty"`
	Text         *fhir.Narrative  `json:"text,omitempty"`
	Extension    []fhir.Extension `json:"extension,omitempty"`

	URL                    string                 `json:"url,omitempty"`
	Identifier             []fhir.Identifier      `json:"identifier,omitempty"`
	Version                string                 `json:"version,omitempty"`
	VersionAlgorithmString string                 `json:"versionAlgorithmString,omitempty"`
	VersionAlgorithmCoding *fhir.Coding           `json:"versionAlgorithmCoding,omitempty"`
	Name                   string                 `json:"name,omitempty"` // Required
	Title                  string                 `json:"title,omitempty"`
	Status                 string                 `json:"status,omitempty"` // Required: draft | active | retired | unknown
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

	Structure []Structure `json:"structure,omitempty"`
	Import    []string    `json:"import,omitempty"`
	Group     []Group     `json:"group,omitempty"` // Required: at least one

	// Contained holds inline ConceptMap resources referenceable from
	// `translate(..., '<url>', ...)` transform calls. Not serialised to wire form.
	Contained []*conceptmap.ConceptMap `json:"-"`

	// Const holds top-level `let name = expr;` constants parsed from FML; map-scoped.
	Const []Rule `json:"-"`

	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

// structureMapAlias avoids infinite recursion inside the custom (un)marshalers.
type structureMapAlias StructureMap

// UnmarshalJSON populates inline ConceptMap resources from the FHIR `contained[]` array
// into Contained so JSON round-tripping preserves them for translate() calls.
func (sm *StructureMap) UnmarshalJSON(data []byte) error {
	aux := struct {
		*structureMapAlias
		Contained []json.RawMessage `json:"contained,omitempty"`
	}{structureMapAlias: (*structureMapAlias)(sm)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	sm.Contained = nil
	for _, raw := range aux.Contained {
		var probe struct {
			ResourceType string `json:"resourceType"`
		}
		if json.Unmarshal(raw, &probe) != nil || probe.ResourceType != "ConceptMap" {
			continue // only ConceptMaps are modelled in Contained today
		}
		cm := &conceptmap.ConceptMap{}
		if err := json.Unmarshal(raw, cm); err == nil {
			sm.Contained = append(sm.Contained, cm)
		}
	}
	return nil
}

// MarshalJSON emits the standard fields plus any Contained ConceptMaps as the
// FHIR `contained[]` array; output is byte-identical to default marshal when empty.
func (sm StructureMap) MarshalJSON() ([]byte, error) {
	if len(sm.Contained) == 0 {
		return json.Marshal(structureMapAlias(sm))
	}
	aux := struct {
		structureMapAlias
		Contained []*conceptmap.ConceptMap `json:"contained,omitempty"`
	}{structureMapAlias: structureMapAlias(sm), Contained: sm.Contained}
	return json.Marshal(aux)
}

// GetID / SetID / GetMeta / SetMeta implement handler.Resource so StructureMap
// can be used with the generic ResourceHandler[T].
func (sm *StructureMap) GetID() string        { return sm.ID }
func (sm *StructureMap) SetID(id string)      { sm.ID = id }
func (sm *StructureMap) GetMeta() *fhir.Meta  { return sm.Meta }
func (sm *StructureMap) SetMeta(m *fhir.Meta) { sm.Meta = m }

// Structure refers to a StructureDefinition involved in the map. Mode tells
// the engine whether it's a source or target shape (or both).
type Structure struct {
	URL           string `json:"url,omitempty"`
	Mode          string `json:"mode,omitempty"` // source | queried | target | produced
	Alias         string `json:"alias,omitempty"`
	Documentation string `json:"documentation,omitempty"`
}

// Group is one named mapping group.
type Group struct {
	Name          string  `json:"name,omitempty"`
	Extends       string  `json:"extends,omitempty"`
	TypeMode      string  `json:"typeMode,omitempty"` // none | types | type-and-types
	Documentation string  `json:"documentation,omitempty"`
	Input         []Input `json:"input,omitempty"` // Required: at least one
	Rule          []Rule  `json:"rule,omitempty"`  // Required: at least one
}

// Input declares one named input slot on a group with its expected type and
// whether it's a source or target value.
type Input struct {
	Name          string `json:"name,omitempty"`
	Type          string `json:"type,omitempty"`
	Mode          string `json:"mode,omitempty"` // source | target
	Documentation string `json:"documentation,omitempty"`
}

// Rule is one rewrite step inside a group. Rules can nest (Rule.Rule) and
// can invoke other groups (Rule.Dependent).
type Rule struct {
	Name          string      `json:"name,omitempty"`
	Source        []Source    `json:"source,omitempty"`
	Target        []Target    `json:"target,omitempty"`
	Rule          []Rule      `json:"rule,omitempty"`
	Dependent     []Dependent `json:"dependent,omitempty"`
	Documentation string      `json:"documentation,omitempty"`
}

// Source binds part of the input value(s) to a local variable, optionally
// filtering and validating with FHIRPath. DefaultValue and DefaultValueType
// implement the FHIR value[x] choice-type convention (e.g., `defaultValueString`).
type Source struct {
	Context          string          `json:"context,omitempty"`
	Min              *int            `json:"min,omitempty"`
	Max              string          `json:"max,omitempty"`
	Type             string          `json:"type,omitempty"`
	DefaultValue     json.RawMessage `json:"-"`
	DefaultValueType string          `json:"-"`
	Element          string          `json:"element,omitempty"`
	ListMode         string          `json:"listMode,omitempty"` // first | not-first | last | not-last | only-one
	Variable         string          `json:"variable,omitempty"`
	Condition        string          `json:"condition,omitempty"` // FHIRPath
	Check            string          `json:"check,omitempty"`     // FHIRPath
	LogMessage       string          `json:"logMessage,omitempty"`
}

// sourceWire is a marshal/unmarshal helper for Source fields.
type sourceWire struct {
	Context    string `json:"context,omitempty"`
	Min        *int   `json:"min,omitempty"`
	Max        string `json:"max,omitempty"`
	Type       string `json:"type,omitempty"`
	Element    string `json:"element,omitempty"`
	ListMode   string `json:"listMode,omitempty"`
	Variable   string `json:"variable,omitempty"`
	Condition  string `json:"condition,omitempty"`
	Check      string `json:"check,omitempty"`
	LogMessage string `json:"logMessage,omitempty"`
}

// UnmarshalJSON handles the FHIR value[x] choice-type (e.g., defaultValueString).
func (s *Source) UnmarshalJSON(data []byte) error {
	var wire sourceWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	s.Context = wire.Context
	s.Min = wire.Min
	s.Max = wire.Max
	s.Type = wire.Type
	s.Element = wire.Element
	s.ListMode = wire.ListMode
	s.Variable = wire.Variable
	s.Condition = wire.Condition
	s.Check = wire.Check
	s.LogMessage = wire.LogMessage

	var bag map[string]json.RawMessage
	if err := json.Unmarshal(data, &bag); err != nil {
		return err
	}
	for key, val := range bag {
		if key == "defaultValue" {
			s.DefaultValue = val
			s.DefaultValueType = ""
			continue
		}
		if strings.HasPrefix(key, "defaultValue") && len(key) > len("defaultValue") {
			s.DefaultValue = val
			s.DefaultValueType = key[len("defaultValue"):]
		}
	}
	return nil
}

// MarshalJSON emits fields with the correct FHIR value[x] key for round-tripping.
func (s Source) MarshalJSON() ([]byte, error) {
	wire := sourceWire{
		Context: s.Context, Min: s.Min, Max: s.Max, Type: s.Type,
		Element: s.Element, ListMode: s.ListMode, Variable: s.Variable,
		Condition: s.Condition, Check: s.Check, LogMessage: s.LogMessage,
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	if len(s.DefaultValue) == 0 {
		return body, nil
	}
	// Splice the defaultValue key into the existing object body. body[0]
	// is `{`; body ends with `}`. Insert before the trailing `}`,
	// prefixing with `,` only when there are other fields already.
	key := "defaultValue"
	if s.DefaultValueType != "" {
		key += s.DefaultValueType
	}
	insert := `"` + key + `":` + string(s.DefaultValue)
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return body, nil
	}
	if len(body) == 2 {
		return []byte(`{` + insert + `}`), nil
	}
	return []byte(string(body[:len(body)-1]) + "," + insert + "}"), nil
}

// Target writes into part of the output value(s) using a transform vocabulary.
type Target struct {
	Context     string      `json:"context,omitempty"`
	ContextType string      `json:"contextType,omitempty"` // R4-only (type|variable); set by ProjectToR4. Required on R4 wire when Context is present (smp-2). Absent on R5 wire.
	Element     string      `json:"element,omitempty"`
	Variable    string      `json:"variable,omitempty"`
	ListMode    []string    `json:"listMode,omitempty"` // first | share | last | collate (note: list in R5)
	ListRuleId  string      `json:"listRuleId,omitempty"`
	Transform   string      `json:"transform,omitempty"` // create | copy | truncate | escape | cast | append | translate | reference | dateOp | uuid | pointer | evaluate | cc | c | qty | id | cp
	Parameter   []Parameter `json:"parameter,omitempty"`
}

// Parameter is one argument to a target.transform invocation. value[x] is
// modelled as the typed fields below — most transforms reference an existing
// variable by name via ValueID (FHIRPath identifier).
type Parameter struct {
	ValueID       string   `json:"valueId,omitempty"`
	ValueString   string   `json:"valueString,omitempty"`
	ValueBoolean  *bool    `json:"valueBoolean,omitempty"`
	ValueInteger  *int     `json:"valueInteger,omitempty"`
	ValueDecimal  *float64 `json:"valueDecimal,omitempty"`
	ValueDate     string   `json:"valueDate,omitempty"`
	ValueTime     string   `json:"valueTime,omitempty"`
	ValueDateTime string   `json:"valueDateTime,omitempty"`
}

// Dependent invokes another group by name with positional arguments.
// MapURL is persisted as `mapUrl` (non-standard FHIR key) so it round-trips through DB.
type Dependent struct {
	Name      string      `json:"name,omitempty"`
	Parameter []Parameter `json:"parameter,omitempty"`
	MapURL    string      `json:"mapUrl,omitempty"`
}
