package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// ResourceHandler[T Resource] implements the FHIR CRUD + _history + vread
// surface for any resource type that satisfies the Resource constraint via an
// Adapter[T]. It is the single canonical shape for Create, Read, Update,
// Delete, Search, History, and Vread; per-resource quirks (translate, transform,
// search-param shapes, R4↔R5 vocabulary) live in the Adapter implementation.
type ResourceHandler[T Resource] struct {
	adapter     Adapter[T]
	baseURL     string
	logger      *slog.Logger
	fhirVersion fhir.FHIRVersion
}

// NewResourceHandler constructs a generic handler backed by the given adapter.
func NewResourceHandler[T Resource](adapter Adapter[T], baseURL string, logger *slog.Logger, version fhir.FHIRVersion) *ResourceHandler[T] {
	return &ResourceHandler[T]{
		adapter:     adapter,
		baseURL:     baseURL,
		logger:      logger,
		fhirVersion: version,
	}
}

func (h *ResourceHandler[T]) routePrefix() string {
	if h.fhirVersion == fhir.VersionR4 {
		return "/fhir/R4"
	}
	return "/fhir"
}

func (h *ResourceHandler[T]) Create(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		if IsBodyTooLarge(err) {
			WriteBodyTooLargeResponse(w)
			return
		}
		h.logger.Error("body read failed", "error", err)
		writeOperationOutcome(w, http.StatusBadRequest, "invalid", "Cannot read request body")
		return
	}

	if h.fhirVersion == fhir.VersionR4 {
		if offending := firstUnknownTopLevelField(rawBody, h.adapter.R5OnlyFields()); offending != "" {
			writeOperationOutcome(w, http.StatusBadRequest, "not-supported",
				"field `"+offending+"` is R5-only and not part of the FHIR R4 "+h.adapter.ResourceName()+" resource")
			return
		}
	}

	t := h.adapter.New()
	if unmarshalErr := json.Unmarshal(rawBody, t); unmarshalErr != nil {
		writeOperationOutcome(w, http.StatusBadRequest, "invalid", "Invalid JSON body: "+unmarshalErr.Error())
		return
	}

	if h.fhirVersion == fhir.VersionR4 {
		h.adapter.CanonicaliseFromR4(t)
	}

	result, err := h.adapter.Create(r.Context(), t, parseValidationMode(r))
	if err != nil {
		h.handleServiceError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/fhir+json")
	w.Header().Set("Location", fmt.Sprintf("%s%s/%s/%s", h.baseURL, h.routePrefix(), h.adapter.ResourceName(), result.GetID()))
	if m := result.GetMeta(); m != nil {
		w.Header().Set("ETag", formatWeakETag(m.VersionID))
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(h.adapter.ProjectForWire(result, h.fhirVersion))
}

func (h *ResourceHandler[T]) Read(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	result, err := h.adapter.Read(r.Context(), id)
	if err != nil {
		h.handleServiceError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/fhir+json")
	if m := result.GetMeta(); m != nil {
		w.Header().Set("ETag", formatWeakETag(m.VersionID))
	}
	_ = json.NewEncoder(w).Encode(h.adapter.ProjectForWire(result, h.fhirVersion))
}

func (h *ResourceHandler[T]) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		if IsBodyTooLarge(err) {
			WriteBodyTooLargeResponse(w)
			return
		}
		h.logger.Error("body read failed", "error", err)
		writeOperationOutcome(w, http.StatusBadRequest, "invalid", "Cannot read request body")
		return
	}

	if h.fhirVersion == fhir.VersionR4 {
		if offending := firstUnknownTopLevelField(rawBody, h.adapter.R5OnlyFields()); offending != "" {
			writeOperationOutcome(w, http.StatusBadRequest, "not-supported",
				"field `"+offending+"` is R5-only and not part of the FHIR R4 "+h.adapter.ResourceName()+" resource")
			return
		}
	}

	t := h.adapter.New()
	if unmarshalErr := json.Unmarshal(rawBody, t); unmarshalErr != nil {
		writeOperationOutcome(w, http.StatusBadRequest, "invalid", "Invalid JSON body: "+unmarshalErr.Error())
		return
	}

	if t.GetID() != "" && t.GetID() != id {
		writeOperationOutcome(w, http.StatusBadRequest, "invalid", "Resource ID in body does not match URL")
		return
	}

	if h.fhirVersion == fhir.VersionR4 {
		h.adapter.CanonicaliseFromR4(t)
	}

	// Parse If-Match strictly per RFC 7232 §2.3. Malformed headers (missing
	// quotes, unterminated quotes, empty quoted value) are rejected at 400
	// rather than silently forwarded to the repo where they'd surface as
	// 409 conflicts. Parser-valid versionIds are treated as opaque — a
	// version mismatch surfaces later as 412 (precondition failed).
	ifMatchSupplied := false
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" {
		ifMatchSupplied = true
		versionID, verr := validateAndParseIfMatchETag(ifMatch)
		if verr != nil {
			writeOperationOutcome(w, http.StatusBadRequest, "invalid", verr.Error())
			return
		}
		m := t.GetMeta()
		if m == nil {
			m = &fhir.Meta{}
			t.SetMeta(m)
		}
		m.VersionID = versionID
	}

	mode := parseValidationMode(r)
	result, err := h.adapter.Update(r.Context(), id, t, mode)
	if err != nil {
		status, _, _ := h.adapter.MapServiceError(err)
		// When a precondition (If-Match) was supplied and the adapter reports a
		// conflict (stale version) or a missing resource, the correct HTTP
		// response is 412 Precondition Failed — not 409 (conflict) or 201
		// (upsert). RFC 7232 §4.2 + FHIR §http.
		if ifMatchSupplied && (status == http.StatusConflict || status == http.StatusNotFound) {
			writeOperationOutcome(w, http.StatusPreconditionFailed, "conflict",
				"If-Match precondition failed")
			return
		}
		if status == http.StatusNotFound {
			// FHIR upsert ("update as create"): PUT /Type/{id} with an unknown id
			// and no If-Match creates the resource.
			t.SetID(id)
			created, cerr := h.adapter.Create(r.Context(), t, mode)
			if cerr != nil {
				h.handleServiceError(w, cerr)
				return
			}
			w.Header().Set("Content-Type", "application/fhir+json")
			w.Header().Set("Location", fmt.Sprintf("%s%s/%s/%s", h.baseURL, h.routePrefix(), h.adapter.ResourceName(), created.GetID()))
			if m := created.GetMeta(); m != nil {
				w.Header().Set("ETag", formatWeakETag(m.VersionID))
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(h.adapter.ProjectForWire(created, h.fhirVersion))
			return
		}
		h.handleServiceError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/fhir+json")
	if m := result.GetMeta(); m != nil {
		w.Header().Set("ETag", formatWeakETag(m.VersionID))
	}
	_ = json.NewEncoder(w).Encode(h.adapter.ProjectForWire(result, h.fhirVersion))
}

func (h *ResourceHandler[T]) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := h.adapter.Delete(r.Context(), id); err != nil {
		// FHIR DELETE is idempotent — deleting an already-deleted resource returns
		// 204, not 410.
		if status, _, _ := h.adapter.MapServiceError(err); status == http.StatusGone {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.handleServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *ResourceHandler[T]) Search(w http.ResponseWriter, r *http.Request) {
	if status, msg, ok := validateCountParam(r.URL.Query().Get("_count")); !ok {
		writeOperationOutcome(w, status, "invalid", msg)
		return
	}
	results, total, err := h.adapter.Search(r.Context(), r.URL.Query())
	if err != nil {
		h.handleServiceError(w, err)
		return
	}

	var entries []fhir.BundleEntry
	for _, t := range results {
		projected := h.adapter.ProjectForWire(t, h.fhirVersion)
		raw, mErr := json.Marshal(projected)
		if mErr != nil {
			writeOperationOutcome(w, http.StatusInternalServerError, "exception", "Failed to serialize resource")
			return
		}
		entries = append(entries, fhir.BundleEntry{
			FullURL:  fmt.Sprintf("%s%s/%s/%s", h.baseURL, h.routePrefix(), h.adapter.ResourceName(), t.GetID()),
			Resource: raw,
			Search:   &fhir.BundleSearch{Mode: "match"},
		})
	}

	selfLink := fmt.Sprintf("%s%s/%s", h.baseURL, h.routePrefix(), h.adapter.ResourceName())
	if r.URL.RawQuery != "" {
		selfLink += "?" + r.URL.RawQuery
	}
	bundle := newSearchBundle(h.baseURL, total, entries, selfLink)
	w.Header().Set("Content-Type", "application/fhir+json")
	_ = json.NewEncoder(w).Encode(bundle)
}

func (h *ResourceHandler[T]) History(w http.ResponseWriter, r *http.Request) {
	if !h.adapter.HasHistory() {
		writeOperationOutcome(w, http.StatusNotImplemented, "not-supported",
			"history is not available on this server's repository backend")
		return
	}
	id := r.PathValue("id")

	entries, err := h.adapter.History(r.Context(), id)
	if err != nil {
		h.handleServiceError(w, err)
		return
	}

	views := make([]historyEntryView, 0, len(entries))
	for _, e := range entries {
		v := historyEntryView{
			VersionID:  e.VersionID,
			Operation:  e.Operation,
			OccurredAt: e.OccurredAt,
		}
		if e.Operation != "delete" && !isNilResource(e.Resource) {
			projected := h.adapter.ProjectForWire(e.Resource, h.fhirVersion)
			if raw, mErr := json.Marshal(projected); mErr == nil {
				v.ResourceJSON = raw
			}
		}
		views = append(views, v)
	}

	bundle := newHistoryBundle(h.baseURL, h.routePrefix(), h.adapter.ResourceName(), id, views)
	w.Header().Set("Content-Type", "application/fhir+json")
	_ = json.NewEncoder(w).Encode(bundle)
}

func (h *ResourceHandler[T]) Vread(w http.ResponseWriter, r *http.Request) {
	if !h.adapter.HasHistory() {
		writeOperationOutcome(w, http.StatusNotImplemented, "not-supported",
			"vread is not available on this server's repository backend")
		return
	}
	id := r.PathValue("id")
	vidStr := r.PathValue("vid")
	vid, err := strconv.Atoi(vidStr)
	if err != nil || vid < 1 {
		writeOperationOutcome(w, http.StatusBadRequest, "invalid", "Version must be a positive integer")
		return
	}

	// Walk history to find the requested version. This allows us to detect
	// delete-op versions (→ 410) uniformly across all resource types.
	entries, herr := h.adapter.History(r.Context(), id)
	if herr != nil {
		h.handleServiceError(w, herr)
		return
	}
	if len(entries) == 0 {
		writeOperationOutcome(w, http.StatusNotFound, "not-found", "Resource not found")
		return
	}

	for _, e := range entries {
		if e.VersionID != vid {
			continue
		}
		if e.Operation == "delete" {
			w.Header().Set("ETag", formatWeakETag(vid))
			writeOperationOutcome(w, http.StatusGone, "gone", "Resource has been deleted")
			return
		}
		if isNilResource(e.Resource) {
			writeOperationOutcome(w, http.StatusInternalServerError, "exception", "history snapshot missing for version")
			return
		}
		w.Header().Set("Content-Type", "application/fhir+json")
		w.Header().Set("ETag", formatWeakETag(vid))
		_ = json.NewEncoder(w).Encode(h.adapter.ProjectForWire(e.Resource, h.fhirVersion))
		return
	}

	// History returned entries but none matched the requested version: the
	// resource exists, the version does not. An empty history would have been
	// reported by handleServiceError above via the adapter's not-found sentinel.
	writeOperationOutcome(w, http.StatusNotFound, "not-found", "Requested version not found")
}

// validateCountParam enforces the FHIR + project contract for the `_count`
// search parameter: empty → pass-through, positive integer → pass-through (adapter clamps >1000),
// zero/negative/non-integer → 400.
func validateCountParam(raw string) (status int, msg string, ok bool) {
	if raw == "" {
		return 0, "", true
	}
	c, err := strconv.Atoi(raw)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return http.StatusBadRequest, "_count value out of range", false
		}
		return http.StatusBadRequest, "_count must be an integer", false
	}
	if c == 0 {
		return http.StatusBadRequest, "_count must be greater than zero", false
	}
	if c < 0 {
		return http.StatusBadRequest, "_count must be > 0; received " + raw, false
	}
	return 0, "", true
}

// isNilResource reports whether a generic Resource value is nil; typed nil
// pointers require reflect (classic Go interface-nil subtlety).
func isNilResource[T Resource](t T) bool {
	rv := reflect.ValueOf(t)
	return !rv.IsValid() || (rv.Kind() == reflect.Pointer && rv.IsNil())
}

// handleServiceError translates an adapter error into an HTTP response; status < 100
// is treated as 500 to prevent panic in WriteHeader(0).
func (h *ResourceHandler[T]) handleServiceError(w http.ResponseWriter, err error) {
	status, code, msg := h.adapter.MapServiceError(err)
	if status < 100 {
		h.logger.Error("adapter returned invalid status; treating as 500", "status", status, "error", err)
		writeOperationOutcome(w, http.StatusInternalServerError, "exception", "An internal error occurred")
		return
	}
	if status == http.StatusUnprocessableEntity || status == http.StatusInternalServerError {
		h.logger.Warn("service error", "status", status, "error", err)
	}
	writeOperationOutcome(w, status, code, msg)
}
