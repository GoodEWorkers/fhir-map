package structuremap

// ProjectToR4 returns a deep copy adapted for FHIR R4 serialization (R5-only fields cleared, R4-required fields populated).
func ProjectToR4(sm *StructureMap) *StructureMap {
	if sm == nil {
		return nil
	}
	cloned := *sm
	cloned.CopyrightLabel = ""
	cloned.VersionAlgorithmString = ""
	cloned.VersionAlgorithmCoding = nil

	if len(sm.Group) > 0 {
		cloned.Group = make([]Group, len(sm.Group))
		for gi, g := range sm.Group {
			cg := g
			if len(g.Input) > 0 {
				cg.Input = append([]Input(nil), g.Input...)
			}
			if len(g.Rule) > 0 {
				cg.Rule = cloneRules(g.Rule)
			}
			cloned.Group[gi] = cg
		}
	}
	return &cloned
}

func cloneRules(rules []Rule) []Rule {
	out := make([]Rule, len(rules))
	for i := range rules {
		r := &rules[i]
		cr := *r
		if len(r.Source) > 0 {
			cr.Source = append([]Source(nil), r.Source...)
		}
		if len(r.Target) > 0 {
			cr.Target = make([]Target, len(r.Target))
			for ti := range r.Target {
				t := &r.Target[ti]
				ct := *t
				if ct.Context != "" && ct.ContextType == "" {
					ct.ContextType = "variable"
				}
				if len(t.ListMode) > 0 {
					ct.ListMode = append([]string(nil), t.ListMode...)
				}
				if len(t.Parameter) > 0 {
					ct.Parameter = append([]Parameter(nil), t.Parameter...)
				}
				cr.Target[ti] = ct
			}
		}
		if len(r.Rule) > 0 {
			cr.Rule = cloneRules(r.Rule)
		}
		if len(r.Dependent) > 0 {
			cr.Dependent = make([]Dependent, len(r.Dependent))
			for di, d := range r.Dependent {
				cd := d
				if len(d.Parameter) > 0 {
					cd.Parameter = append([]Parameter(nil), d.Parameter...)
				}
				cr.Dependent[di] = cd
			}
		}
		out[i] = cr
	}
	return out
}
