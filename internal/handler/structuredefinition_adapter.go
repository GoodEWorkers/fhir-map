package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/goodeworkers/fhir-map/internal/domain/structuredefinition"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// StructureDefinitionAdapter implements Adapter[*structuredefinition.StructureDefinition].
type StructureDefinitionAdapter struct {
	service       *structuredefinition.Service
	historyReader StructureDefinitionHistoryReader
}

func (a *StructureDefinitionAdapter) ResourceName() string { return "StructureDefinition" }

func (a *StructureDefinitionAdapter) New() *structuredefinition.StructureDefinition {
	return &structuredefinition.StructureDefinition{}
}

func (a *StructureDefinitionAdapter) Create(ctx context.Context, sd *structuredefinition.StructureDefinition, _ ValidationMode) (*structuredefinition.StructureDefinition, error) {
	// StructureDefinition always uses strict validation, ignoring the passed mode.
	return a.service.CreateWithMode(ctx, sd, structuredefinition.ModeStrict)
}

func (a *StructureDefinitionAdapter) Read(ctx context.Context, id string) (*structuredefinition.StructureDefinition, error) {
	return a.service.Read(ctx, id)
}

func (a *StructureDefinitionAdapter) Update(ctx context.Context, id string, sd *structuredefinition.StructureDefinition, _ ValidationMode) (*structuredefinition.StructureDefinition, error) {
	return a.service.UpdateWithMode(ctx, id, sd, structuredefinition.ModeStrict)
}

func (a *StructureDefinitionAdapter) Delete(ctx context.Context, id string) error {
	return a.service.Delete(ctx, id)
}

func (a *StructureDefinitionAdapter) Search(ctx context.Context, q url.Values) ([]*structuredefinition.StructureDefinition, int, error) {
	params := parseSDSearchParams(q)
	result, err := a.service.Search(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*structuredefinition.StructureDefinition, len(result.StructureDefinitions))
	for i := range result.StructureDefinitions {
		out[i] = &result.StructureDefinitions[i]
	}
	return out, result.Total, nil
}

func (a *StructureDefinitionAdapter) FindByURL(ctx context.Context, rawURL, version string) (*structuredefinition.StructureDefinition, error) {
	return a.service.FindByURL(ctx, rawURL, version)
}

func (a *StructureDefinitionAdapter) HasHistory() bool { return a.historyReader != nil }

func (a *StructureDefinitionAdapter) History(ctx context.Context, id string) ([]HistoryEntry[*structuredefinition.StructureDefinition], error) {
	entries, err := a.historyReader.History(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry[*structuredefinition.StructureDefinition], len(entries))
	for i, e := range entries {
		out[i] = HistoryEntry[*structuredefinition.StructureDefinition]{
			VersionID:  e.VersionID,
			Operation:  e.Operation,
			OccurredAt: e.OccurredAt,
			Resource:   e.Resource,
		}
	}
	return out, nil
}

func (a *StructureDefinitionAdapter) MapServiceError(err error) (statusCode int, issueCode, message string) {
	switch {
	case errors.Is(err, structuredefinition.ErrNotFound):
		return http.StatusNotFound, "not-found", "Resource not found"
	case errors.Is(err, structuredefinition.ErrGone):
		return http.StatusGone, "gone", "Resource has been deleted"
	case errors.Is(err, structuredefinition.ErrUnprocessable):
		return http.StatusUnprocessableEntity, "invalid", "The resource failed validation. Correct the resource and resubmit."
	case errors.Is(err, structuredefinition.ErrInvalidInput):
		return http.StatusBadRequest, "invalid", err.Error()
	case errors.Is(err, structuredefinition.ErrConflict):
		return http.StatusConflict, "conflict", err.Error()
	default:
		return http.StatusInternalServerError, "exception", "An internal error occurred"
	}
}

func (a *StructureDefinitionAdapter) ProjectForWire(sd *structuredefinition.StructureDefinition, version fhir.FHIRVersion) *structuredefinition.StructureDefinition {
	if version == fhir.VersionR4 {
		return structuredefinition.ProjectToR4(sd)
	}
	return sd
}

func (a *StructureDefinitionAdapter) CanonicaliseFromR4(_ *structuredefinition.StructureDefinition) {
	// R4 and R5 shapes are identical for the exposed fields.
}

func (a *StructureDefinitionAdapter) R5OnlyFields() []string {
	// No R4↔R5 schema difference for the fields we expose; ingress scan disabled.
	return nil
}

// parseSDSearchParams converts url.Values to structuredefinition.SearchParams.
func parseSDSearchParams(q url.Values) structuredefinition.SearchParams {
	params := structuredefinition.SearchParams{
		ID:      q.Get("_id"),
		URL:     q.Get("url"),
		Version: q.Get("version"),
		Name:    q.Get("name"),
		Status:  q.Get("status"),
		Kind:    q.Get("kind"),
		Type:    q.Get("type"),
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
