package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// startupTime is captured once at package init so the CapabilityStatement
// `date` field is stable across requests — consumers can cache the metadata
// response by ETag/Date semantics without artificial invalidation.
var startupTime = time.Now().UTC().Format(time.RFC3339)

// fhirSpecVersion returns the canonical fhirVersion string we advertise for
// a given URL tree. Used by the CapabilityStatement; matches HAPI's strings.
func fhirSpecVersion(v fhir.FHIRVersion) string {
	if v == fhir.VersionR4 {
		return "4.0.1"
	}
	return "5.0.0"
}

// metadataHandler is the GET /metadata endpoint.
// Spec: https://hl7.org/fhir/R5/capabilitystatement.html
func (h *ConceptMapHandler) metadataHandler(w http.ResponseWriter, r *http.Request) {
	cs := h.buildCapabilityStatement()
	w.Header().Set("Content-Type", "application/fhir+json")
	_ = json.NewEncoder(w).Encode(cs)
}

func (h *ConceptMapHandler) buildCapabilityStatement() map[string]any {
	return map[string]any{
		"resourceType": "CapabilityStatement",
		"status":       "active",
		"date":         startupTime,
		"kind":         "instance",
		"software": map[string]any{
			"name":    "fhir-map",
			"version": "0.1",
		},
		"implementation": map[string]any{
			"description": "fhir-map ConceptMap server",
			"url":         h.baseURL + h.routePrefix(),
		},
		"fhirVersion": fhirSpecVersion(h.fhirVersion),
		// CapabilityStatement.format is required-bound to BCP-13 MIME types.
		// "json" is shorthand HAPI accepts but is not a valid MIME type, so the
		// HL7 Validator rejects it — only emit the canonical FHIR JSON type.
		"format": []string{"application/fhir+json"},
		"rest": []any{
			map[string]any{
				"mode": "server",
				"resource": []any{
					conceptMapResourceCapability(h.fhirVersion),
					structureMapResourceCapability(h.fhirVersion),
					structureDefinitionResourceCapability(h.fhirVersion),
				},
				"operation": systemLevelOperations(),
			},
		},
	}
}

// systemLevelOperations enumerates system-level operations ($transform is routed
// at system level for HAPI/Postman compat even though the FHIR spec defines it
// as a StructureMap-scoped operation; the HAPI-alias extension makes this explicit).
func systemLevelOperations() []any {
	return []any{
		map[string]any{
			"name":       "transform",
			"definition": "http://hl7.org/fhir/OperationDefinition/StructureMap-transform",
			"extension": []any{
				map[string]any{
					"url":          "https://goodeworkers.github.io/fhir-map/fhir/extensions/operation-hapi-alias",
					"valueBoolean": true,
				},
			},
		},
	}
}

// structureMapResourceCapability advertises the StructureMap resource.
// Each $transform entry carries an `extension` array enumerating parameter names
// the server accepts; R4 advertises only spec names (`source`, `content`),
// R5 advertises the full set. Long-standing HAPI synonyms aren't advertised
// — the goal is to tell spec-conformant clients what to send, not surface aliases.
func structureMapResourceCapability(version fhir.FHIRVersion) map[string]any {
	return map[string]any{
		"type": "StructureMap",
		"interaction": []any{
			map[string]any{"code": "create"},
			map[string]any{"code": "read"},
			map[string]any{"code": "update"},
			map[string]any{"code": "delete"},
			map[string]any{"code": "search-type"},
		},
		"versioning": "versioned-update",
		"searchParam": []any{
			searchParam("_id", "token", "Logical id"),
			searchParam("url", "uri", "Canonical URL"),
			searchParam("version", "token", "Business version"),
			searchParam("name", "string", "Computer-friendly name"),
			searchParam("title", "string", "Human-friendly title"),
			searchParam("status", "token", "draft | active | retired | unknown"),
			searchParam("publisher", "string", "Publisher name"),
			searchParam("description", "string", "Free-text description"),
		},
		"operation": []any{
			transformOperation(version),
		},
	}
}

