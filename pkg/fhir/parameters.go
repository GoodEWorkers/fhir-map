package fhir

// Parameters represents a FHIR Parameters resource (used for $translate I/O).
type Parameters struct {
	ResourceType string      `json:"resourceType"`
	Parameter    []Parameter `json:"parameter,omitempty"`
}

// Parameter represents a single parameter in a Parameters resource.
type Parameter struct {
	Name                 string           `json:"name"`
	ValueString          string           `json:"valueString,omitempty"`
	ValueBoolean         *bool            `json:"valueBoolean,omitempty"`
	ValueCode            string           `json:"valueCode,omitempty"`
	ValueURI             string           `json:"valueUri,omitempty"`
	ValueCoding          *Coding          `json:"valueCoding,omitempty"`
	ValueCodeableConcept *CodeableConcept `json:"valueCodeableConcept,omitempty"`
	Part                 []Parameter      `json:"part,omitempty"`
	// Resource carries an embedded FHIR resource for parameters that
	// transport one (e.g. an OperationOutcome attached to an `issue`
	// parameter, B3 audit fix 2026-05-24).
	Resource any `json:"resource,omitempty"`
}

// TranslateIssue is the wire-shape of a non-fatal advisory emitted by
// $translate (e.g. a `dependency.attribute` that named no declared
// target attribute). Each TranslateIssue surfaces as one `issue`
// parameter at the top of the response Parameters resource, carrying
// an embedded OperationOutcome with a single issue entry.
type TranslateIssue struct {
	Severity    string
	Code        string
	Diagnostics string
}

// NewTranslateBatchResponse builds the wire-shape of $translate-batch: one
// outer "result" boolean plus one repeating "translate" parameter per input
// probe (in input order). Each translate part carries a nested "result" and
// zero-or-more nested "match" parameters with the same shape as $translate.
//
// This op is non-standard (HAPI has no equivalent) and lives at
// /fhir/{R4|R5}/ConceptMap/$translate-batch. Custom but documented in the
// CapabilityStatement (M4).
// BatchPerProbe is the per-probe shape; exported for handler use.
type BatchPerProbe struct {
	Code    string
	System  string
	Result  bool
	Msg     string
	Matches []TranslateMatch
	// IsError distinguishes an engine-level failure (e.g. depth-cap exceeded,
	// store error) from a legitimate "no mapping found" outcome. When true,
	// the `unmapped` echo part must NOT be emitted — the probe did not
	// produce a mapping because the lookup failed, not because no mapping exists.
	IsError bool
}

// NewTranslateBatchResponseFor assembles the per-probe translate results into
// a single Parameters resource. The qualifier part name (relationship vs
// equivalence) depends on fhirVersion, matching $translate's behaviour.
func NewTranslateBatchResponseFor(overall bool, perProbe []BatchPerProbe, fhirVersion FHIRVersion) Parameters {
	params := Parameters{
		ResourceType: ResourceTypeParameters,
		Parameter: []Parameter{
			{Name: "result", ValueBoolean: &overall},
		},
	}
	for _, p := range perProbe {
		t := Parameter{Name: "translate"}
		// Echo input for client-side correlation.
		input := Parameter{Name: "input", Part: []Parameter{}}
		if p.Code != "" {
			input.Part = append(input.Part, Parameter{Name: "code", ValueCode: p.Code})
		}
		if p.System != "" {
			input.Part = append(input.Part, Parameter{Name: "system", ValueURI: p.System})
		}
		t.Part = append(t.Part, input)
		res := p.Result
		t.Part = append(t.Part, Parameter{Name: "result", ValueBoolean: &res})
		if p.Msg != "" {
			t.Part = append(t.Part, Parameter{Name: "message", ValueString: p.Msg})
		}
		// When a probe has no match (result=false), emit an `unmapped` part
		// echoing the input code/system so clients can correlate without
		// re-parsing the input array. This mirrors the FHIR R5 ConceptMap
		// BatchTranslate extension documented in the CapabilityStatement.
		// Skip when IsError=true: the probe failed due to an engine error
		// (e.g. depth-cap, store failure), not because no mapping exists.
		// Emitting `unmapped` in that case would mislead clients into treating
		// a lookup failure as a confirmed negative mapping result.
		if !p.Result && !p.IsError && p.Code != "" {
			unmappedPart := Parameter{Name: "unmapped", Part: []Parameter{}}
			unmappedPart.Part = append(unmappedPart.Part, Parameter{Name: "code", ValueCode: p.Code})
			if p.System != "" {
				unmappedPart.Part = append(unmappedPart.Part, Parameter{Name: "system", ValueURI: p.System})
			}
			t.Part = append(t.Part, unmappedPart)
		}
		for _, match := range p.Matches {
			matchParam := Parameter{Name: "match"}
			if match.Relationship != "" {
				partName, partValue := qualifierPart(match.Relationship, fhirVersion)
				matchParam.Part = append(matchParam.Part, Parameter{Name: partName, ValueCode: partValue})
			}
			if match.Concept != nil {
				matchParam.Part = append(matchParam.Part, Parameter{Name: "concept", ValueCoding: match.Concept})
			}
			if match.OriginMap != "" {
				matchParam.Part = append(matchParam.Part, Parameter{Name: "originMap", ValueURI: match.OriginMap})
			}
			for _, dep := range match.DependsOn {
				matchParam.Part = append(matchParam.Part, Parameter{
					Name: "dependsOn",
					Part: []Parameter{
						{Name: "attribute", ValueURI: dep.Attribute},
						valuePartFromDependency(dep),
					},
				})
			}
			t.Part = append(t.Part, matchParam)
		}
		params.Parameter = append(params.Parameter, t)
	}
	return params
}

