package transform

import "regexp"

// FHIR polymorphic "value[x]" elements are often written by a StructureMap under
// their bare base name (e.g. a rule targets `value` or `effective`), leaving the
// engine to serialize them with the concrete type suffix (`valueQuantity`,
// `effectiveDateTime`). A schema-aware engine reads that suffix from the
// StructureDefinition; lacking profiles, we infer it from the value's shape.
var choiceBaseElements = map[string]bool{
	"value": true, "effective": true, "onset": true, "abatement": true,
	"deceased": true, "multipleBirth": true, "occurrence": true, "performed": true,
}

var fhirDateTimeRe = regexp.MustCompile(`^\d{4}(-\d{2}(-\d{2}(T\d{2}:\d{2}(:\d{2})?([.+\-Z:0-9]*)?)?)?)?$`)

func normalizeChoiceTypes(v any) {
	switch t := v.(type) {
	case map[string]any:
		if _, isResource := t["resourceType"]; isResource {
			for base := range choiceBaseElements {
				val, ok := t[base]
				if !ok {
					continue
				}
				if suffix := fhirTypeSuffix(val); suffix != "" {
					t[base+suffix] = val
					delete(t, base)
				}
			}
		}
		for _, child := range t {
			normalizeChoiceTypes(child)
		}
	case []any:
		for _, child := range t {
			normalizeChoiceTypes(child)
		}
	}
}

func fhirTypeSuffix(v any) string {
	switch t := v.(type) {
	case map[string]any:
		switch {
		case hasKey(t, "coding") || hasKey(t, "text"):
			return "CodeableConcept"
		case hasKey(t, "system") && (hasKey(t, "value") || hasKey(t, "unit") || hasKey(t, "code")):
			return "Quantity"
		case hasKey(t, "reference"):
			return "Reference"
		case hasKey(t, "start") || hasKey(t, "end"):
			return "Period"
		case hasKey(t, "numerator"):
			return "Ratio"
		}
		return ""
	case string:
		if fhirDateTimeRe.MatchString(t) {
			return "DateTime"
		}
		return "String"
	case bool:
		return "Boolean"
	default:
		// Bare numbers are ambiguous (Integer vs Decimal vs Quantity); leave the
		// base name rather than guess.
		return ""
	}
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}
