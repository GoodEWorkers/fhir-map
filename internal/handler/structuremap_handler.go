package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/internal/transform"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// StructureMapHandler serves the FHIR StructureMap surface. CRUD + _history +
// vread delegate to the embedded generic ResourceHandler[T]; $transform is
// handled by TransformStub in structuremap_operations.go.
type StructureMapHandler struct {
	rh           *ResourceHandler[*structuremap.StructureMap]
	adapter      *StructureMapAdapter
	transformEng *transform.Engine
	baseURL      string
	logger       *slog.Logger
	fhirVersion  fhir.FHIRVersion
	// transformTimeout caps a single $transform execution. Zero means "no
	// engine-level deadline" (the request is then bounded only by the HTTP
	// WriteTimeout); set via WithTransformTimeout from SERVER_TRANSFORM_TIMEOUT.
	transformTimeout time.Duration
	// outputValidator, when non-nil, validates the $transform result before it
	// is returned (P0.2 output-validation gate). nil = disabled (default;
	// byte-identical output). See WithTransformOutputValidation.
	outputValidator OutputValidator
	// outputValidateStrict selects the gate's response when the validator finds
	// issues: true = reject as 422 (do not emit the output); false = emit but
	// flag (Warning response header + server log). Ignored when outputValidator
	// is nil.
	outputValidateStrict bool
}

// StructureMapHistoryReader is the repository subset the _history/vread routes need.
type StructureMapHistoryReader interface {
	History(ctx context.Context, id string) ([]structuremap.HistoryEntry, error)
	ReadVersion(ctx context.Context, id string, versionID int) (*structuremap.StructureMap, error)
}

// NewStructureMapHandler creates an R5-default StructureMap handler.
func NewStructureMapHandler(service *structuremap.Service, baseURL string, logger *slog.Logger) *StructureMapHandler {
	adapter := &StructureMapAdapter{service: service}
	rh := NewResourceHandler[*structuremap.StructureMap](adapter, baseURL, logger, fhir.VersionR5)
	return &StructureMapHandler{rh: rh, adapter: adapter, baseURL: baseURL, logger: logger, fhirVersion: fhir.VersionR5}
}

// NewR4StructureMapHandler is the FHIR-R4 variant.
func NewR4StructureMapHandler(service *structuremap.Service, baseURL string, logger *slog.Logger) *StructureMapHandler {
	adapter := &StructureMapAdapter{service: service}
	rh := NewResourceHandler[*structuremap.StructureMap](adapter, baseURL, logger, fhir.VersionR4)
	return &StructureMapHandler{rh: rh, adapter: adapter, baseURL: baseURL, logger: logger, fhirVersion: fhir.VersionR4}
}

// WithHistory enables _history and vread by injecting the history reader.
func (h *StructureMapHandler) WithHistory(r StructureMapHistoryReader) *StructureMapHandler {
	h.adapter.historyReader = r
	return h
}

// WithTransformEngine enables the $transform endpoint.
func (h *StructureMapHandler) WithTransformEngine(eng *transform.Engine) *StructureMapHandler {
	h.transformEng = eng
	return h
}

// WithTransformTimeout sets the per-request $transform execution budget. A
// non-positive duration leaves the engine uncapped (HTTP WriteTimeout only).
func (h *StructureMapHandler) WithTransformTimeout(d time.Duration) *StructureMapHandler {
	h.transformTimeout = d
	return h
}

// WithTransformOutputValidation enables the $transform output-validation gate
// (P0.2). With strict=true, output that fails validation is rejected as a 422
// OperationOutcome and not emitted; with strict=false (lenient) the output is
// still returned but flagged via a `Warning` response header and a server-side
// log. A nil validator disables the gate (the default), keeping output
// byte-identical to today.
func (h *StructureMapHandler) WithTransformOutputValidation(v OutputValidator, strict bool) *StructureMapHandler {
	h.outputValidator = v
	h.outputValidateStrict = strict
	return h
}

// RegisterRoutes mounts at the handler's natural prefix (/fhir/R4 or /fhir).
func (h *StructureMapHandler) RegisterRoutes(mux *http.ServeMux) {
	h.registerAt(mux, h.routePrefix())
}

// RegisterRoutesAtPrefix mounts under /fhir/{version}.
func (h *StructureMapHandler) RegisterRoutesAtPrefix(mux *http.ServeMux, version string) {
	h.registerAt(mux, "/fhir/"+version)
}

func (h *StructureMapHandler) routePrefix() string {
	if h.fhirVersion == fhir.VersionR4 {
		return "/fhir/R4"
	}
	return "/fhir"
}

func (h *StructureMapHandler) registerAt(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("POST "+prefix+"/StructureMap", h.Create)
	mux.HandleFunc("GET "+prefix+"/StructureMap/{id}", h.Read)
	mux.HandleFunc("PUT "+prefix+"/StructureMap/{id}", h.Update)
	mux.HandleFunc("DELETE "+prefix+"/StructureMap/{id}", h.Delete)
	mux.HandleFunc("GET "+prefix+"/StructureMap", h.Search)
	mux.HandleFunc("GET "+prefix+"/StructureMap/{id}/_history", h.History)
	mux.HandleFunc("GET "+prefix+"/StructureMap/{id}/_history/{vid}", h.Vread)
	mux.HandleFunc("POST "+prefix+"/StructureMap/$transform", h.TransformStub)
	mux.HandleFunc("POST "+prefix+"/StructureMap/{id}/$transform", h.TransformStub)
	// System-level $transform alias — HAPI exposes the operation at the FHIR base URL.
	mux.HandleFunc("POST "+prefix+"/$transform", h.TransformStub)
}

func (h *StructureMapHandler) Create(w http.ResponseWriter, r *http.Request)  { h.rh.Create(w, r) }
func (h *StructureMapHandler) Read(w http.ResponseWriter, r *http.Request)    { h.rh.Read(w, r) }
func (h *StructureMapHandler) Update(w http.ResponseWriter, r *http.Request)  { h.rh.Update(w, r) }
func (h *StructureMapHandler) Delete(w http.ResponseWriter, r *http.Request)  { h.rh.Delete(w, r) }
func (h *StructureMapHandler) Search(w http.ResponseWriter, r *http.Request)  { h.rh.Search(w, r) }
func (h *StructureMapHandler) History(w http.ResponseWriter, r *http.Request) { h.rh.History(w, r) }
func (h *StructureMapHandler) Vread(w http.ResponseWriter, r *http.Request)   { h.rh.Vread(w, r) }
