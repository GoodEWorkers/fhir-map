package translate

import (
	"context"
	"errors"
	"testing"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// cfgFlatStore is a configurable, PK-aware in-memory FlatStore for FlatEngine
// unit tests. Each ConceptMap URL gets its own pk and isolated row/unmapped
// data, so cross-map (other-map) recursion can be exercised realistically.
// It deliberately does NOT implement BatchFlatStore, so TranslateBatch exercises
// the fan-out fallback path against it; batchCfgStore covers the fast path.
type cfgFlatStore struct {
	pkByURL  map[string]int64
	data     map[int64]*pkData
	nextPK   int64
	defURL   string
	notFound map[string]bool

	resolveErr error
	forwardErr error
	reverseErr error
	unmapErr   error
}

type pkData struct {
	forward  map[[2]string][]FlatRow
	reverse  map[[2]string][]FlatRow
	unmapped map[string]*FlatUnmapped
}

func newCfgStore(defURL string) *cfgFlatStore {
	s := &cfgFlatStore{
		pkByURL:  make(map[string]int64),
		data:     make(map[int64]*pkData),
		notFound: make(map[string]bool),
		defURL:   defURL,
	}
	s.mapFor(defURL) // pre-register the default map as pk 1
	return s
}

// mapFor returns (creating if needed) the per-map data for a URL.
func (s *cfgFlatStore) mapFor(url string) *pkData {
	if pk, ok := s.pkByURL[url]; ok {
		return s.data[pk]
	}
	s.nextPK++
	d := &pkData{
		forward:  make(map[[2]string][]FlatRow),
		reverse:  make(map[[2]string][]FlatRow),
		unmapped: make(map[string]*FlatUnmapped),
	}
	s.pkByURL[url] = s.nextPK
	s.data[s.nextPK] = d
	return d
}

func (s *cfgFlatStore) addForward(url, sys, code string, rows ...FlatRow) {
	d := s.mapFor(url)
	d.forward[[2]string{sys, code}] = rows
}

func (s *cfgFlatStore) addReverse(url, sys, code string, rows ...FlatRow) {
	d := s.mapFor(url)
	d.reverse[[2]string{sys, code}] = rows
}

func (s *cfgFlatStore) addUnmapped(url, groupSource string, u *FlatUnmapped) {
	d := s.mapFor(url)
	d.unmapped[groupSource] = u
}

func (s *cfgFlatStore) ResolveConceptMap(_ context.Context, req Request) (FlatConceptMapRef, error) {
	if s.resolveErr != nil {
		return FlatConceptMapRef{}, s.resolveErr
	}
	url := req.URL
	if url == "" {
		url = s.defURL
	}
	if s.notFound[url] {
		return FlatConceptMapRef{}, conceptmap.ErrNotFound
	}
	return FlatConceptMapRef{PK: s.pkByURL[s.mapForURL(url)], URL: url}, nil
}

// mapForURL ensures a pk exists for url and returns the url (helper for Resolve).
func (s *cfgFlatStore) mapForURL(url string) string {
	s.mapFor(url)
	return url
}

func (s *cfgFlatStore) QueryForward(_ context.Context, pk int64, sourceSystem, sourceCode, _ string) ([]FlatRow, error) {
	if s.forwardErr != nil {
		return nil, s.forwardErr
	}
	if d := s.data[pk]; d != nil {
		return d.forward[[2]string{sourceSystem, sourceCode}], nil
	}
	return nil, nil
}

func (s *cfgFlatStore) QueryReverse(_ context.Context, pk int64, targetSystem, targetCode, _ string) ([]FlatRow, error) {
	if s.reverseErr != nil {
		return nil, s.reverseErr
	}
	if d := s.data[pk]; d != nil {
		return d.reverse[[2]string{targetSystem, targetCode}], nil
	}
	return nil, nil
}

func (s *cfgFlatStore) GroupUnmapped(_ context.Context, pk int64, groupSource string) (*FlatUnmapped, error) {
	if s.unmapErr != nil {
		return nil, s.unmapErr
	}
	if d := s.data[pk]; d != nil {
		return d.unmapped[groupSource], nil
	}
	return nil, nil
}

// batchCfgStore embeds cfgFlatStore and adds BatchQueryForward, so TranslateBatch
// takes the fast path through buildSingleResponse.
type batchCfgStore struct {
	*cfgFlatStore
	batchErr error
}

func (s *batchCfgStore) BatchQueryForward(_ context.Context, pk int64, probes []BatchProbe, _ string) ([][]FlatRow, error) {
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	out := make([][]FlatRow, len(probes))
	d := s.data[pk]
	for i, p := range probes {
		if d != nil {
			out[i] = d.forward[[2]string{p.SourceSystem, p.SourceCode}]
		}
	}
	return out, nil
}

func fwdRow(srcSys, srcCode, tgtSys, tgtCode, rel string) FlatRow {
	return FlatRow{
		SourceSystem: srcSys, SourceCode: srcCode,
		TargetSystem: tgtSys, TargetCode: tgtCode, TargetDisplay: tgtCode + "-disp",
		Relationship: rel,
	}
}

func TestFlatEngine_Translate_ForwardMatch(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.addForward("http://maps/a", "http://src", "A", fwdRow("http://src", "A", "http://tgt", "X", "equivalent"))
	resp, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/a", SourceSystem: "http://src", SourceCode: "A",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Result || len(resp.Matches) != 1 {
		t.Fatalf("expected one positive match, got result=%v matches=%d", resp.Result, len(resp.Matches))
	}
	m := resp.Matches[0]
	if m.Concept.Code != "X" || m.Concept.System != "http://tgt" || m.OriginMap != "http://maps/a" {
		t.Fatalf("unexpected match: %+v", m)
	}
}

