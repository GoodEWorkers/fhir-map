package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/translate"
)

// TestIntegration_M3b_ShadowReadDiffsZero verifies shadow-read parity: both the JSONB and flat-table engines must produce byte-identical responses for all translate request shapes.
func TestIntegration_M3b_ShadowReadDiffsZero(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewConceptMapRepo(pool)
	store := NewMappingStore(pool)
	jsonbEngine := translate.NewEngine(repo)
	flatEngine := translate.NewFlatEngine(store)
	ctx := context.Background()

	files, err := filepath.Glob(filepath.Join("..", "..", "..", "docs", "conceptmap-*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	var ingested []ingestedMap

	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		var cm conceptmap.ConceptMap
		require.NoError(t, json.Unmarshal(raw, &cm))
		cm.ID = "" // let the repo assign a UUID
		if cm.Status == "" {
			cm.Status = "draft"
		}
		stored, err := repo.Create(ctx, &cm)
		if err != nil {
			t.Logf("skipping %s — strict validator rejected fixture: %v", filepath.Base(f), err)
			continue
		}
		ingested = append(ingested, ingestedMap{path: f, cm: stored})
	}
	require.NotEmpty(t, ingested, "no fixtures ingested; cannot diff-test anything")

	cases := buildDiffCases(ingested)
	t.Logf("running %d diff cases across %d fixtures", len(cases), len(ingested))

	var diffs []string
	for _, c := range cases {
		jResp, jErr := jsonbEngine.Translate(ctx, c.req)
		fResp, fErr := flatEngine.Translate(ctx, c.req)

		// Check only error nilness (not error type) for agreement since "not found" can be wrapped differently.
		if (jErr == nil) != (fErr == nil) {
			diffs = append(diffs, fmt.Sprintf("[%s] error nilness diverged: jsonb=%v flat=%v", c.name, jErr, fErr))
			continue
		}
		if jErr != nil {
			continue
		}

		if d := diffResponses(jResp, fResp); d != "" {
			diffs = append(diffs, fmt.Sprintf("[%s] %s", c.name, d))
		}
	}

	if len(diffs) > 0 {
		t.Fatalf("shadow-read diff oracle found %d divergence(s):\n  - %s", len(diffs),
			joinFirstN(diffs, 25, "\n  - "))
	}
}

// diffCase pairs a human-readable name with the translate.Request to fire at
// both engines.
type diffCase struct {
	name string
	req  translate.Request
}

// ingestedMap pairs a fixture path with the ConceptMap that successfully
// loaded into the repo (some fixtures may fail the strict validator).
type ingestedMap struct {
	path string
	cm   *conceptmap.ConceptMap
}

// buildDiffCases generates translate requests for every element in each fixture, limited to 50 per fixture to keep test count reasonable.
func buildDiffCases(ingested []ingestedMap) []diffCase {
	var cases []diffCase
	for _, m := range ingested {
		cm := m.cm
		name := filepath.Base(m.path)
		if cm.URL == "" {
			continue
		}
		// One pair (sourceCode, sourceSystem) per (group, element) — limited to
		// 50 per fixture so the specimen-type map doesn't blow the case count.
		probed := 0
		for gi, g := range cm.Group {
			for ei, e := range g.Element {
				if probed >= 50 {
					break
				}
				if e.Code == "" {
					continue
				}
				cases = append(cases,
					diffCase{
						name: fmt.Sprintf("%s g%d e%d byURL", name, gi, ei),
						req: translate.Request{
							URL:          cm.URL,
							SourceCode:   e.Code,
							SourceSystem: g.Source,
						},
					},
					diffCase{
						name: fmt.Sprintf("%s g%d e%d byID", name, gi, ei),
						req: translate.Request{
							ConceptMapID: cm.ID,
							SourceCode:   e.Code,
							SourceSystem: g.Source,
						},
					},
				)
				if len(e.Target) > 0 {
					tc := e.Target[0].Code
					cases = append(cases, diffCase{
						name: fmt.Sprintf("%s g%d e%d reverse", name, gi, ei),
						req: translate.Request{
							URL:          cm.URL,
							TargetCode:   tc,
							TargetSystem: g.Target,
						},
					})
				}
				probed++
			}
		}

		cases = append(cases, diffCase{
			name: fmt.Sprintf("%s unmapped-miss", name),
			req: translate.Request{
				URL:          cm.URL,
				SourceCode:   "definitely-not-in-this-map-x9z",
				SourceSystem: firstGroupSource(cm),
			},
		})
	}
	return cases
}

func firstGroupSource(cm *conceptmap.ConceptMap) string {
	if len(cm.Group) == 0 {
		return ""
	}
	return cm.Group[0].Source
}

// diffResponses marshals both responses to JSON and compares them byte-for-byte (JSON canonicalises map iteration order).
func diffResponses(a, b *translate.Response) string {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	if string(aj) == string(bj) {
		return ""
	}
	return fmt.Sprintf("response divergence:\n   jsonb=%s\n   flat=%s", string(aj), string(bj))
}

func joinFirstN(items []string, n int, sep string) string {
	if len(items) > n {
		items = items[:n]
		items[n-1] += fmt.Sprintf("  ...(%d more truncated)", len(items)-n)
	}
	out := ""
	for i, it := range items {
		if i > 0 {
			out += sep
		}
		out += it
	}
	return out
}

// Compile-time guard: postgres.MappingStore satisfies translate.FlatStore.
var _ translate.FlatStore = (*MappingStore)(nil)
