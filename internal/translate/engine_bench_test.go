package translate

import (
	"context"
	"strconv"
	"testing"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// BenchmarkTranslateForward benchmarks the core translation logic.
func BenchmarkTranslateForward(b *testing.B) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	req := Request{
		URL:          "http://hl7.org/fhir/ConceptMap/101",
		SourceCode:   "home",
		SourceSystem: "http://hl7.org/fhir/address-use",
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := engine.Translate(context.Background(), req)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTranslateForward_LargeMap benchmarks translation with a large ConceptMap.
func BenchmarkTranslateForward_LargeMap(b *testing.B) {
	elements := make([]conceptmap.Element, 200)
	for i := range elements {
		code := "SRC-" + string(rune('A'+i%26)) + string(rune('0'+i/26))
		elements[i] = conceptmap.Element{
			Code: code,
			Target: []conceptmap.Target{
				{Code: "TGT-" + code, Relationship: "equivalent"},
			},
		}
	}

	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "large-map",
		URL:          "http://example.org/large-map",
		Status:       "active",
		Group: []conceptmap.Group{
			{
				Source:  "http://source.system",
				Target:  "http://target.system",
				Element: elements,
			},
		},
	}

	repo := newMockRepo()
	repo.Create(context.Background(), cm)
	engine := NewEngine(repo)

	// Translate the last element (worst case: linear scan)
	req := Request{
		URL:          "http://example.org/large-map",
		SourceCode:   elements[199].Code,
		SourceSystem: "http://source.system",
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := engine.Translate(context.Background(), req)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTranslateReverse benchmarks reverse translation.
func BenchmarkTranslateReverse(b *testing.B) {
	repo := newMockRepo()
	repo.Create(context.Background(), makeExample101())
	engine := NewEngine(repo)

	req := Request{
		URL: "http://hl7.org/fhir/ConceptMap/101",
		TargetCoding: &fhir.Coding{
			System: "http://terminology.hl7.org/CodeSystem/v3-AddressUse",
			Code:   "H",
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := engine.Translate(context.Background(), req)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// memFlatStore is a minimal in-memory FlatStore for benchmark use. It builds
// lookup maps at construction time for O(1) forward queries.
type memFlatStore struct {
	url string
	// forward maps (sourceSystem, sourceCode) → []FlatRow
	forward map[[2]string][]FlatRow
}

func newMemFlatStore(url string, cm *conceptmap.ConceptMap) *memFlatStore {
	s := &memFlatStore{
		url:     url,
		forward: make(map[[2]string][]FlatRow),
	}
	for _, group := range cm.Group {
		for _, elem := range group.Element {
			key := [2]string{group.Source, elem.Code}
			for _, tgt := range elem.Target {
				s.forward[key] = append(s.forward[key], FlatRow{
					SourceSystem: group.Source,
					SourceCode:   elem.Code,
					TargetSystem: group.Target,
					TargetCode:   tgt.Code,
					Relationship: tgt.Relationship,
				})
			}
		}
	}
	return s
}

func (s *memFlatStore) ResolveConceptMap(_ context.Context, req Request) (FlatConceptMapRef, error) {
	if req.URL == s.url || req.URL == "" {
		return FlatConceptMapRef{PK: 1, URL: s.url}, nil
	}
	return FlatConceptMapRef{}, conceptmap.ErrNotFound
}

func (s *memFlatStore) QueryForward(_ context.Context, _ int64, sourceSystem, sourceCode, _ string) ([]FlatRow, error) {
	return s.forward[[2]string{sourceSystem, sourceCode}], nil
}

func (s *memFlatStore) QueryReverse(_ context.Context, _ int64, _, _, _ string) ([]FlatRow, error) {
	return nil, nil
}

func (s *memFlatStore) GroupUnmapped(_ context.Context, _ int64, _ string) (*FlatUnmapped, error) {
	return nil, nil
}

// BenchmarkBatch_50kProbes_100kConceptMap benchmarks $translate-batch with 50k probes against 100k mappings; SLA gate (≤2s p95 on 4vCPU/8GB CI) enforced externally.
func BenchmarkBatch_50kProbes_100kConceptMap(b *testing.B) {
	const nMappings = 100_000
	const nProbes = 50_000

	// Build a 100k-row ConceptMap. Half have mappings (even indices),
	// half are misses (odd indices) — simulates realistic mixed workloads.
	elements := make([]conceptmap.Element, nMappings)
	for i := range elements {
		code := "SRC-" + itoa(i)
		elements[i] = conceptmap.Element{
			Code: code,
			Target: []conceptmap.Target{
				{Code: "TGT-" + code, Relationship: "equivalent"},
			},
		}
	}
	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		ID:           "bench-100k",
		URL:          "http://example.org/bench/100k",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source:  "http://src",
			Target:  "http://tgt",
			Element: elements,
		}},
	}

	store := newMemFlatStore(cm.URL, cm)
	engine := NewFlatEngine(store)

	probes := make([]BatchProbe, nProbes)
	for i := range probes {
		if i < nProbes/2 {
			probes[i] = BatchProbe{SourceCode: "SRC-" + itoa(i*2), SourceSystem: "http://src"}
		} else {
			probes[i] = BatchProbe{SourceCode: "MISS-" + itoa(i), SourceSystem: "http://src"}
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := engine.TranslateBatch(context.Background(), cm.URL, "", "", probes, "")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// itoa delegates to strconv.Itoa for correctness and maintainability.
func itoa(n int) string {
	return strconv.Itoa(n)
}
