package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/internal/transform"
	"github.com/goodeworkers/fhir-map/internal/transform/fml"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// TransformStub handles POST /fhir/.../StructureMap/$transform (type- and instance-level).
func (h *StructureMapHandler) TransformStub(w http.ResponseWriter, r *http.Request) {
	if h.transformEng == nil {
		writeOperationOutcome(w, http.StatusNotImplemented, "not-supported",
			"$transform is not yet wired on this server (no transform engine configured)")
		return
	}

	instanceID := r.PathValue("id")

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
		if offending := firstR5OnlyTransformParam(rawBody); offending != "" {
			writeOperationOutcome(w, http.StatusBadRequest, "not-supported",
				"parameter `"+offending+"` is R5-only and not part of the FHIR R4 $transform spec")
			return
		}
	}

	mapRef, sourceValue, inlineMap, err := h.parseTransformInputs(rawBody, instanceID)
	if err != nil {
		writeOperationOutcome(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}

	var sm *structuremap.StructureMap
	switch {
	case inlineMap != nil:
		sm = inlineMap
	case instanceID != "":
		sm, err = h.adapter.Read(r.Context(), instanceID)
		if err != nil {
			h.rh.handleServiceError(w, err)
			return
		}
	case mapRef != "":
		sm, err = h.adapter.FindByURL(r.Context(), mapRef, "")
		if err != nil {
			if errors.Is(err, structuremap.ErrNotFound) {
				writeOperationOutcome(w, http.StatusUnprocessableEntity, "not-found",
					fmt.Sprintf("Referenced StructureMap %q is not loaded", mapRef))
				return
			}
			h.rh.handleServiceError(w, err)
			return
		}
	default:
		writeOperationOutcome(w, http.StatusBadRequest, "invalid",
			"$transform requires either an instance id in the URL, a `map` Parameter with the StructureMap canonical URL, or an `fml` Parameter with FML text")
		return
	}

	// Bound the engine to a per-request time budget to prevent pathological
	// StructureMaps from pinning a worker indefinitely.
	ctx := r.Context()
	if h.transformTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.transformTimeout)
		defer cancel()
	}

	start := time.Now()
	result, err := h.transformEng.Transform(ctx, sm, sourceValue)
	recordTransform(time.Since(start).Seconds(), err)
	if err != nil {
		h.handleTransformError(w, err)
		return
	}

	if resultMap, ok := result.(map[string]any); ok {
		if _, already := resultMap["resourceType"]; !already {
			if tgtType := entryTargetInputType(sm); tgtType != "" {
				resultMap["resourceType"] = tgtType
			}
		}
	}

	// Validate result before returning so invalid FHIR is not emitted;
	// intentionally not double-counted in metrics since transform already succeeded.
	if h.outputValidator != nil {
		if issues := h.outputValidator.ValidateOutput(result); len(issues) > 0 {
			code, detail := aggregateOutputIssues(issues)
			if h.outputValidateStrict {
				writeOperationOutcome(w, http.StatusUnprocessableEntity, code,
					"Transform output failed validation: "+detail)
				return
			}
			// Lenient: emit the output but flag it so the caller and operators
			// see it was not validated clean. Detail is PHI-conservative.
			w.Header().Set("Warning", fmt.Sprintf(`299 fhir-map "transform output validation: %s"`, detail))
			h.logger.Warn("transform output validation issues", "count", len(issues), "detail", detail)
		}
	}

	w.Header().Set("Content-Type", "application/fhir+json")
	_ = json.NewEncoder(w).Encode(result)
}

// isHL7v2Shape reports whether a transform result is an HL7v2 target object
// (so it should be serialized to ER7 rather than JSON): either tagged
// `resourceType: "HL7v2"` or carrying MSH-* segment-field keys.
func isHL7v2Shape(m map[string]any) bool {
	if rt, _ := m["resourceType"].(string); rt == "HL7v2" {
		return true
	}
	for k := range m {
		if strings.HasPrefix(k, "MSH-") {
			return true
		}
	}
	return false
}

