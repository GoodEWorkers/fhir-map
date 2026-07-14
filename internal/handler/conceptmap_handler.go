// Package handler provides HTTP handlers for FHIR operations.
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/translate"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// ConceptMapHandler is a thin wrapper around a generic ResourceHandler[T] plus
// ConceptMap-specific routes ($translate, $translate-batch, /metadata).
// All CRUD + History/Vread delegate to the embedded ResourceHandler.
type ConceptMapHandler struct {
	rh          *ResourceHandler[*conceptmap.ConceptMap]
	adapter     *ConceptMapAdapter
	engine      translate.Translator
	baseURL     string
	logger      *slog.Logger
	fhirVersion fhir.FHIRVersion
}

// HistoryReader is the surface the _history / vread routes need.
type HistoryReader interface {
	History(ctx context.Context, id string) ([]conceptmap.HistoryEntry, error)
	ReadVersion(ctx context.Context, id string, versionID int) (*conceptmap.ConceptMap, error)
}

// NewConceptMapHandler creates an R5-default ConceptMap handler.
func NewConceptMapHandler(service *conceptmap.Service, engine translate.Translator, baseURL string, logger *slog.Logger) *ConceptMapHandler {
	adapter := &ConceptMapAdapter{service: service}
	rh := NewResourceHandler[*conceptmap.ConceptMap](adapter, baseURL, logger, fhir.VersionR5)
	return &ConceptMapHandler{
		rh: rh, adapter: adapter, engine: engine,
		baseURL: baseURL, logger: logger, fhirVersion: fhir.VersionR5,
	}
}

// NewR4ConceptMapHandler is the FHIR-R4 variant.
func NewR4ConceptMapHandler(service *conceptmap.Service, engine translate.Translator, baseURL string, logger *slog.Logger) *ConceptMapHandler {
	adapter := &ConceptMapAdapter{service: service}
	rh := NewResourceHandler[*conceptmap.ConceptMap](adapter, baseURL, logger, fhir.VersionR4)
	return &ConceptMapHandler{
		rh: rh, adapter: adapter, engine: engine,
		baseURL: baseURL, logger: logger, fhirVersion: fhir.VersionR4,
	}
}

// WithHistory injects a HistoryReader so _history and vread routes serve data.
func (h *ConceptMapHandler) WithHistory(reader HistoryReader) *ConceptMapHandler {
	h.adapter.historyReader = reader
	return h
}

// RegisterRoutes mounts at the handler's natural prefix (/fhir/R4 or /fhir).
func (h *ConceptMapHandler) RegisterRoutes(mux *http.ServeMux) {
	h.registerAt(mux, h.routePrefix())
}

// RegisterRoutesAtPrefix mounts under /fhir/{version}, e.g. /fhir/R5.
func (h *ConceptMapHandler) RegisterRoutesAtPrefix(mux *http.ServeMux, version string) {
	h.registerAt(mux, "/fhir/"+version)
}

func (h *ConceptMapHandler) routePrefix() string {
	if h.fhirVersion == fhir.VersionR4 {
		return "/fhir/R4"
	}
	return "/fhir"
}

func (h *ConceptMapHandler) registerAt(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("POST "+prefix+"/ConceptMap", h.Create)
	mux.HandleFunc("GET "+prefix+"/ConceptMap/{id}", h.Read)
	mux.HandleFunc("PUT "+prefix+"/ConceptMap/{id}", h.Update)
	mux.HandleFunc("DELETE "+prefix+"/ConceptMap/{id}", h.Delete)
	mux.HandleFunc("GET "+prefix+"/ConceptMap", h.Search)
	mux.HandleFunc("GET "+prefix+"/ConceptMap/$translate", h.TranslateType)
	mux.HandleFunc("POST "+prefix+"/ConceptMap/$translate", h.TranslateType)
	mux.HandleFunc("GET "+prefix+"/ConceptMap/{id}/$translate", h.TranslateInstance)
	mux.HandleFunc("POST "+prefix+"/ConceptMap/{id}/$translate", h.TranslateInstance)
	mux.HandleFunc("POST "+prefix+"/ConceptMap/$translate-batch", h.TranslateBatch)
	mux.HandleFunc("GET "+prefix+"/metadata", h.metadataHandler)
	mux.HandleFunc("GET "+prefix+"/ConceptMap/{id}/_history", h.History)
	mux.HandleFunc("GET "+prefix+"/ConceptMap/{id}/_history/{vid}", h.Vread)
}

func (h *ConceptMapHandler) Create(w http.ResponseWriter, r *http.Request)  { h.rh.Create(w, r) }
func (h *ConceptMapHandler) Read(w http.ResponseWriter, r *http.Request)    { h.rh.Read(w, r) }
func (h *ConceptMapHandler) Update(w http.ResponseWriter, r *http.Request)  { h.rh.Update(w, r) }
func (h *ConceptMapHandler) Delete(w http.ResponseWriter, r *http.Request)  { h.rh.Delete(w, r) }
func (h *ConceptMapHandler) Search(w http.ResponseWriter, r *http.Request)  { h.rh.Search(w, r) }
func (h *ConceptMapHandler) History(w http.ResponseWriter, r *http.Request) { h.rh.History(w, r) }
func (h *ConceptMapHandler) Vread(w http.ResponseWriter, r *http.Request)   { h.rh.Vread(w, r) }
