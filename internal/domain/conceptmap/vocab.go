package conceptmap

// FHIR R4 used a `equivalence` element on ConceptMap.group.element.target with
// a richer (and somewhat overlapping) vocabulary. FHIR R5 replaced it with
// `relationship` whose vocabulary is smaller and clearer. Internally we keep
// everything in the R5 form; the helpers below convert at the wire boundary.
//
// The mapping is not symmetric: some R4 codes (`equal`, `subsumes`,
// `specializes`, `inexact`, `disjoint`) collapse onto a more general R5 code.
// `Lossy=true` flags those cases so callers that need round-trip fidelity for
// an R4 client can preserve the original code via an extension.
//
// References:
//   https://hl7.org/fhir/R4/valueset-concept-map-equivalence.html
//   https://hl7.org/fhir/R5/valueset-concept-map-relationship.html

const (
	RelEquivalent   = "equivalent"
	RelRelatedTo    = "related-to"
	RelBroaderThan  = "source-is-broader-than-target"
	RelNarrowerThan = "source-is-narrower-than-target"
	RelNotRelatedTo = "not-related-to"
)

const (
	EqEquivalent  = "equivalent"
	EqEqual       = "equal"
	EqRelatedTo   = "relatedto"
	EqWider       = "wider"
	EqSubsumes    = "subsumes"
	EqNarrower    = "narrower"
	EqSpecializes = "specializes"
	EqInexact     = "inexact"
	EqUnmatched   = "unmatched"
	EqDisjoint    = "disjoint"
)

// equivalenceToRelationship maps R4 codes to R5 relationships; lossy=true when the R4 code is finer-grained.
var equivalenceToRelationship = map[string]struct {
	rel   string
	lossy bool
}{
	EqEquivalent:  {RelEquivalent, false},
	EqEqual:       {RelEquivalent, true},
	EqWider:       {RelBroaderThan, false},
	EqSubsumes:    {RelBroaderThan, true},
	EqNarrower:    {RelNarrowerThan, false},
	EqSpecializes: {RelNarrowerThan, true},
	EqInexact:     {RelRelatedTo, true},
	EqRelatedTo:   {RelRelatedTo, false},
	EqUnmatched:   {RelNotRelatedTo, false},
	EqDisjoint:    {RelNotRelatedTo, true},
}

// relationshipToEquivalence maps R5 relationships to primary R4 codes for lossless round-tripping.
var relationshipToEquivalence = map[string]string{
	RelEquivalent:   EqEquivalent,
	RelBroaderThan:  EqWider,
	RelNarrowerThan: EqNarrower,
	RelRelatedTo:    EqRelatedTo,
	RelNotRelatedTo: EqUnmatched,
}

// RelationshipFromEquivalence converts an R4 `equivalence` code to its R5
// `relationship` projection. The bool reports whether the projection lost
// information (a finer-grained R4 code mapped to a coarser R5 one).
//
// Unknown inputs return ("", false) so callers can fall back to whatever
// quarantine behaviour they prefer (e.g. emit an OperationOutcome warning).
func RelationshipFromEquivalence(equivalence string) (string, bool) {
	if r, ok := equivalenceToRelationship[equivalence]; ok {
		return r.rel, r.lossy
	}
	return "", false
}

// EquivalenceFromRelationship converts an R5 `relationship` code to its
// canonical R4 `equivalence` spelling. Unknown inputs return "".
func EquivalenceFromRelationship(relationship string) string {
	return relationshipToEquivalence[relationship]
}