// FHIRVersion identifies which wire vocabulary a serialisation boundary
// (mostly $translate, $transform, and the CapabilityStatement) should
// speak. The engine and storage are always canonical R5; this enum picks
// the per-tree projection at the wire boundary.
//
// Numeric values are STABLE and tested in version_test.go — reorder at
// your peril.
type FHIRVersion int

const (
	// VersionR5 (5.0.0) is the canonical internal version. The unprefixed
	// /fhir tree aliases R5 for back-compat with the pre-M2b Bruno suite.
	VersionR5 FHIRVersion = iota
	// VersionR4 (4.0.1) — emits the older `equivalence` vocabulary on
	// ConceptMap $translate responses and strips R5-only fields
	// (versionAlgorithm, copyrightLabel, const) from outgoing resources.
	VersionR4
)

// String returns the canonical FHIR fhirVersion identifier. Used by the
// CapabilityStatement at /fhir/{prefix}/metadata.
func (v FHIRVersion) String() string {
	switch v {
	case VersionR4:
		return "4.0.1"
	case VersionR5:
		return "5.0.0"
	}
	return "unknown"
}

// URLPrefix returns the URL-path segment under /fhir/ for this version.
// VersionR5 returns "" because the unprefixed /fhir tree is the R5 alias.
// Used by handlers to compute Location headers and route prefixes.
func (v FHIRVersion) URLPrefix() string {
	if v == VersionR4 {
		return "R4"
	}
	// VersionR5 (and any unknown value) falls through to the unprefixed
	// alias — that's the canonical default and matches the M2b routing.
	return ""
}

// NewTranslateResponse creates a Parameters resource for the $translate response,
// defaulting to R5 vocabulary. Use NewTranslateResponseFor when the caller knows
// which FHIR wire version it is serving.
func NewTranslateResponse(result bool, message string, matches []TranslateMatch) Parameters {
	return NewTranslateResponseFor(result, message, matches, VersionR5)
}

