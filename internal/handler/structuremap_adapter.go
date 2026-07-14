package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// StructureMapAdapter implements Adapter[*structuremap.StructureMap].
type StructureMapAdapter struct {
	service       *structuremap.Service
	historyReader StructureMapHistoryReader
}

func (a *StructureMapAdapter) ResourceName() string { return "StructureMap" }

func (a *StructureMapAdapter) New() *structuremap.StructureMap { return &structuremap.StructureMap{} }

func (a *StructureMapAdapter) Create(ctx context.Context, sm *structuremap.StructureMap, mode ValidationMode) (*structuremap.StructureMap, error) {
	return a.service.CreateWithMode(ctx, sm, domainSMMode(mode))
}

func (a *StructureMapAdapter) Read(ctx context.Context, id string) (*structuremap.StructureMap, error) {
	return a.service.Read(ctx, id)
}

func (a *StructureMapAdapter) Update(ctx context.Context, id string, sm *structuremap.StructureMap, mode ValidationMode) (*structuremap.StructureMap, error) {
	return a.service.UpdateWithMode(ctx, id, sm, domainSMMode(mode))
}

func (a *StructureMapAdapter) Delete(ctx context.Context, id string) error {
	return a.service.Delete(ctx, id)
}

func (a *StructureMapAdapter) Search(ctx context.Context, q url.Values) ([]*structuremap.StructureMap, int, error) {
	params := parseSMSearchParams(q)
	result, err := a.service.Search(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*structuremap.StructureMap, len(result.StructureMaps))
	for i := range result.StructureMaps {
		out[i] = &result.StructureMaps[i]
	}
	return out, result.Total, nil
}

func (a *StructureMapAdapter) FindByURL(ctx context.Context, rawURL, version string) (*structuremap.StructureMap, error) {
	return a.service.FindByURL(ctx, rawURL, version)
}

func (a *StructureMapAdapter) HasHistory() bool { return a.historyReader != nil }

func (a *StructureMapAdapter) History(ctx context.Context, id string) ([]HistoryEntry[*structuremap.StructureMap], error) {
	entries, err := a.historyReader.History(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry[*structuremap.StructureMap], len(entries))
	for i, e := range entries {
		out[i] = HistoryEntry[*structuremap.StructureMap]{
			VersionID:  e.VersionID,
			Operation:  e.Operation,
			OccurredAt: e.OccurredAt,
			Resource:   e.Resource,
		}
	}
	return out, nil
}

func (a *StructureMapAdapter) MapServiceError(err error) (statusCode int, issueCode, message string) {
	switch {
	case errors.Is(err, structuremap.ErrNotFound):
		return http.StatusNotFound, "not-found", "Resource not found"
	case errors.Is(err, structuremap.ErrGone):
		return http.StatusGone, "gone", "Resource has been deleted"
	case errors.Is(err, structuremap.ErrUnprocessable):
		return http.StatusUnprocessableEntity, "invalid", "The resource failed validation. Correct the resource and resubmit."
	case errors.Is(err, structuremap.ErrInvalidInput):
		return http.StatusBadRequest, "invalid", err.Error()
	case errors.Is(err, structuremap.ErrConflict):
		return http.StatusConflict, "conflict", err.Error()
	default:
		return http.StatusInternalServerError, "exception", "An internal error occurred"
	}
}

func (a *StructureMapAdapter) ProjectForWire(sm *structuremap.StructureMap, version fhir.FHIRVersion) *structuremap.StructureMap {
	if version == fhir.VersionR4 {
		return structuremap.ProjectToR4(sm)
	}
	return sm
}

func (a *StructureMapAdapter) CanonicaliseFromR4(sm *structuremap.StructureMap) {
	// Strip R4-only contextType; repopulated on R4 egress, never on R5.
	if sm == nil {
		return
	}
	stripContextType(sm.Group)
}

func stripContextType(groups []structuremap.Group) {
	for gi := range groups {
		stripRules(groups[gi].Rule)
	}
}

func stripRules(rules []structuremap.Rule) {
	for ri := range rules {
		r := &rules[ri]
		for ti := range r.Target {
			r.Target[ti].ContextType = ""
		}
		if len(r.Rule) > 0 {
			stripRules(r.Rule)
		}
	}
}

func (a *StructureMapAdapter) R5OnlyFields() []string {
	// Same R5-only fields as StructureMap Create/Update strictness check.
	return []string{"copyrightLabel", "versionAlgorithmString", "versionAlgorithmCoding"}
}

func domainSMMode(mode ValidationMode) structuremap.ValidationMode {
	if mode == ModeLenient {
		return structuremap.ModeLenient
	}
	return structuremap.ModeStrict
}

func parseSMSearchParams(q url.Values) structuremap.SearchParams {
	params := structuremap.SearchParams{
		ID:          q.Get("_id"),
		URL:         q.Get("url"),
		Version:     q.Get("version"),
		Name:        q.Get("name"),
		Title:       q.Get("title"),
		Status:      q.Get("status"),
		Publisher:   q.Get("publisher"),
		Description: q.Get("description"),
		Date:        q.Get("date"),
		Identifier:  q.Get("identifier"),
	}
	if c := q.Get("_count"); c != "" {
		if v, err := strconv.Atoi(c); err == nil {
			params.Count = v
		}
	}
	if o := q.Get("_offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil {
			params.Offset = v
		}
	}
	return params
}