func TestFlatEngine_Translate_NoMatch_NoUnmapped(t *testing.T) {
	s := newCfgStore("http://maps/a")
	resp, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/a", SourceSystem: "http://src", SourceCode: "missing",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Result || resp.Message != "No mapping found for the provided concept" {
		t.Fatalf("expected no-match message, got result=%v msg=%q", resp.Result, resp.Message)
	}
}

func TestFlatEngine_Translate_NegativeOnly(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.addForward("http://maps/a", "http://src", "A", fwdRow("http://src", "A", "http://tgt", "X", "not-related-to"))
	resp, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/a", SourceSystem: "http://src", SourceCode: "A",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Result || resp.Message != "Only negative matches found" {
		t.Fatalf("expected negative-only, got result=%v msg=%q", resp.Result, resp.Message)
	}
}

func TestFlatEngine_Translate_Unmapped_Fixed(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.addUnmapped("http://maps/a", "http://src", &FlatUnmapped{Mode: "fixed", Code: "UNK", Display: "Unknown", Relationship: "equivalent", GroupTarget: "http://tgt"})
	resp, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/a", SourceSystem: "http://src", SourceCode: "nope",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Result || resp.Matches[0].Concept.Code != "UNK" {
		t.Fatalf("expected fixed unmapped match, got %+v", resp)
	}
}

func TestFlatEngine_Translate_Unmapped_UseSourceCode(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.addUnmapped("http://maps/a", "http://src", &FlatUnmapped{Mode: "use-source-code", Relationship: "equivalent", GroupTarget: "http://tgt"})
	resp, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/a", SourceSystem: "http://src", SourceCode: "passthru",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Result || resp.Matches[0].Concept.Code != "passthru" {
		t.Fatalf("expected use-source-code match, got %+v", resp)
	}
}

func TestFlatEngine_Translate_Unmapped_UnknownMode(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.addUnmapped("http://maps/a", "http://src", &FlatUnmapped{Mode: "provided", GroupTarget: "http://tgt"})
	resp, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/a", SourceSystem: "http://src", SourceCode: "x",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Result {
		t.Fatalf("unknown unmapped mode should yield no match, got %+v", resp)
	}
}

