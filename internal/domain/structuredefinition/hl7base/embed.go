// Package hl7base embeds the bundled HL7 R5 base StructureDefinition fixture set.
// The fixture lives only in memory — it is NEVER written to the database.
// Operators register their own profiles via FHIR REST.
package hl7base

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/goodeworkers/fhir-map/internal/domain/structuredefinition"
)

//go:embed hl7-r5-base.json
var hl7BaseBytes []byte

// BaseTypes is the parsed in-memory index of HL7 R5 base StructureDefinitions,
// keyed by canonical URL. Populated at package init from the embedded JSON.
var BaseTypes map[string]*structuredefinition.StructureDefinition

func init() {
	var bundle struct {
		Entry []struct {
			Resource *structuredefinition.StructureDefinition `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(hl7BaseBytes, &bundle); err != nil {
		panic(fmt.Sprintf("hl7base: failed to parse embedded fixture: %v", err))
	}
	BaseTypes = make(map[string]*structuredefinition.StructureDefinition, len(bundle.Entry))
	for _, e := range bundle.Entry {
		if e.Resource != nil && e.Resource.URL != "" {
			cp := *e.Resource
			BaseTypes[e.Resource.URL] = &cp
		}
	}
}
