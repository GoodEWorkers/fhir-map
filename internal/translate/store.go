package translate

import (
	"context"
	"encoding/json"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// FlatStore is the persistence surface the flat-table translate engine reads
// from. It is intentionally narrower than the full conceptmap.Repository — only
// what the hot path needs — so the engine can be tested against an in-memory
// fake without dragging in the whole CRUD interface.
//
// Implementations:
//   - postgres.MappingStore reads concept_map_mappings via the indexed
//     (source_system, source_code, concept_map_pk) / (target_system,
//     target_code, concept_map_pk) compound indexes.
//   - the in-memory fake in engine_flat_test.go satisfies it for unit tests.
type FlatStore interface {
	// ResolveConceptMap returns the internal pk plus the canonical URL that
	// the engine should attribute matches to (cm.URL for instance/url lookups,
	// the resolved-by-source-scope URL otherwise). Returns conceptmap.ErrNotFound
	// when nothing matches.
	ResolveConceptMap(ctx context.Context, req Request) (FlatConceptMapRef, error)

	// QueryForward returns every mapping row whose source matches the
	// requested code/system inside the given ConceptMap, optionally
	// constrained to a specific target system.
	QueryForward(ctx context.Context, conceptMapPK int64, sourceSystem, sourceCode, targetSystemFilter string) ([]FlatRow, error)

	// QueryReverse returns every mapping row whose target matches the
	// requested code/system inside the given ConceptMap.
	QueryReverse(ctx context.Context, conceptMapPK int64, targetSystem, targetCode, targetSystemFilter string) ([]FlatRow, error)

	// GroupUnmapped returns the unmapped strategy for a given group key
	// (group.source / group.target), or nil if the group has none.
	GroupUnmapped(ctx context.Context, conceptMapPK int64, groupSource string) (*FlatUnmapped, error)
}

// FlatConceptMapRef is the small projection of concept_maps used to resolve
// the URL attribution on a match.
type FlatConceptMapRef struct {
	PK  int64
	URL string
}

// FlatRow is one row of concept_map_mappings projected for engine use.
type FlatRow struct {
	GroupIndex    int32
	ElementIndex  int32
	TargetIndex   int32
	SourceSystem  string
	SourceCode    string
	SourceDisplay string
	TargetSystem  string
	TargetCode    string
	TargetDisplay string
	Relationship  string
	// DependsOnJSONB/ProductJSONB are the raw stored bytes; the engine
	// unmarshals lazily into the domain DependsOn type only when emitting a
	// response (most rows don't have any).
	DependsOnJSONB []byte
	ProductJSONB   []byte
}

// FlatUnmapped mirrors conceptmap.Unmapped for the flat-table engine.
type FlatUnmapped struct {
	Mode         string
	Code         string
	Display      string
	Relationship string
	OtherMap     string
	GroupSource  string
	GroupTarget  string
}

// decodeDependsOn converts the stored JSONB blob back into a slice of the
// domain DependsOn type for response emission. Returns nil for empty blobs.
func decodeDependsOn(raw []byte) []conceptmap.DependsOn {
	if len(raw) == 0 {
		return nil
	}
	var out []conceptmap.DependsOn
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// toMatchDependencies builds the response shape from stored DependsOn rows.
func toMatchDependencies(deps []conceptmap.DependsOn) []fhir.TranslateMatchDependency {
	if len(deps) == 0 {
		return nil
	}
	out := make([]fhir.TranslateMatchDependency, len(deps))
	for i, d := range deps {
		out[i] = toMatchDependency(d)
	}
	return out
}