// Map A has no direct row for "A" and an other-map pointer to B; B has the row.
func TestFlatEngine_Translate_Unmapped_OtherMap_Resolves(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.addUnmapped("http://maps/a", "http://src", &FlatUnmapped{Mode: "other-map", OtherMap: "http://maps/b", GroupSource: "http://src"})
	s.addForward("http://maps/b", "http://src", "A", fwdRow("http://src", "A", "http://tgt", "Z", "equivalent"))
	resp, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/a", SourceSystem: "http://src", SourceCode: "A",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Result || len(resp.Matches) != 1 || resp.Matches[0].Concept.Code != "Z" {
		t.Fatalf("expected other-map match Z, got %+v", resp)
	}
}

func TestFlatEngine_Translate_Unmapped_OtherMap_DepthCap(t *testing.T) {
	s := newCfgStore("http://maps/a")
	// A → loop, and loop → loop (same groupSource) → infinite chain caught by cap.
	s.addUnmapped("http://maps/a", "http://src", &FlatUnmapped{Mode: "other-map", OtherMap: "http://maps/loop", GroupSource: "http://src"})
	s.addUnmapped("http://maps/loop", "http://src", &FlatUnmapped{Mode: "other-map", OtherMap: "http://maps/loop", GroupSource: "http://src"})
	_, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/a", SourceSystem: "http://src", SourceCode: "loopcode",
	})
	if err == nil {
		t.Fatalf("expected depth-cap error")
	}
}

func TestFlatEngine_Translate_OtherMap_EmptyTarget(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.addUnmapped("http://maps/a", "http://src", &FlatUnmapped{Mode: "other-map", OtherMap: "", GroupSource: "http://src"})
	resp, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/a", SourceSystem: "http://src", SourceCode: "x",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Result {
		t.Fatalf("empty other-map should yield no match")
	}
}

func TestFlatEngine_Translate_Reverse(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.addReverse("http://maps/a", "http://tgt", "X",
		FlatRow{SourceSystem: "http://src", SourceCode: "A", SourceDisplay: "Alpha", Relationship: "source-is-narrower-than-target"})
	resp, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL:          "http://maps/a",
		TargetCoding: &fhir.Coding{System: "http://tgt", Code: "X"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Result || len(resp.Matches) != 1 {
		t.Fatalf("expected one reverse match, got %+v", resp)
	}
	m := resp.Matches[0]
	if m.Concept.Code != "A" || m.Concept.System != "http://src" {
		t.Fatalf("reverse concept wrong: %+v", m.Concept)
	}
	if m.Relationship != "source-is-broader-than-target" {
		t.Fatalf("relationship should be reversed, got %q", m.Relationship)
	}
}

func TestFlatEngine_Translate_MultipleSourceCodings(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.addForward("http://maps/a", "http://src", "A", fwdRow("http://src", "A", "http://tgt", "X", "equivalent"))
	s.addForward("http://maps/a", "http://src", "B", fwdRow("http://src", "B", "http://tgt", "Y", "equivalent"))
	resp, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/a",
		SourceCodeableConcept: &fhir.CodeableConcept{Coding: []fhir.Coding{
			{System: "http://src", Code: "A"},
			{System: "http://src", Code: "B"},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Matches) != 2 {
		t.Fatalf("expected two matches, got %d", len(resp.Matches))
	}
}

func TestFlatEngine_Translate_ResolveNotFound(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.notFound["http://maps/missing"] = true
	_, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/missing", SourceSystem: "http://src", SourceCode: "A",
	})
	if !errors.Is(err, conceptmap.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestFlatEngine_Translate_ForwardStoreError(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.forwardErr = errors.New("db down")
	_, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL: "http://maps/a", SourceSystem: "http://src", SourceCode: "A",
	})
	if err == nil {
		t.Fatalf("expected forward store error")
	}
}

func TestFlatEngine_Translate_ReverseStoreError(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.reverseErr = errors.New("db down")
	_, err := NewFlatEngine(s).Translate(context.Background(), Request{
		URL:          "http://maps/a",
		TargetCoding: &fhir.Coding{System: "http://tgt", Code: "X"},
	})
	if err == nil {
		t.Fatalf("expected reverse store error")
	}
}