// firstR5OnlyTransformParam scans a $transform Parameters body for R5-only
// parameter names.
func firstR5OnlyTransformParam(rawBody []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(rawBody, &raw); err != nil {
		return ""
	}
	if rt, _ := raw["resourceType"].(string); rt != "Parameters" {
		return ""
	}
	params, _ := raw["parameter"].([]any)
	for _, p := range params {
		pm, _ := p.(map[string]any)
		name, _ := pm["name"].(string)
		switch name {
		case "sourceMap", "srcMap", "supportingMap":
			return name
		}
	}
	return ""
}

// canonicalURLFromPart pulls a canonical URL out of a Parameters part.
func canonicalURLFromPart(pm map[string]any) string {
	if v, ok := pm["valueUri"].(string); ok && v != "" {
		return v
	}
	if v, ok := pm["valueCanonical"].(string); ok && v != "" {
		return v
	}
	if v, ok := pm["valueString"].(string); ok && v != "" {
		return v
	}
	return ""
}

// parseTransformInputs extracts mapRef, sourceValue, and inlineMap from the
// raw $transform request body.
func (h *StructureMapHandler) parseTransformInputs(rawBody []byte, instanceID string) (mapRef string, sourceValue any, inlineMap *structuremap.StructureMap, err error) {
	var raw map[string]any
	if err := json.Unmarshal(rawBody, &raw); err != nil {
		return "", nil, nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	if rt, _ := raw["resourceType"].(string); rt == "Parameters" {
		params, _ := raw["parameter"].([]any)
		for _, p := range params {
			pm, _ := p.(map[string]any)
			name, _ := pm["name"].(string)
			switch name {
			case "map":
				if v := canonicalURLFromPart(pm); v != "" {
					mapRef = v
				}
			case "source":
				if res, ok := pm["resource"]; ok {
					sourceValue = res
				} else if v := canonicalURLFromPart(pm); v != "" {
					mapRef = v
				}
			case "content":
				if res, ok := pm["resource"]; ok {
					sourceValue = res
				}
			case "input":
				if parts, ok := pm["part"].([]any); ok {
					for _, sp := range parts {
						spm, _ := sp.(map[string]any)
						if spm["name"] == "source" {
							if res, ok := spm["resource"]; ok {
								sourceValue = res
							}
						}
					}
				}
			case "fml", "srcMap":
				if v, ok := pm["valueString"].(string); ok && v != "" {
					parsed, perr := fml.Parse(v)
					if perr != nil {
						return "", nil, nil, fmt.Errorf("FML parse error: %w", perr)
					}
					inlineMap = parsed
				}
			case "structureMap", "sourceMap":
				if res, ok := pm["resource"].(map[string]any); ok {
					raw2, err := json.Marshal(res)
					if err != nil {
						return "", nil, nil, fmt.Errorf("re-marshal inline structureMap: %w", err)
					}
					var parsed structuremap.StructureMap
					if err := json.Unmarshal(raw2, &parsed); err != nil {
						return "", nil, nil, fmt.Errorf("parse inline structureMap: %w", err)
					}
					inlineMap = &parsed
				}
			case "supportingMap":
				// R5 spec: accepted but ignored.
			case "structureMapEndpoint", "endpoint":
				// Recognized but ignored.
			}
		}
		if sourceValue == nil {
			return "", nil, nil, fmt.Errorf("parameters body must include a `content` (R4/R5 spec) or `source`/`input` (HAPI) parameter with a resource")
		}
		return mapRef, sourceValue, inlineMap, nil
	}
	if instanceID == "" {
		return "", nil, nil, fmt.Errorf("type-level $transform requires a Parameters body with a `map` parameter naming the StructureMap")
	}
	return "", raw, nil, nil
}

// handleTransformError maps engine sentinel errors to FHIR HTTP responses.
func (h *StructureMapHandler) handleTransformError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, transform.ErrInputTypeMismatch):
		detail := extractSentinelDetail(err, transform.ErrInputTypeMismatch)
		if detail == "" {
			detail = "The input resource type does not match the StructureMap-declared input type."
		}
		writeOperationOutcome(w, http.StatusUnprocessableEntity, "invalid", detail)
	case errors.Is(err, transform.ErrMapNotFound):
		detail := extractSentinelDetail(err, transform.ErrMapNotFound)
		if detail == "" {
			detail = "A referenced map or group could not be resolved."
		}
		writeOperationOutcome(w, http.StatusUnprocessableEntity, "not-found", detail)
	case errors.Is(err, transform.ErrRecursionLimit):
		detail := extractSentinelDetail(err, transform.ErrRecursionLimit)
		if detail == "" {
			detail = "Group recursion depth limit exceeded."
		}
		writeOperationOutcome(w, http.StatusUnprocessableEntity, "too-costly", detail)
	case errors.Is(err, transform.ErrTransformCanceled):
		// Distinguish a genuine timeout (server-imposed budget) from a client
		// disconnect (request context canceled). A canceled client gets no
		// useful response; only emit the structured outcome for a deadline.
		if errors.Is(err, context.DeadlineExceeded) {
			writeOperationOutcome(w, http.StatusUnprocessableEntity, "too-costly",
				"Transform exceeded the server time budget. Simplify the StructureMap or reduce the source resource size.")
			return
		}
		// Client-canceled: best-effort 499-style signal; body may never be read.
		writeOperationOutcome(w, http.StatusUnprocessableEntity, "timeout",
			"Transform was canceled before completion.")
	case errors.Is(err, transform.ErrCheckFailed):
		detail := extractSentinelDetail(err, transform.ErrCheckFailed)
		if detail == "" {
			detail = "A source.check assertion failed."
		}
		writeOperationOutcome(w, http.StatusUnprocessableEntity, "invariant", detail)
	case errors.Is(err, transform.ErrNonConformantCoercion):
		// Strict mode: a typed transform could not coerce a non-conformant
		// value. Detail names only the transform (PHI-safe). FHIR code "value".
		detail := extractSentinelDetail(err, transform.ErrNonConformantCoercion)
		if detail == "" {
			detail = "A typed transform could not coerce a non-conformant value (strict mode)."
		} else {
			detail = "Transform " + detail + " could not coerce a non-conformant value (strict mode)."
		}
		writeOperationOutcome(w, http.StatusUnprocessableEntity, "value", detail)
	case errors.Is(err, transform.ErrTranslateNoMatch):
		// Strict mode: a resolved ConceptMap had no mapping for the code.
		// Detail names only the map URL (PHI-safe). FHIR code "not-found".
		detail := extractSentinelDetail(err, transform.ErrTranslateNoMatch)
		if detail == "" {
			detail = "A code could not be mapped by the resolved ConceptMap (strict mode)."
		} else {
			detail = "A code could not be mapped by ConceptMap " + detail + " (strict mode)."
		}
		writeOperationOutcome(w, http.StatusUnprocessableEntity, "not-found", detail)
	case errors.Is(err, transform.ErrInputInvalid):
		writeOperationOutcome(w, http.StatusUnprocessableEntity, "invalid",
			"The input resource failed validation. Correct the resource and resubmit.")
	default:
		h.logger.Error("transform internal error", "error", err)
		writeOperationOutcome(w, http.StatusUnprocessableEntity, "exception",
			"Transform failed due to an internal error.")
	}
}