// transformOperation builds the $transform operation entry with a
// per-version supported-parameter advertisement.
func transformOperation(version fhir.FHIRVersion) map[string]any {
	var params []string
	if version == fhir.VersionR4 {
		params = []string{"source", "content"}
	} else {
		params = []string{"source", "sourceMap", "srcMap", "supportingMap", "content"}
	}
	exts := make([]any, 0, len(params))
	for _, p := range params {
		exts = append(exts, map[string]any{
			"url":         "https://goodeworkers.github.io/fhir-map/fhir/extensions/transform-supported-parameter",
			"valueString": p,
		})
	}
	return map[string]any{
		"name":       "transform",
		"definition": "http://hl7.org/fhir/OperationDefinition/StructureMap-transform",
		"extension":  exts,
	}
}

// conceptMapResourceCapability lists the interactions, search params, and
// operations the server exposes for the ConceptMap resource.
func conceptMapResourceCapability(version fhir.FHIRVersion) map[string]any {
	_ = version
	return map[string]any{
		"type": "ConceptMap",
		"interaction": []any{
			map[string]any{"code": "create"},
			map[string]any{"code": "read"},
			map[string]any{"code": "update"},
			map[string]any{"code": "delete"},
			map[string]any{"code": "search-type"},
		},
		"versioning":      "versioned-update",
		"conditionalRead": "not-supported",
		"searchParam": []any{
			searchParam("_id", "token", "Logical id"),
			searchParam("url", "uri", "Canonical URL"),
			searchParam("version", "token", "Business version"),
			searchParam("name", "string", "Computer-friendly name"),
			searchParam("title", "string", "Human-friendly title"),
			searchParam("status", "token", "draft | active | retired | unknown"),
			searchParam("publisher", "string", "Publisher name"),
			searchParam("description", "string", "Free-text description"),
			searchParam("date", "date", "When changed"),
			searchParam("identifier", "token", "External identifier"),
			searchParam("source-code", "token", "Source-side codes present"),
			searchParam("target-code", "token", "Target-side codes present"),
			searchParam("source-group-system", "uri", "Source CodeSystem URL"),
			searchParam("target-group-system", "uri", "Target CodeSystem URL"),
			searchParam("source-scope", "uri", "Source canonical ValueSet"),
			searchParam("source-scope-uri", "uri", "Source ValueSet URI"),
			searchParam("target-scope", "uri", "Target canonical ValueSet"),
			searchParam("target-scope-uri", "uri", "Target ValueSet URI"),
		},
		"operation": []any{
			operation("translate", "http://hl7.org/fhir/OperationDefinition/ConceptMap-translate"),
			operation("translate-batch", "http://example.org/fhir/OperationDefinition/ConceptMap-translate-batch"),
		},
	}
}

// structureDefinitionResourceCapability advertises the StructureDefinition
// CRUD + History + Search surface.
func structureDefinitionResourceCapability(version fhir.FHIRVersion) map[string]any {
	_ = version
	return map[string]any{
		"type": "StructureDefinition",
		"interaction": []any{
			map[string]any{"code": "create"},
			map[string]any{"code": "read"},
			map[string]any{"code": "vread"},
			map[string]any{"code": "update"},
			map[string]any{"code": "delete"},
			map[string]any{"code": "search-type"},
			map[string]any{"code": "history-instance"},
			map[string]any{"code": "history-type"},
		},
		"versioning": "versioned-update",
		"searchParam": []any{
			searchParam("_id", "token", "Logical id"),
			searchParam("url", "uri", "Canonical URL"),
			searchParam("version", "token", "Business version"),
			searchParam("name", "string", "Computer-friendly name"),
			searchParam("status", "token", "draft | active | retired | unknown"),
			searchParam("kind", "token", "primitive-type | complex-type | resource | logical"),
			searchParam("type", "token", "FHIR base type name"),
		},
	}
}

func searchParam(name, typ, doc string) map[string]any {
	return map[string]any{"name": name, "type": typ, "documentation": doc}
}

func operation(name, definitionURL string) map[string]any {
	return map[string]any{"name": name, "definition": definitionURL}
}