func TestFlatEngine_Translate_Inline(t *testing.T) {
	// Inline ConceptMap bypasses the flat store and uses the JSONB engine path.
	inline := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "http://maps/inline",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://src",
			Target: "http://tgt",
			Element: []conceptmap.Element{{
				Code:   "A",
				Target: []conceptmap.Target{{Code: "X", Relationship: "equivalent"}},
			}},
		}},
	}
	s := newCfgStore("http://maps/a")
	resp, err := NewFlatEngine(s).Translate(context.Background(), Request{
		ConceptMap: inline, SourceSystem: "http://src", SourceCode: "A",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Result || resp.Matches[0].Concept.Code != "X" {
		t.Fatalf("inline translate failed: %+v", resp)
	}
}

func TestFlatEngine_TranslateBatch_FastPath(t *testing.T) {
	base := newCfgStore("http://maps/a")
	base.addForward("http://maps/a", "http://src", "A", fwdRow("http://src", "A", "http://tgt", "X", "equivalent"))
	s := &batchCfgStore{cfgFlatStore: base}
	resp, err := NewFlatEngine(s).TranslateBatch(context.Background(), "http://maps/a", "", "", []BatchProbe{
		{SourceSystem: "http://src", SourceCode: "A"},
		{SourceSystem: "http://src", SourceCode: "MISS"},
	}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Overall || len(resp.Per) != 2 {
		t.Fatalf("expected overall true and 2 entries, got %+v", resp)
	}
	if !resp.Per[0].Result || resp.Per[1].Result {
		t.Fatalf("per-probe results wrong: %+v", resp.Per)
	}
}

func TestFlatEngine_TranslateBatch_Fallback(t *testing.T) {
	s := newCfgStore("http://maps/a") // no BatchFlatStore → fan-out path
	s.addForward("http://maps/a", "http://src", "A", fwdRow("http://src", "A", "http://tgt", "X", "equivalent"))
	resp, err := NewFlatEngine(s).TranslateBatch(context.Background(), "http://maps/a", "", "", []BatchProbe{
		{SourceSystem: "http://src", SourceCode: "A"},
		{SourceSystem: "http://src", SourceCode: "MISS"},
	}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Overall || !resp.Per[0].Result || resp.Per[1].Result {
		t.Fatalf("fallback batch results wrong: %+v", resp)
	}
}

func TestFlatEngine_TranslateBatch_FallbackDepthCap(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.addUnmapped("http://maps/a", "http://src", &FlatUnmapped{Mode: "other-map", OtherMap: "http://maps/loop", GroupSource: "http://src"})
	s.addUnmapped("http://maps/loop", "http://src", &FlatUnmapped{Mode: "other-map", OtherMap: "http://maps/loop", GroupSource: "http://src"})
	resp, err := NewFlatEngine(s).TranslateBatch(context.Background(), "http://maps/a", "", "", []BatchProbe{
		{SourceSystem: "http://src", SourceCode: "loop"},
	}, "")
	if err != nil {
		t.Fatalf("batch should absorb depth-cap into IsError, got err %v", err)
	}
	if resp.Per[0].Result || !resp.Per[0].IsError {
		t.Fatalf("expected IsError entry, got %+v", resp.Per[0])
	}
}

func TestFlatEngine_TranslateBatch_Empty(t *testing.T) {
	s := newCfgStore("http://maps/a")
	resp, err := NewFlatEngine(s).TranslateBatch(context.Background(), "http://maps/a", "", "", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Overall || len(resp.Per) != 0 {
		t.Fatalf("empty batch should be empty, got %+v", resp)
	}
}

func TestFlatEngine_TranslateBatch_ResolveError(t *testing.T) {
	s := newCfgStore("http://maps/a")
	s.resolveErr = errors.New("resolve boom")
	_, err := NewFlatEngine(s).TranslateBatch(context.Background(), "http://maps/a", "", "", []BatchProbe{
		{SourceSystem: "http://src", SourceCode: "A"},
	}, "")
	if err == nil {
		t.Fatalf("expected resolve error to propagate")
	}
}
