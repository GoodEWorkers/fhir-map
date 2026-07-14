package transform

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

func TestMergeImports_NoImports_ReturnsSameMap(t *testing.T) {
	sm := &structuremap.StructureMap{ResourceType: "StructureMap", URL: "u", Group: []structuremap.Group{{Name: "g"}}}
	got := mergeImports(sm, nil)
	assert.Same(t, sm, got, "no imports must be a pointer-identity no-op (zero blast radius)")
}

func TestMergeRules_BaseWinsOnTarget_SubRulesUnion(t *testing.T) {
	base := &structuremap.Rule{
		Name:   "r",
		Target: []structuremap.Target{{Context: "tgt", Element: "obs", Variable: "o", Transform: "create"}},
		Rule:   []structuremap.Rule{{Name: "base"}},
	}
	imp := &structuremap.Rule{
		Name: "r",
		// a duplicate create that MUST be dropped (base wins on rule-level target)
		Target: []structuremap.Target{{Context: "tgt", Element: "obs", Variable: "o2", Transform: "create"}},
		Rule:   []structuremap.Rule{{Name: "enrich"}, {Name: "base"}},
	}
	mergeRules(base, imp)

	require.Len(t, base.Target, 1, "base wins: imported duplicate create dropped")
	assert.Equal(t, "o", base.Target[0].Variable)
	names := make([]string, 0, len(base.Rule))
	for _, sr := range base.Rule {
		names = append(names, sr.Name)
	}
	assert.ElementsMatch(t, []string{"base", "enrich"}, names, "sub-rules unioned by name (enrich added, base merged not duplicated)")
}

func TestMergeRules_FillsEmptyBaseFromImport(t *testing.T) {
	base := &structuremap.Rule{Name: "r"} // no source/target
	imp := &structuremap.Rule{
		Name:   "r",
		Source: []structuremap.Source{{Context: "src", Element: "x", Variable: "v"}},
		Target: []structuremap.Target{{Context: "tgt", Element: "y", Transform: "copy"}},
	}
	mergeRules(base, imp)
	require.Len(t, base.Source, 1)
	require.Len(t, base.Target, 1)
}

func TestSameGroupSignature(t *testing.T) {
	mk := func(st, tt string) structuremap.Group {
		return structuremap.Group{Input: []structuremap.Input{
			{Name: "s", Type: st, Mode: "source"}, {Name: "t", Type: tt, Mode: "target"}}}
	}
	assert.True(t, sameGroupSignature(mk("HL7v2", "Bundle"), mk("HL7v2", "Bundle")))
	assert.False(t, sameGroupSignature(mk("HL7v2", "Bundle"), mk("CDA", "Bundle")))
	assert.False(t, sameGroupSignature(mk("HL7v2", "Bundle"), structuremap.Group{}))
}

func TestMergeContained_UnionsByIDOrURL(t *testing.T) {
	base := []*conceptmap.ConceptMap{{ID: "cm1"}}
	imp := []*conceptmap.ConceptMap{{ID: "cm1"}, {ID: "cm2"}, {URL: "http://x/cm3"}}
	got := mergeContained(base, imp)
	ids := map[string]bool{}
	for _, c := range got {
		if c.ID != "" {
			ids[c.ID] = true
		} else {
			ids[c.URL] = true
		}
	}
	assert.Len(t, got, 3, "cm1 deduped; cm2 + cm3 added")
	assert.True(t, ids["cm1"] && ids["cm2"] && ids["http://x/cm3"])
}

func TestMergeStructureMaps_GroupMatchBySignature(t *testing.T) {
	base := &structuremap.StructureMap{
		ResourceType: "StructureMap", URL: "base",
		Group: []structuremap.Group{{
			Name:  "main",
			Input: []structuremap.Input{{Type: "HL7v2", Mode: "source"}, {Type: "Bundle", Mode: "target"}},
			Rule:  []structuremap.Rule{{Name: "a"}},
		}},
	}
	imp := &structuremap.StructureMap{
		ResourceType: "StructureMap", URL: "imp",
		Group: []structuremap.Group{
			{ // same name + signature -> rules merge into base.main
				Name:  "main",
				Input: []structuremap.Input{{Type: "HL7v2", Mode: "source"}, {Type: "Bundle", Mode: "target"}},
				Rule:  []structuremap.Rule{{Name: "b"}},
			},
			{ // same name, different signature -> kept separate
				Name:  "main",
				Input: []structuremap.Input{{Type: "CDA", Mode: "source"}, {Type: "Bundle", Mode: "target"}},
				Rule:  []structuremap.Rule{{Name: "c"}},
			},
		},
	}
	mergeStructureMaps(base, imp)
	require.Len(t, base.Group, 2, "the differently-signatured group is appended, not merged")
	main := base.Group[0]
	names := make([]string, 0, len(main.Rule))
	for _, r := range main.Rule {
		names = append(names, r.Name)
	}
	assert.ElementsMatch(t, []string{"a", "b"}, names)
}
