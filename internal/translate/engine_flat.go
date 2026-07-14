package translate

import (
	"context"
	"fmt"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// FlatEngine reads from concept_map_mappings (flat table) instead of the JSONB resource document for bounded performance.
type FlatEngine struct {
	store FlatStore
}

// NewFlatEngine wires a FlatEngine to its FlatStore. Production callers use
// postgres.NewMappingStore(pool); tests can supply an in-memory fake.
func NewFlatEngine(store FlatStore) *FlatEngine {
	return &FlatEngine{store: store}
}

// Translate runs the flat-table version of the $translate operation.
// Behaviour is intended to match the JSONB engine byte-for-byte on every
// well-formed request — that property is enforced by M3b's shadow-read diff
// test in repository/postgres.
func (e *FlatEngine) Translate(ctx context.Context, req Request) (*Response, error) {
	return e.translateWithDepth(ctx, req, 0)
}

func (e *FlatEngine) translateWithDepth(ctx context.Context, req Request, depth int) (*Response, error) {
	req = parsePipeVersion(req)

	// Inline ConceptMap requests bypass the flat store (no rows exist for an
	// in-memory map). Fall back to the JSONB engine path for that one case.
	if req.ConceptMap != nil {
		return inlineTranslate(ctx, req)
	}

	ref, err := e.store.ResolveConceptMap(ctx, req)
	if err != nil {
		return nil, err
	}

	targetCode, targetSystem, isReverse := resolveTargetConcept(req)

	var matches []fhir.TranslateMatch

	if isReverse {
		rows, err := e.store.QueryReverse(ctx, ref.PK, targetSystem, targetCode, req.TargetSystem)
		if err != nil {
			return nil, fmt.Errorf("flat-engine reverse query: %w", err)
		}
		for i := range rows {
			r := &rows[i]
			matches = append(matches, fhir.TranslateMatch{
				Relationship: reverseRelationship(r.Relationship),
				Concept: &fhir.Coding{
					System:  r.SourceSystem,
					Code:    r.SourceCode,
					Display: r.SourceDisplay,
				},
				OriginMap: ref.URL,
			})
		}
	} else {
		codings := resolveAllSourceCodings(req)
		if len(codings) == 0 {
			partial, err := e.forwardForCoding(ctx, ref, req, "", "", depth)
			if err != nil {
				return nil, err
			}
			matches = append(matches, partial...)
		} else {
			for _, c := range codings {
				partial, err := e.forwardForCoding(ctx, ref, req, c.Code, c.System, depth)
				if err != nil {
					return nil, err
				}
				matches = append(matches, partial...)
			}
		}
	}

	matches = deduplicateMatches(matches)

	result := false
	hasAnyMatch := len(matches) > 0
	for _, m := range matches {
		if m.Relationship != "not-related-to" {
			result = true
			break
		}
	}
	resp := &Response{Result: result, Matches: matches}
	if !result {
		if hasAnyMatch {
			resp.Message = "Only negative matches found"
		} else {
			resp.Message = "No mapping found for the provided concept"
		}
	}
	return resp, nil
}

// forwardForCoding does one indexed lookup for a single (system, code) pair,
// or — if nothing matched — falls back to the group's unmapped strategy.
func (e *FlatEngine) forwardForCoding(ctx context.Context, ref FlatConceptMapRef, req Request, sourceCode, sourceSystem string, depth int) ([]fhir.TranslateMatch, error) {
	rows, err := e.store.QueryForward(ctx, ref.PK, sourceSystem, sourceCode, req.TargetSystem)
	if err != nil {
		return nil, fmt.Errorf("flat-engine forward query: %w", err)
	}
	if len(rows) > 0 {
		matches := make([]fhir.TranslateMatch, 0, len(rows))
		for i := range rows {
			r := &rows[i]
			matches = append(matches, fhir.TranslateMatch{
				Relationship: r.Relationship,
				Concept: &fhir.Coding{
					System:  r.TargetSystem,
					Code:    r.TargetCode,
					Display: r.TargetDisplay,
				},
				OriginMap: ref.URL,
				DependsOn: toMatchDependencies(decodeDependsOn(r.DependsOnJSONB)),
				Product:   toMatchDependencies(decodeDependsOn(r.ProductJSONB)),
			})
		}
		return matches, nil
	}

	// No mapping rows — apply the group's unmapped strategy, if any.
	unmapped, err := e.store.GroupUnmapped(ctx, ref.PK, sourceSystem)
	if err != nil {
		return nil, err
	}
	if unmapped == nil {
		return nil, nil
	}
	return e.applyFlatUnmapped(ctx, unmapped, sourceCode, sourceSystem, ref.URL, req.TargetSystem, depth)
}

func (e *FlatEngine) applyFlatUnmapped(ctx context.Context, u *FlatUnmapped, sourceCode, sourceSystem, originMap, targetSystemFilter string, depth int) ([]fhir.TranslateMatch, error) {
	switch u.Mode {
	case "fixed":
		return []fhir.TranslateMatch{{
			Relationship: u.Relationship,
			Concept: &fhir.Coding{
				System:  u.GroupTarget,
				Code:    u.Code,
				Display: u.Display,
			},
			OriginMap: originMap,
		}}, nil
	case "use-source-code":
		return []fhir.TranslateMatch{{
			Relationship: u.Relationship,
			Concept: &fhir.Coding{
				System: u.GroupTarget,
				Code:   sourceCode,
			},
			OriginMap: originMap,
		}}, nil
	case "other-map":
		if depth+1 >= otherMapMaxDepth {
			// PHI: see internal/translate/engine.go for the same rationale —
			// sourceCode must not appear in error strings that reach logs.
			return nil, fmt.Errorf("other-map recursion limit exceeded (depth %d)", otherMapMaxDepth)
		}
		if u.OtherMap == "" {
			return nil, nil
		}
		next := Request{
			URL:          u.OtherMap,
			SourceCode:   sourceCode,
			SourceSystem: sourceSystem,
			TargetSystem: targetSystemFilter,
		}
		nextResp, err := e.translateWithDepth(ctx, next, depth+1)
		if err != nil {
			return nil, err
		}
		return nextResp.Matches, nil
	default:
		return nil, nil
	}
}

// inlineTranslate runs the JSONB engine for inline ConceptMaps (not persisted, so flat store cannot serve them).
func inlineTranslate(ctx context.Context, req Request) (*Response, error) {
	// Reuse the JSONB engine with a nil repo; resolveConceptMap takes the inline branch immediately.
	tmp := &Engine{repo: nil}
	return tmp.Translate(ctx, req)
}
