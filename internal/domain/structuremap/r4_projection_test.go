package structuremap

import "testing"

func TestProjectToR4_StripsCopyrightLabel(t *testing.T) {
	src := &StructureMap{
		ResourceType:   "StructureMap",
		ID:             "x1",
		Name:           "Demo",
		Status:         "active",
		Copyright:      "© Demo Org",
		CopyrightLabel: "Demo License v1", // R5-only
	}

	out := ProjectToR4(src)

	if out == nil {
		t.Fatalf("ProjectToR4 returned nil for non-nil input")
	}
	if out.CopyrightLabel != "" {
		t.Errorf("CopyrightLabel = %q, want empty (R5-only must be stripped)", out.CopyrightLabel)
	}
	if out.Copyright != "© Demo Org" {
		t.Errorf("Copyright should survive projection (R4 supports it), got %q", out.Copyright)
	}
	if out.Name != "Demo" || out.Status != "active" || out.ID != "x1" {
		t.Errorf("non-version-specific fields lost: %+v", out)
	}
}

// Canonical resource is cached for other callers; in-place mutation would corrupt storage.
func TestProjectToR4_DoesNotMutateOriginal(t *testing.T) {
	src := &StructureMap{
		CopyrightLabel: "keep-me",
		Group: []Group{{
			Name: "g",
			Rule: []Rule{{Name: "r"}},
		}},
	}
	_ = ProjectToR4(src)
	if src.CopyrightLabel != "keep-me" {
		t.Errorf("input was mutated: CopyrightLabel = %q", src.CopyrightLabel)
	}
	if len(src.Group) != 1 || src.Group[0].Name != "g" {
		t.Errorf("input Group mutated: %+v", src.Group)
	}
}

// Deep-copy guarantee: editing the projection must not leak into the source.
func TestProjectToR4_DeepCopyNestedSlices(t *testing.T) {
	src := &StructureMap{
		Group: []Group{{
			Name: "g0",
			Rule: []Rule{{
				Name:   "r0",
				Target: []Target{{Element: "e0"}},
			}},
		}},
	}
	out := ProjectToR4(src)
	out.Group[0].Rule[0].Target[0].Element = "MUTATED"
	if src.Group[0].Rule[0].Target[0].Element != "e0" {
		t.Errorf("projection mutation leaked into source: src element = %q",
			src.Group[0].Rule[0].Target[0].Element)
	}
}

func TestProjectToR4_NilInputReturnsNil(t *testing.T) {
	if got := ProjectToR4(nil); got != nil {
		t.Errorf("ProjectToR4(nil) = %+v, want nil", got)
	}
}

// R4 smp-2: targets must carry contextType on wire; R5 dropped it from the model.
// Projection populates default "variable" to preserve variable-bound rules.
func TestProjectToR4_PopulatesTargetContextType(t *testing.T) {
	src := &StructureMap{
		Group: []Group{{
			Name: "g",
			Rule: []Rule{{
				Name:   "r0",
				Source: []Source{{Context: "source", Element: "id", Variable: "v"}},
				Target: []Target{{Context: "target", Element: "id", Transform: "copy"}},
				Rule: []Rule{{
					Name:   "nested",
					Source: []Source{{Context: "src2"}},
					Target: []Target{{Context: "tgt2"}},
				}},
			}},
		}},
	}

	out := ProjectToR4(src)

	if got := out.Group[0].Rule[0].Target[0].ContextType; got != "variable" {
		t.Errorf("target[0].contextType = %q, want %q", got, "variable")
	}
	if got := out.Group[0].Rule[0].Rule[0].Target[0].ContextType; got != "variable" {
		t.Errorf("nested target.contextType = %q, want %q", got, "variable")
	}

	// Canonical input must stay unchanged so R5 egress does not emit contextType.
	if src.Group[0].Rule[0].Target[0].ContextType != "" {
		t.Errorf("input target.contextType leaked: %q", src.Group[0].Rule[0].Target[0].ContextType)
	}
}

// When the caller has already set Target.ContextType (e.g. tests, explicit
// "type"), the projection must not overwrite it.
func TestProjectToR4_PreservesExistingTargetContextType(t *testing.T) {
	src := &StructureMap{
		Group: []Group{{
			Rule: []Rule{{
				Target: []Target{{Context: "target", ContextType: "type"}},
			}},
		}},
	}
	out := ProjectToR4(src)
	if got := out.Group[0].Rule[0].Target[0].ContextType; got != "type" {
		t.Errorf("target.contextType = %q, want preserved %q", got, "type")
	}
}