// NewTranslateResponseFor builds the Parameters resource using the
// part name dictated by fhirVersion. The match-qualifier part is named
// "relationship" for R5 and "equivalence" for R4; the code value is translated
// in M2a's vocab tables for R4.
func NewTranslateResponseFor(result bool, message string, matches []TranslateMatch, fhirVersion FHIRVersion, issues ...TranslateIssue) Parameters {
	params := Parameters{
		ResourceType: ResourceTypeParameters,
		Parameter: []Parameter{
			{Name: "result", ValueBoolean: &result},
		},
	}

	// Surface engine-level advisories before the matches so clients see them
	// alongside `result`/`message` rather than buried under match payloads.
	// Each issue becomes one `issue` parameter carrying a one-issue
	// OperationOutcome via the `resource` field (B3, 2026-05-24 audit).
	for _, iss := range issues {
		severity := iss.Severity
		if severity == "" {
			severity = "warning"
		}
		params.Parameter = append(params.Parameter, Parameter{
			Name: "issue",
			Resource: map[string]any{
				"resourceType": "OperationOutcome",
				"issue": []map[string]any{{
					"severity":    severity,
					"code":        iss.Code,
					"diagnostics": iss.Diagnostics,
				}},
			},
		})
	}

	if message != "" {
		params.Parameter = append(params.Parameter, Parameter{
			Name:        "message",
			ValueString: message,
		})
	}

	for _, match := range matches {
		matchParam := Parameter{
			Name: "match",
			Part: []Parameter{},
		}
		if match.Relationship != "" {
			partName, partValue := qualifierPart(match.Relationship, fhirVersion)
			matchParam.Part = append(matchParam.Part, Parameter{
				Name:      partName,
				ValueCode: partValue,
			})
		}
		if match.Concept != nil {
			matchParam.Part = append(matchParam.Part, Parameter{
				Name:        "concept",
				ValueCoding: match.Concept,
			})
		}
		if match.OriginMap != "" {
			matchParam.Part = append(matchParam.Part, Parameter{
				Name:     "originMap",
				ValueURI: match.OriginMap,
			})
		}
		for _, dep := range match.DependsOn {
			matchParam.Part = append(matchParam.Part, Parameter{
				Name: "dependsOn",
				Part: []Parameter{
					{Name: "attribute", ValueURI: dep.Attribute},
					valuePartFromDependency(dep),
				},
			})
		}
		for _, prod := range match.Product {
			matchParam.Part = append(matchParam.Part, Parameter{
				Name: "product",
				Part: []Parameter{
					{Name: "attribute", ValueURI: prod.Attribute},
					valuePartFromDependency(prod),
				},
			})
		}
		params.Parameter = append(params.Parameter, matchParam)
	}

	return params
}

// qualifierPart returns the (partName, valueCode) used to advertise the
// relationship between source and target concepts in a $translate match. R5
// uses `relationship` with the new vocabulary; R4 uses `equivalence` with the
// old vocabulary translated via the vocab table.
func qualifierPart(relationship string, fhirVersion FHIRVersion) (partName, valueCode string) {
	if fhirVersion == VersionR4 {
		return "equivalence", relationshipToEquivalenceR4Wire(relationship)
	}
	return "relationship", relationship
}

// relationshipToEquivalenceR4Wire is a tiny lookup local to this package so
// the parameters builder does not need to import the conceptmap domain package
// (which already imports this one).
func relationshipToEquivalenceR4Wire(relationship string) string {
	switch relationship {
	case "equivalent":
		return "equivalent"
	case "source-is-broader-than-target":
		return "wider"
	case "source-is-narrower-than-target":
		return "narrower"
	case "related-to":
		return "relatedto"
	case "not-related-to":
		return "unmatched"
	default:
		return relationship
	}
}

// valuePartFromDependency picks the right value[x] FHIR shape for a dependsOn/product
// part, mirroring the choice type on ConceptMap.group.element.target.dependsOn.value.
func valuePartFromDependency(dep TranslateMatchDependency) Parameter {
	p := Parameter{Name: "value"}
	switch {
	case dep.ValueCoding != nil:
		p.ValueCoding = dep.ValueCoding
	case dep.ValueCode != "":
		p.ValueCode = dep.ValueCode
	default:
		p.ValueString = dep.ValueString
	}
	return p
}

// TranslateMatch represents a single match result from a $translate operation.
type TranslateMatch struct {
	Relationship string
	Concept      *Coding
	OriginMap    string
	DependsOn    []TranslateMatchDependency
	Product      []TranslateMatchDependency
}

// TranslateMatchDependency represents a dependsOn or product entry in a translate match.
// Exactly one of ValueString, ValueCode, or ValueCoding is meaningful, matching
// the FHIR value[x] choice on ConceptMap.group.element.target.dependsOn.value.
type TranslateMatchDependency struct {
	Attribute   string
	ValueString string
	ValueCode   string
	ValueCoding *Coding
}