// extractSentinelDetail extracts wrapped detail from fmt.Errorf("%w: detail", sentinel).
func extractSentinelDetail(err, sentinel error) string {
	prefix := sentinel.Error() + ": "
	for e := err; e != nil; e = errors.Unwrap(e) {
		if u := errors.Unwrap(e); u != nil && errors.Is(u, sentinel) {
			s := e.Error()
			if strings.HasPrefix(s, prefix) {
				return strings.TrimPrefix(s, prefix)
			}
		}
	}
	return ""
}

// entryTargetInputType returns the Type declared on the first target-mode Input
// of the StructureMap's entry group.
func entryTargetInputType(sm *structuremap.StructureMap) string {
	if len(sm.Group) == 0 {
		return ""
	}
	extended := make(map[string]bool, len(sm.Group))
	for i := range sm.Group {
		if sm.Group[i].Extends != "" {
			extended[sm.Group[i].Extends] = true
		}
	}
	var entry *structuremap.Group
	for i := range sm.Group {
		if !extended[sm.Group[i].Name] {
			entry = &sm.Group[i]
			break
		}
	}
	if entry == nil {
		entry = &sm.Group[0]
	}
	for _, in := range entry.Input {
		if in.Mode == "target" {
			return in.Type
		}
	}
	return ""
}
