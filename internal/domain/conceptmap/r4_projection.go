package conceptmap

// CanonicaliseFromR4 walks a freshly-decoded ConceptMap that may have been
// delivered with R4 `equivalence` codes and copies each Target.Equivalence
// into Target.Relationship so the engine sees consistent R5 vocabulary.
// Targets that already carry a relationship win; targets carrying only an
// equivalence are upgraded; targets carrying both are left alone (the explicit
// relationship is authoritative).
//
// This is called on ingress in the R4 handler. After it returns the resource
// is safe to persist exactly as if it had arrived on the R5 tree.
func CanonicaliseFromR4(cm *ConceptMap) {
	if cm == nil {
		return
	}
	for gi := range cm.Group {
		for ei := range cm.Group[gi].Element {
			for ti := range cm.Group[gi].Element[ei].Target {
				t := &cm.Group[gi].Element[ei].Target[ti]
				if t.Relationship == "" && t.Equivalence != "" {
					if rel, _ := RelationshipFromEquivalence(t.Equivalence); rel != "" {
						t.Relationship = rel
					}
				}
				// Equivalence is wire-only — drop it before persistence so the
				// stored JSON is canonical R5.
				t.Equivalence = ""
			}
		}
	}
}

// ProjectToR4 returns a deep copy of cm stripped of all R5-only fields, with
// every Target.Relationship moved into Target.Equivalence (and Relationship
// cleared). Use this on egress from the R4 URL tree.
//
// The original cm is not modified.
func ProjectToR4(cm *ConceptMap) *ConceptMap {
	if cm == nil {
		return nil
	}
	cloned := *cm

	cloned.CopyrightLabel = ""
	cloned.VersionAlgorithmString = ""
	cloned.VersionAlgorithmCoding = nil
	cloned.AdditionalAttribute = nil
	cloned.Property = nil
	// R5 renamed sourceScope[x]/targetScope[x] from R4 source[x]/target[x];
	// clear the R5 names (R4 client resolves source/target from group declarations).
	cloned.SourceScopeURI = ""
	cloned.SourceScopeCanonical = ""
	cloned.TargetScopeURI = ""
	cloned.TargetScopeCanonical = ""

	cloned.Group = make([]Group, len(cm.Group))
	for gi, g := range cm.Group {
		cg := g
		cg.SourceVersion = ""
		cg.TargetVersion = ""
		if cg.Unmapped != nil {
			u := *cg.Unmapped
			u.Relationship = ""
			u.OtherMap = ""
			cg.Unmapped = &u
		}
		cg.Element = make([]Element, len(g.Element))
		for ei, e := range g.Element {
			ce := e
			ce.NoMap = nil
			ce.ValueSet = ""
			ce.Target = make([]Target, len(e.Target))
			for ti := range e.Target {
				ct := e.Target[ti]
				ct.Property = nil
				ct.ValueSet = ""
				if eq := EquivalenceFromRelationship(ct.Relationship); eq != "" {
					ct.Equivalence = eq
				}
				ct.Relationship = ""
				ce.Target[ti] = ct
			}
			cg.Element[ei] = ce
		}
		cloned.Group[gi] = cg
	}
	return &cloned
}
