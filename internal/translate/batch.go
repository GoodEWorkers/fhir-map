package translate

import (
	"context"
	"fmt"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// BatchProbe is one (code, system) lookup in a $translate-batch request.
type BatchProbe struct {
	SourceCode   string
	SourceSystem string
}

// BatchResponse is the engine-level result of $translate-batch: one
// per-probe response in the same order the probes arrived. Overall.Result
// is true if any probe produced a positive match.
type BatchResponse struct {
	Overall bool
	Per     []Response
}

// BatchTranslator is implemented by engines that support the bulk-lookup
// pathway. FlatEngine satisfies it via a single SQL roundtrip that
// unnest()s the probes into the indexed lookup.
//
// The JSONB engine does not implement BatchTranslator — callers that
// receive a non-batching translator should fan out N times.
type BatchTranslator interface {
	TranslateBatch(ctx context.Context, conceptMapURL, conceptMapVersion, conceptMapID string, probes []BatchProbe, targetSystemFilter string) (*BatchResponse, error)
}

// TranslateBatch on FlatEngine collapses N probes into one indexed SQL
// query. Order is preserved: per[i] corresponds to probes[i].
func (e *FlatEngine) TranslateBatch(ctx context.Context, conceptMapURL, conceptMapVersion, conceptMapID string, probes []BatchProbe, targetSystemFilter string) (*BatchResponse, error) {
	if len(probes) == 0 {
		return &BatchResponse{}, nil
	}

	ref, err := e.store.ResolveConceptMap(ctx, Request{
		URL:          conceptMapURL,
		Version:      conceptMapVersion,
		ConceptMapID: conceptMapID,
	})
	if err != nil {
		return nil, err
	}

	store, ok := e.store.(BatchFlatStore)
	if !ok {
		// Fallback: fan out to single-probe lookups via forwardForCoding which
		// also applies group.unmapped strategies (fixed, use-source-code,
		// other-map) when no direct mapping row exists.
		out := &BatchResponse{Per: make([]Response, len(probes))}
		for i, p := range probes {
			req := Request{
				URL:          conceptMapURL,
				Version:      conceptMapVersion,
				ConceptMapID: conceptMapID,
				SourceCode:   p.SourceCode,
				SourceSystem: p.SourceSystem,
				TargetSystem: targetSystemFilter,
			}
			matches, fErr := e.forwardForCoding(ctx, ref, req, p.SourceCode, p.SourceSystem, 0)
			if fErr != nil {
				// Depth-cap and other non-fatal errors: absorb into result=false
				// so the batch returns 200 overall rather than propagating a 5xx.
				// IsError=true signals to the response builder that this is an
				// engine failure, not a "no mapping" result — suppresses unmapped echo.
				out.Per[i] = Response{Result: false, Message: fErr.Error(), IsError: true}
				continue
			}
			out.Per[i] = responseFromMatches(matches)
			if out.Per[i].Result {
				out.Overall = true
			}
		}
		return out, nil
	}

	probeRows, err := store.BatchQueryForward(ctx, ref.PK, probes, targetSystemFilter)
	if err != nil {
		return nil, fmt.Errorf("batch forward query: %w", err)
	}

	out := &BatchResponse{Per: make([]Response, len(probes))}
	for i, rows := range probeRows {
		out.Per[i] = buildSingleResponse(ref, rows)
		if out.Per[i].Result {
			out.Overall = true
		}
	}
	return out, nil
}

// BatchFlatStore is the optional fast path used by FlatEngine.TranslateBatch.
// Implementations return rows grouped by probe index, in input order.
type BatchFlatStore interface {
	BatchQueryForward(ctx context.Context, conceptMapPK int64, probes []BatchProbe, targetSystemFilter string) ([][]FlatRow, error)
}

// buildSingleResponse converts the flat rows for one probe into the per-probe
// Response shape used by the engine. Mirrors the single-translate result-flag
// logic so a batch entry and a one-off $translate response are indistinguishable.
func buildSingleResponse(ref FlatConceptMapRef, rows []FlatRow) Response {
	if len(rows) == 0 {
		return responseFromMatches(nil)
	}
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
	return responseFromMatches(matches)
}

// responseFromMatches collapses a slice of TranslateMatch into the per-probe
// Response shape, including the result-flag and message conventions used by
// both the SQL batch path and the in-memory fallback path so they cannot drift.
func responseFromMatches(matches []fhir.TranslateMatch) Response {
	if len(matches) == 0 {
		return Response{
			Result:  false,
			Message: "No mapping found for the provided concept",
		}
	}
	result := false
	for _, m := range matches {
		if m.Relationship != "not-related-to" {
			result = true
			break
		}
	}
	resp := Response{Result: result, Matches: matches}
	if !result {
		resp.Message = "Only negative matches found"
	}
	return resp
}
