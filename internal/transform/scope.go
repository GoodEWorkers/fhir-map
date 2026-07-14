package transform

import "github.com/goodeworkers/fhir-map/internal/domain/structuremap"

// scope is the variable environment for a single executor invocation; a linked-list of frames isolates nested scopes without clobbering enclosing bindings.
type scope struct {
	parent  *scope
	values  map[string]any
	sm      *structuremap.StructureMap
	imports []*structuremap.StructureMap // pre-resolved FML imports (root frame only)
	shared  map[string]any               // target listMode `share` instances, keyed by listRuleId (root frame only)
}

func newScope() *scope { return &scope{values: map[string]any{}} }

// setMap stashes a reference to the StructureMap currently being executed.
// `dependent` rule resolution walks sm.Group to find the named group.
func (s *scope) setMap(sm *structuremap.StructureMap) { s.sm = sm }

func (s *scope) set(name string, v any) {
	if name == "" {
		return
	}
	s.values[name] = v
}

// setRoot writes a binding into the outermost frame. Used for variables
// introduced by `target.variable` — the FHIR mapping spec treats them as
// living for the whole transform, so later rules in the same group must
// see them even after the enclosing rule's iteration scope has popped.
func (s *scope) setRoot(name string, v any) {
	if name == "" {
		return
	}
	s.root().values[name] = v
}

func (s *scope) get(name string) (any, bool) {
	for cur := s; cur != nil; cur = cur.parent {
		if v, ok := cur.values[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func (s *scope) root() *scope {
	cur := s
	for cur.parent != nil {
		cur = cur.parent
	}
	return cur
}

// envSnapshot flattens every scope frame into a single map[string]any; inner frames shadow outer ones.
func (s *scope) envSnapshot() map[string]any {
	out := map[string]any{}
	for cur := s; cur != nil; cur = cur.parent {
		for k, v := range cur.values {
			if _, exists := out[k]; !exists {
				out[k] = v
			}
		}
	}
	return out
}

// getShared returns the target instance shared under listRuleId by a target
// with listMode `share`. The store lives on the root frame so it persists
// across the per-iteration child scopes a repeating source spawns — every
// firing of the same rule sees the one instance (HAPI's sharedVars).
func (s *scope) getShared(listRuleID string) (any, bool) {
	r := s.root()
	if r.shared == nil {
		return nil, false
	}
	v, ok := r.shared[listRuleID]
	return v, ok
}

// setShared records the instance to reuse for subsequent firings under
// listRuleID. Stored on the root frame (see getShared).
func (s *scope) setShared(listRuleID string, v any) {
	r := s.root()
	if r.shared == nil {
		r.shared = map[string]any{}
	}
	r.shared[listRuleID] = v
}

// lookupGroup finds a group by name on the currently-executing StructureMap.
// Walks to the root since `sm` is set there at engine start.
func (s *scope) lookupGroup(name string) *structuremap.Group {
	root := s.root()
	if root.sm == nil {
		return nil
	}
	for i := range root.sm.Group {
		if root.sm.Group[i].Name == name {
			return &root.sm.Group[i]
		}
	}
	return nil
}

// setImports stores pre-resolved imported StructureMaps on the root frame so all nested scopes can access them.
func (s *scope) setImports(maps []*structuremap.StructureMap) {
	s.root().imports = maps
}

// lookupGroupAcrossImports finds a group by name on the executing map first,
// then on each imported map in declaration order. Returns nil when not found.
func (s *scope) lookupGroupAcrossImports(name string) *structuremap.Group {
	if g := s.lookupGroup(name); g != nil {
		return g
	}
	root := s.root()
	for _, imp := range root.imports {
		if imp == nil {
			continue
		}
		for i := range imp.Group {
			if imp.Group[i].Name == name {
				return &imp.Group[i]
			}
		}
	}
	return nil
}
