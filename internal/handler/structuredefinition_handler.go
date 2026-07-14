package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/goodeworkers/fhir-map/internal/domain/structuredefinition"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// StructureDefinitionHandler serves FHIR StructureDefinition CRUD + Search +
// _history + vread on either the R4 or R5 URL tree. All routes delegate to
// the embedded generic ResourceHandler[T].
type StructureDefinitionHandler struct {
	rh          *ResourceHandler[*structuredefinition.StructureDefinition]
	adapter     *StructureDefinitionAdapter
	baseURL     string
	logger      *slog.Logger
	fhirVersion fhir.FHIRVersion
}

// StructureDefinitionHistoryReader is the repository subset the _history/vread routes need.
type StructureDefinitionHistoryReader interface {
	History(ctx context.Context, id string) ([]structuredefinition.HistoryEntry, error)
	ReadVersion(ctx context.Context, id string, versionID int) (*structuredefinition.StructureDefinition, error)
}

// NewStructureDefinitionHandler creates an R5-default handler.
func NewStructureDefinitionHandler(service *structuredefinition.Service, baseURL string, logger *slog.Logger) *StructureDefinitionHandler {
	adapter := &StructureDefinitionAdapter{service: service}
	rh := NewResourceHandler[*structuredefinition.StructureDefinition](adapter, baseURL, logger, fhir.VersionR5)
	return &StructureDefinitionHandler{rh: rh, adapter: adapter, baseURL: baseURL, logger: logger, fhirVersion: fhir.VersionR5}
}

// NewR4StructureDefinitionHandler is the R4-tree variant.
func NewR4StructureDefinitionHandler(service *structuredefinition.Service, baseURL string, logger *slog.Logger) *StructureDefinitionHandler {
	adapter := &StructureDefinitionAdapter{service: service}
	rh := NewResourceHandler[*structuredefinition.StructureDefinition](adapter, baseURL, logger, fhir.VersionR4)
	return &StructureDefinitionHandler{rh: rh, adapter: adapter, baseURL: baseURL, logger: logger, fhirVersion: fhir.VersionR4}
}

// WithHistory enables _history and vread by injecting the history reader.
func (h *StructureDefinitionHandler) WithHistory(r StructureDefinitionHistoryReader) *StructureDefinitionHandler {
	h.adapter.historyReader = r
	return h
}

// RegisterRoutes mounts at the handler's natural prefix (/fhir/R4 or /fhir).
func (h *StructureDefinitionHandler) RegisterRoutes(mux *http.ServeMux) {
	h.registerAt(mux, h.routePrefix())
}

// RegisterRoutesAtPrefix mounts under /fhir/{version}.
func (h *StructureDefinitionHandler) RegisterRoutesAtPrefix(mux *http.ServeMux, version string) {
	h.registerAt(mux, "/fhir/"+version)
}

func (h *StructureDefinitionHandler) routePrefix() string {
	if h.fhirVersion == fhir.VersionR4 {
		return "/fhir/R4"
	}
	return "/fhir"
}

func (h *StructureDefinitionHandler) registerAt(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("POST "+prefix+"/StructureDefinition", h.Create)
	mux.HandleFunc("GET "+prefix+"/StructureDefinition/{id}", h.Read)
	mux.HandleFunc("PUT "+prefix+"/StructureDefinition/{id}", h.Update)
	mux.HandleFunc("DELETE "+prefix+"/StructureDefinition/{id}", h.Delete)
	mux.HandleFunc("GET "+prefix+"/StructureDefinition", h.Search)
	mux.HandleFunc("GET "+prefix+"/StructureDefinition/{id}/_history", h.History)
	mux.HandleFunc("GET "+prefix+"/StructureDefinition/{id}/_history/{vid}", h.Vread)
}
func (h *StructureDefinitionHandler) Create(w http.ResponseWriter, r *http.Request) {
	h.rh.Create(w, r)
}
func (h *StructureDefinitionHandler) Read(w http.ResponseWriter, r *http.Request) { h.rh.Read(w, r) }
func (h *StructureDefinitionHandler) Update(w http.ResponseWriter, r *http.Request) {
	h.rh.Update(w, r)
}
func (h *StructureDefinitionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	h.rh.Delete(w, r)
}
func (h *StructureDefinitionHandler) Search(w http.ResponseWriter, r *http.Request) {
	h.rh.Search(w, r)
}
func (h *StructureDefinitionHandler) History(w http.ResponseWriter, r *http.Request) {
	h.rh.History(w, r)
}
func (h *StructureDefinitionHandler) Vread(w http.ResponseWriter, r *http.Request) { h.rh.Vread(w, r) }
