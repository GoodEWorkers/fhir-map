package transform

import (
	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

// mergeImports folds resolved FML imports into a copy of sm using merge-by-name
// (mirroring HAPI engine logic). Groups are matched by name+input-type signature;
// rules by name with base's source/target winning on conflict; sub-rules and
// dependents unioned by name; contained ConceptMaps unioned by id/url.
// Returns sm unchanged when there are no imports.
func mergeImports(sm *structuremap.StructureMap, imported []*structuremap.StructureMap) *structuremap.StructureMap {
	if len(imported) == 0 {
		return sm
	}
	base := shallowMapWithOwnGroups(sm)
	for _, imp := range imported {
		if imp != nil {
			mergeStructureMaps(base, imp)
		}
	}
	return base
}

// shallowMapWithOwnGroups returns a shallow copy with deep copies of the
// mutable parts (Group tree, Contained slice); everything else is shared (read-only).
func shallowMapWithOwnGroups(sm *structuremap.StructureMap) *structuremap.StructureMap {
	cp := *sm
	cp.Group = make([]structuremap.Group, len(sm.Group))
	for i := range sm.Group {
		cp.Group[i] = cloneGroup(sm.Group[i])
	}
	cp.Contained = append([]*conceptmap.ConceptMap(nil), sm.Contained...)
	return &cp
}

func mergeStructureMaps(base, imp *structuremap.StructureMap) {
	base.Contained = mergeContained(base.Contained, imp.Contained)
	for i := range imp.Group {
		ig := imp.Group[i]
		if bi := findGroupIndex(base, ig); bi >= 0 {
			mergeGroups(&base.Group[bi], &ig)
		} else {
			// Append: entry group must stay at index 0 (only entry is executed).
			base.Group = append(base.Group, cloneGroup(ig))
		}
	}
}

// findGroupIndex returns the index of a base group with the same name and the
// same input-type signature as g, or -1.
func findGroupIndex(base *structuremap.StructureMap, g structuremap.Group) int {
	for i := range base.Group {
		if base.Group[i].Name == g.Name && sameGroupSignature(base.Group[i], g) {
			return i
		}
	}
	return -1
}

// sameGroupSignature compares two groups' input lists by ordered (mode, type).
func sameGroupSignature(a, b structuremap.Group) bool {
	if len(a.Input) != len(b.Input) {
		return false
	}
	for i := range a.Input {
		if a.Input[i].Mode != b.Input[i].Mode || a.Input[i].Type != b.Input[i].Type {
			return false
		}
	}
	return true
}

func mergeGroups(base, imp *structuremap.Group) {
	for i := range imp.Rule {
		ir := imp.Rule[i]
		if bi := findRuleIndex(base.Rule, ir.Name); bi >= 0 {
			mergeRules(&base.Rule[bi], &ir)
		} else {
			// Prepend imported rules (run before base rules).
			base.Rule = append([]structuremap.Rule{cloneRule(ir)}, base.Rule...)
		}
	}
}

// mergeRules merges imp into base by name, with base's source/target winning.
// Sub-rules and dependents are unioned by name (recursively).
func mergeRules(base, imp *structuremap.Rule) {
	if len(base.Source) == 0 && len(imp.Source) > 0 {
		base.Source = append([]structuremap.Source(nil), imp.Source...)
	}
	if len(base.Target) == 0 && len(imp.Target) > 0 {
		base.Target = append([]structuremap.Target(nil), imp.Target...)
	}
	for i := range imp.Rule {
		isub := imp.Rule[i]
		if bi := findRuleIndex(base.Rule, isub.Name); bi >= 0 {
			mergeRules(&base.Rule[bi], &isub)
		} else {
			base.Rule = append(base.Rule, cloneRule(isub)) // sub-rules append
		}
	}
	for i := range imp.Dependent {
		if !hasDependent(base.Dependent, imp.Dependent[i].Name) {
			base.Dependent = append(base.Dependent, imp.Dependent[i])
		}
	}
}

func findRuleIndex(rules []structuremap.Rule, name string) int {
	for i := range rules {
		if rules[i].Name == name {
			return i
		}
	}
	return -1
}

func hasDependent(deps []structuremap.Dependent, name string) bool {
	for i := range deps {
		if deps[i].Name == name {
			return true
		}
	}
	return false
}

// mergeContained unions ConceptMaps by id then url; base entries win on conflict.
func mergeContained(base, imp []*conceptmap.ConceptMap) []*conceptmap.ConceptMap {
	seen := make(map[string]bool, len(base))
	key := func(cm *conceptmap.ConceptMap) string {
		if cm == nil {
			return ""
		}
		if cm.ID != "" {
			return "#" + cm.ID
		}
		return cm.URL
	}
	for _, cm := range base {
		seen[key(cm)] = true
	}
	for _, cm := range imp {
		if cm == nil {
			continue
		}
		if k := key(cm); k != "" && !seen[k] {
			base = append(base, cm)
			seen[k] = true
		}
	}
	return base
}

// --- focused deep-copy of the mutated tree -------------------------------

func cloneGroup(g structuremap.Group) structuremap.Group {
	cp := g
	cp.Input = append([]structuremap.Input(nil), g.Input...)
	cp.Rule = cloneRules(g.Rule)
	return cp
}

func cloneRules(rs []structuremap.Rule) []structuremap.Rule {
	if rs == nil {
		return nil
	}
	out := make([]structuremap.Rule, len(rs))
	for i := range rs {
		out[i] = cloneRule(rs[i])
	}
	return out
}

func cloneRule(r structuremap.Rule) structuremap.Rule {
	cp := r
	cp.Source = append([]structuremap.Source(nil), r.Source...)
	cp.Target = append([]structuremap.Target(nil), r.Target...)
	cp.Dependent = append([]structuremap.Dependent(nil), r.Dependent...)
	cp.Rule = cloneRules(r.Rule)
	return cp
}
