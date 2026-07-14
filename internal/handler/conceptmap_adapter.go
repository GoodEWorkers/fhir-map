package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

var errUnsupportedSearchParam = errors.New("unsupported search parameter")

// ConceptMapAdapter implements Adapter[*conceptmap.ConceptMap].
type ConceptMapAdapter struct {
	service       *conceptmap.Service
	historyReader HistoryReader
}

func (a *ConceptMapAdapter) ResourceName() string { return "ConceptMap" }

func (a *ConceptMapAdapter) New() *conceptmap.ConceptMap { return &conceptmap.ConceptMap{} }

func (a *ConceptMapAdapter) Create(ctx context.Context, cm *conceptmap.ConceptMap, mode ValidationMode) (*conceptmap.ConceptMap, error) {
	return a.service.CreateWithMode(ctx, cm, domainCMMode(mode))
}

func (a *ConceptMapAdapter) Read(ctx context.Context, id string) (*conceptmap.ConceptMap, error) {
	return a.service.Read(ctx, id)
}

func (a *ConceptMapAdapter) Update(ctx context.Context, id string, cm *conceptmap.ConceptMap, mode ValidationMode) (*conceptmap.ConceptMap, error) {
	return a.service.UpdateWithMode(ctx, id, cm, domainCMMode(mode))
}

func (a *ConceptMapAdapter) Delete(ctx context.Context, id string) error {
	return a.service.Delete(ctx, id)
}

func (a *ConceptMapAdapter) Search(ctx context.Context, q url.Values) ([]*conceptmap.ConceptMap, int, error) {
	if err := validateCMSearchParams(q); err != nil {
		return nil, 0, err
	}
	params := parseCMSearchParams(q)
	result, err := a.service.Search(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*conceptmap.ConceptMap, len(result.ConceptMaps))
	for i := range result.ConceptMaps {
		out[i] = &result.ConceptMaps[i]
	}
	return out, result.Total, nil
}

func (a *ConceptMapAdapter) FindByURL(ctx context.Context, rawURL, version string) (*conceptmap.ConceptMap, error) {
	return a.service.FindByURL(ctx, rawURL, version)
}

func (a *ConceptMapAdapter) HasHistory() bool { return a.historyReader != nil }

func (a *ConceptMapAdapter) History(ctx context.Context, id string) ([]HistoryEntry[*conceptmap.ConceptMap], error) {
	entries, err := a.historyReader.History(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry[*conceptmap.ConceptMap], len(entries))
	for i, e := range entries {
		out[i] = HistoryEntry[*conceptmap.ConceptMap]{
			VersionID:  e.VersionID,
			Operation:  e.Operation,
			OccurredAt: e.OccurredAt,
			Resource:   e.Resource,
		}
	}
	return out, nil
}

func (a *ConceptMapAdapter) MapServiceError(err error) (statusCode int, issueCode, message string) {
	switch {
	case errors.Is(err, errUnsupportedSearchParam):
		return http.StatusBadRequest, "not-supported", "unsupported search parameter"
	case errors.Is(err, conceptmap.ErrNotFound):
		return http.StatusNotFound, "not-found", "Resource not found"
	case errors.Is(err, conceptmap.ErrGone):
		return http.StatusGone, "gone", "Resource has been deleted"
	case errors.Is(err, conceptmap.ErrUnprocessable):
		return http.StatusUnprocessableEntity, "invalid", "The resource failed validation. Correct the resource and resubmit."
	case errors.Is(err, conceptmap.ErrInvalidInput):
		return http.StatusBadRequest, "invalid", err.Error()
	case errors.Is(err, conceptmap.ErrConflict):
		return http.StatusConflict, "conflict", err.Error()
	default:
		return http.StatusInternalServerError, "exception", "An internal error occurred"
	}
}

func (a *ConceptMapAdapter) ProjectForWire(cm *conceptmap.ConceptMap, version fhir.FHIRVersion) *conceptmap.ConceptMap {
	if version == fhir.VersionR4 {
		return conceptmap.ProjectToR4(cm)
	}
	return cm
}

func (a *ConceptMapAdapter) CanonicaliseFromR4(cm *conceptmap.ConceptMap) {
	conceptmap.CanonicaliseFromR4(cm)
}

func (a *ConceptMapAdapter) R5OnlyFields() []string {
	// Verified against the FHIR R5 ConceptMap schema: absent from FHIR R4 4.0.1.
	return []string{"versionAlgorithmString", "versionAlgorithmCoding", "copyrightLabel"}
}

// domainCMMode converts the package-level ValidationMode to conceptmap's domain mode.
func domainCMMode(mode ValidationMode) conceptmap.ValidationMode {
	if mode == ModeLenient {
		return conceptmap.ModeLenient
	}
	return conceptmap.ModeStrict
}

// validateCMSearchParams rejects unrecognised FHIR search parameters.
// Modifier suffixes (e.g. status:exact) are stripped before lookup.
func validateCMSearchParams(q url.Values) error {
	for key := range q {
		base := key
		if i := strings.IndexByte(base, ':'); i >= 0 {
			base = base[:i]
		}
		if _, ok := knownSearchParams[base]; !ok {
			return errUnsupportedSearchParam
		}
	}
	return nil
}

// parseCMSearchParams converts url.Values to conceptmap.SearchParams.
func parseCMSearchParams(q url.Values) conceptmap.SearchParams {
	params := conceptmap.SearchParams{
		ID:                q.Get("_id"),
		URL:               q.Get("url"),
		Version:           q.Get("version"),
		Name:              q.Get("name"),
		Title:             q.Get("title"),
		Status:            q.Get("status"),
		Publisher:         q.Get("publisher"),
		Description:       q.Get("description"),
		Date:              q.Get("date"),
		Identifier:        q.Get("identifier"),
		SourceCode:        q.Get("source-code"),
		TargetCode:        q.Get("target-code"),
		SourceGroupSystem: firstNonEmpty(q.Get("source-system"), q.Get("source-group-system")),
		TargetGroupSystem: firstNonEmpty(q.Get("target-system"), q.Get("target-group-system")),
		SourceScope:       q.Get("source-scope"),
		SourceScopeURI:    q.Get("source-scope-uri"),
		TargetScope:       q.Get("target-scope"),
		TargetScopeURI:    q.Get("target-scope-uri"),
	}
	if count := q.Get("_count"); count != "" {
		if c, err := strconv.Atoi(count); err == nil {
			params.Count = c
		}
	}
	if offset := q.Get("_offset"); offset != "" {
		if o, err := strconv.Atoi(offset); err == nil {
			params.Offset = o
		}
	}
	if params.Offset < 0 {
		params.Offset = 0
	}
	const searchCountCap = 1000
	if params.Count <= 0 || params.Count > searchCountCap {
		params.Count = searchCountCap
	}
	return params
}
