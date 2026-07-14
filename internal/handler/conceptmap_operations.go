package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/translate"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// TranslateType handles GET/POST /fhir/.../ConceptMap/$translate (type-level).
func (h *ConceptMapHandler) TranslateType(w http.ResponseWriter, r *http.Request) {
	req, err := h.parseTranslateRequest(r, "")
	if err != nil {
		if IsBodyTooLarge(err) {
			WriteBodyTooLargeResponse(w)
			return
		}
		writeOperationOutcome(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}

	resp, err := h.engine.Translate(r.Context(), *req)
	if err != nil {
		if errors.Is(err, conceptmap.ErrGone) {
			writeOperationOutcome(w, http.StatusGone, "gone", "Resource has been deleted")
			return
		}
		if errors.Is(err, conceptmap.ErrNotFound) {
			params := fhir.NewTranslateResponseFor(false, "No ConceptMap found", nil, h.fhirVersion)
			w.Header().Set("Content-Type", "application/fhir+json")
			_ = json.NewEncoder(w).Encode(params)
			return
		}
		h.logger.Error("translate error", "error", err)
		writeOperationOutcome(w, http.StatusInternalServerError, "exception", "An internal error occurred")
		return
	}

	params := fhir.NewTranslateResponseFor(resp.Result, resp.Message, resp.Matches, h.fhirVersion, translateWarningsToIssues(resp.Warnings)...)
	w.Header().Set("Content-Type", "application/fhir+json")
	_ = json.NewEncoder(w).Encode(params)
}

// TranslateInstance handles GET/POST /fhir/.../ConceptMap/{id}/$translate (instance-level).
func (h *ConceptMapHandler) TranslateInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	req, err := h.parseTranslateRequest(r, id)
	if err != nil {
		if IsBodyTooLarge(err) {
			WriteBodyTooLargeResponse(w)
			return
		}
		writeOperationOutcome(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}

	resp, err := h.engine.Translate(r.Context(), *req)
	if err != nil {
		if errors.Is(err, conceptmap.ErrGone) {
			writeOperationOutcome(w, http.StatusGone, "gone", "Resource has been deleted")
			return
		}
		if errors.Is(err, conceptmap.ErrNotFound) {
			params := fhir.NewTranslateResponseFor(false, "No ConceptMap found", nil, h.fhirVersion)
			w.Header().Set("Content-Type", "application/fhir+json")
			_ = json.NewEncoder(w).Encode(params)
			return
		}
		h.logger.Error("translate error", "error", err)
		writeOperationOutcome(w, http.StatusInternalServerError, "exception", "An internal error occurred")
		return
	}

	params := fhir.NewTranslateResponseFor(resp.Result, resp.Message, resp.Matches, h.fhirVersion, translateWarningsToIssues(resp.Warnings)...)
	w.Header().Set("Content-Type", "application/fhir+json")
	_ = json.NewEncoder(w).Encode(params)
}

// TranslateBatch handles POST /fhir/{R4|R5}/ConceptMap/$translate-batch.
func (h *ConceptMapHandler) TranslateBatch(w http.ResponseWriter, r *http.Request) {
	var params fhir.Parameters
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		if IsBodyTooLarge(err) {
			WriteBodyTooLargeResponse(w)
			return
		}
		writeOperationOutcome(w, http.StatusBadRequest, "invalid", "Invalid Parameters body: "+err.Error())
		return
	}

	for pi := range params.Parameter {
		p := &params.Parameter[pi]
		if p.Name == "reverse" {
			isTrue := (p.ValueBoolean != nil && *p.ValueBoolean) ||
				strings.EqualFold(p.ValueCode, "true") ||
				strings.EqualFold(p.ValueString, "true")
			if isTrue {
				writeOperationOutcome(w, http.StatusBadRequest, "invalid",
					"reverse translation is not supported in $translate-batch")
				return
			}
		}
	}

	var (
		url, version, targetSystem, conceptMapID string
		probes                                   []translate.BatchProbe
	)
	for pi := range params.Parameter {
		p := &params.Parameter[pi]
		switch p.Name {
		case "url":
			url = pickURI(*p)
		case "conceptMapVersion":
			version = p.ValueString
		case "targetSystem", "targetsystem":
			if v := pickURI(*p); v != "" {
				targetSystem = v
			}
		case "conceptMapId":
			conceptMapID = p.ValueString
		case "code":
			probe := translate.BatchProbe{}
			for ppi := range p.Part {
				pp := &p.Part[ppi]
				switch pp.Name {
				case "code":
					probe.SourceCode = pp.ValueCode
					if probe.SourceCode == "" {
						probe.SourceCode = pp.ValueString
					}
				case "system":
					probe.SourceSystem = pickURI(*pp)
				}
			}
			if probe.SourceCode != "" {
				probes = append(probes, probe)
			}
		}
	}

	if len(probes) == 0 {
		writeOperationOutcome(w, http.StatusUnprocessableEntity, "invalid",
			"$translate-batch requires at least one `code` parameter with a `code` part")
		return
	}
	if url == "" && conceptMapID == "" {
		writeOperationOutcome(w, http.StatusUnprocessableEntity, "invalid",
			"$translate-batch requires either `url` or `conceptMapId`")
		return
	}

	batcher, ok := h.engine.(translate.BatchTranslator)
	if !ok {
		writeOperationOutcome(w, http.StatusNotImplemented, "not-supported",
			"batch translate not supported by the current engine; use $translate per code")
		return
	}

	resp, err := batcher.TranslateBatch(r.Context(), url, version, conceptMapID, probes, targetSystem)
	if err != nil {
		writeOperationOutcome(w, http.StatusNotFound, "not-found", err.Error())
		return
	}

	per := make([]fhir.BatchPerProbe, len(probes))
	for i, p := range probes {
		per[i] = fhir.BatchPerProbe{
			Code:    p.SourceCode,
			System:  p.SourceSystem,
			Result:  resp.Per[i].Result,
			Msg:     resp.Per[i].Message,
			Matches: resp.Per[i].Matches,
			IsError: resp.Per[i].IsError,
		}
	}
	params2 := fhir.NewTranslateBatchResponseFor(resp.Overall, per, h.fhirVersion)
	w.Header().Set("Content-Type", "application/fhir+json")
	_ = json.NewEncoder(w).Encode(params2)
}

// knownSearchParams is the set of FHIR ConceptMap search parameters this server supports.
var knownSearchParams = map[string]struct{}{
	"_id": {}, "url": {}, "version": {}, "name": {}, "title": {}, "status": {},
	"publisher": {}, "description": {}, "date": {}, "identifier": {},
	"source-code": {}, "target-code": {},
	"source-system": {}, "target-system": {},
	"source-group-system": {}, "target-group-system": {},
	"source-scope": {}, "source-scope-uri": {},
	"target-scope": {}, "target-scope-uri": {},
	"context": {}, "context-type": {}, "context-quantity": {},
	"context-type-quantity": {}, "context-type-value": {}, "jurisdiction": {},
	"_count": {}, "_offset": {}, "_sort": {},
	"_format": {}, "_summary": {}, "_elements": {}, "_pretty": {}, "_total": {},
	"_lastUpdated": {}, "_include": {}, "_revinclude": {}, "_has": {},
	"_security": {}, "_profile": {}, "_tag": {}, "_contained": {}, "_containedType": {},
}

// firstNonEmpty returns the first non-empty string from its arguments, or "".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseTranslateRequest parses the $translate operation parameters.
func (h *ConceptMapHandler) parseTranslateRequest(r *http.Request, instanceID string) (*translate.Request, error) {
	req := &translate.Request{
		ConceptMapID: instanceID,
	}

	if r.Method == http.MethodGet {
		q := r.URL.Query()
		req.URL = q.Get("url")
		req.Version = q.Get("conceptMapVersion")

		var err error
		if req.SourceCode, err = pickOne("sourceCode", q.Get("sourceCode"), "code", q.Get("code")); err != nil {
			return nil, err
		}
		if req.SourceSystem, err = pickOne("sourceSystem", q.Get("sourceSystem"), "system", q.Get("system")); err != nil {
			return nil, err
		}
		if req.SourceVersion, err = pickOne("sourceVersion", q.Get("sourceVersion"), "version", q.Get("version")); err != nil {
			return nil, err
		}
		if req.SourceScope, err = pickOne("sourceScope", q.Get("sourceScope"), "source", q.Get("source")); err != nil {
			return nil, err
		}
		if req.TargetScope, err = pickOne("targetScope", q.Get("targetScope"), "target", q.Get("target")); err != nil {
			return nil, err
		}
		req.TargetSystem = q.Get("targetSystem")
		if req.TargetSystem == "" {
			req.TargetSystem = q.Get("targetsystem")
		}
		req.TargetCode = q.Get("targetCode")

		r5coding := q.Get("sourceCoding")
		r4coding := q.Get("coding")
		if r5coding != "" && r4coding != "" {
			return nil, fmt.Errorf("parameters 'coding' (R4) and 'sourceCoding' (R5) are mutually exclusive")
		}
		coding := r5coding
		if coding == "" {
			coding = r4coding
		}
		if coding != "" {
			if parts := strings.SplitN(coding, "|", 2); len(parts) == 2 {
				req.SourceCoding = &fhir.Coding{System: parts[0], Code: parts[1]}
			}
		}
		if coding := q.Get("targetCoding"); coding != "" {
			parts := strings.SplitN(coding, "|", 2)
			if len(parts) == 2 {
				req.TargetCoding = &fhir.Coding{System: parts[0], Code: parts[1]}
			}
		}
		if q.Get("reverse") == "true" && req.SourceCode != "" {
			req.TargetCode = req.SourceCode
			req.SourceCode = ""
			if req.TargetSystem == "" {
				req.TargetSystem = req.SourceSystem
			}
			req.SourceSystem = ""
		}
	} else {
		var params fhir.Parameters
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			return nil, fmt.Errorf("invalid Parameters resource: %w", err)
		}

		type stringSlot struct {
			present bool
			value   string
		}
		type codingSlot struct {
			present bool
			value   *fhir.Coding
		}
		type ccSlot struct {
			present bool
			value   *fhir.CodeableConcept
		}
		strSlots := map[string]stringSlot{}
		codSlots := map[string]codingSlot{}
		ccSlots := map[string]ccSlot{}

		setStr := func(name string, p fhir.Parameter) {
			v := p.ValueCode
			if v == "" {
				v = p.ValueURI
			}
			if v == "" {
				v = p.ValueString
			}
			strSlots[name] = stringSlot{present: true, value: v}
		}

		for pi2 := range params.Parameter {
			p := &params.Parameter[pi2]
			switch p.Name {
			case "url":
				strSlots["url"] = stringSlot{present: true, value: pickURI(*p)}
			case "conceptMapVersion":
				strSlots["conceptMapVersion"] = stringSlot{present: true, value: p.ValueString}
			case "code", "sourceCode":
				setStr(p.Name, *p)
			case "system", "sourceSystem":
				setStr(p.Name, *p)
			case "version", "sourceVersion":
				strSlots[p.Name] = stringSlot{present: true, value: p.ValueString}
			case "source", "sourceScope":
				setStr(p.Name, *p)
			case "coding", "sourceCoding":
				codSlots[p.Name] = codingSlot{present: true, value: p.ValueCoding}
			case "codeableConcept", "sourceCodeableConcept":
				ccSlots[p.Name] = ccSlot{present: true, value: p.ValueCodeableConcept}
			case "targetCodeableConcept":
				ccSlots["targetCodeableConcept"] = ccSlot{present: true, value: p.ValueCodeableConcept}
			case "targetCode":
				setStr(p.Name, *p)
			case "targetCoding":
				codSlots["targetCoding"] = codingSlot{present: true, value: p.ValueCoding}
			case "target", "targetScope":
				setStr(p.Name, *p)
			case "targetsystem", "targetSystem":
				setStr(p.Name, *p)
			case "reverse":
				if p.ValueBoolean != nil && *p.ValueBoolean {
					strSlots["reverse"] = stringSlot{present: true, value: "true"}
				}
			case "dependency":
				dep := DependencyInput{}
				for ppi2 := range p.Part {
					pp := &p.Part[ppi2]
					switch pp.Name {
					case "attribute":
						dep.Attribute = pickURI(*pp)
						if dep.Attribute == "" {
							dep.Attribute = pp.ValueString
						}
					case "value":
						switch {
						case pp.ValueCoding != nil:
							dep.ValueCoding = pp.ValueCoding
						case pp.ValueCode != "":
							dep.ValueCode = pp.ValueCode
						default:
							dep.ValueString = pp.ValueString
						}
					}
				}
				hasCoding := dep.ValueCoding != nil && (dep.ValueCoding.Code != "" || dep.ValueCoding.System != "")
				hasValue := hasCoding || dep.ValueCode != "" || dep.ValueString != ""
				if dep.ValueCoding != nil && !hasCoding {
					dep.ValueCoding = nil
				}
				if dep.Attribute != "" && hasValue {
					req.Dependencies = append(req.Dependencies, dep)
				}
			}
		}

		req.URL = strSlots["url"].value
		req.Version = strSlots["conceptMapVersion"].value
		var err error
		if req.SourceCode, err = pickOne("sourceCode", strSlots["sourceCode"].value, "code", strSlots["code"].value); err != nil {
			return nil, err
		}
		if req.SourceSystem, err = pickOne("sourceSystem", strSlots["sourceSystem"].value, "system", strSlots["system"].value); err != nil {
			return nil, err
		}
		if req.SourceVersion, err = pickOne("sourceVersion", strSlots["sourceVersion"].value, "version", strSlots["version"].value); err != nil {
			return nil, err
		}
		if req.SourceScope, err = pickOne("sourceScope", strSlots["sourceScope"].value, "source", strSlots["source"].value); err != nil {
			return nil, err
		}
		if req.TargetScope, err = pickOne("targetScope", strSlots["targetScope"].value, "target", strSlots["target"].value); err != nil {
			return nil, err
		}
		req.TargetSystem = strSlots["targetSystem"].value
		if req.TargetSystem == "" {
			req.TargetSystem = strSlots["targetsystem"].value
		}
		req.TargetCode = strSlots["targetCode"].value
		if codSlots["sourceCoding"].present && codSlots["coding"].present {
			return nil, fmt.Errorf("parameters 'coding' (R4) and 'sourceCoding' (R5) are mutually exclusive")
		}
		if codSlots["sourceCoding"].present {
			req.SourceCoding = codSlots["sourceCoding"].value
		} else if codSlots["coding"].present {
			req.SourceCoding = codSlots["coding"].value
		}
		if codSlots["targetCoding"].present {
			req.TargetCoding = codSlots["targetCoding"].value
		}
		if ccSlots["sourceCodeableConcept"].present && ccSlots["codeableConcept"].present {
			return nil, fmt.Errorf("parameters 'codeableConcept' (R4) and 'sourceCodeableConcept' (R5) are mutually exclusive")
		}
		if ccSlots["sourceCodeableConcept"].present {
			req.SourceCodeableConcept = ccSlots["sourceCodeableConcept"].value
		} else if ccSlots["codeableConcept"].present {
			req.SourceCodeableConcept = ccSlots["codeableConcept"].value
		}
		if ccSlots["targetCodeableConcept"].present {
			req.TargetCodeableConcept = ccSlots["targetCodeableConcept"].value
		}
		if strSlots["reverse"].value == "true" && req.SourceCode != "" {
			req.TargetCode = req.SourceCode
			req.SourceCode = ""
			if req.TargetSystem == "" {
				req.TargetSystem = req.SourceSystem
			}
			req.SourceSystem = ""
		}
	}

	hasSource := req.SourceCode != "" || req.SourceCoding != nil || req.SourceCodeableConcept != nil
	hasTarget := req.TargetCode != "" || req.TargetCoding != nil || req.TargetCodeableConcept != nil
	if !hasSource && !hasTarget {
		return nil, fmt.Errorf("one of sourceCode, sourceCoding, sourceCodeableConcept, targetCode, targetCoding, or targetCodeableConcept must be provided")
	}

	return req, nil
}

// pickOne returns either the R5 value or the R4 value when only one is set,
// and an error when both are present.
func pickOne(r5Name, r5Value, r4Name, r4Value string) (string, error) {
	if r5Value != "" && r4Value != "" {
		return "", fmt.Errorf("parameters %q (R4) and %q (R5) are mutually exclusive", r4Name, r5Name)
	}
	if r5Value != "" {
		return r5Value, nil
	}
	return r4Value, nil
}

// pickURI extracts a URI-shaped value from a Parameter.
func pickURI(p fhir.Parameter) string {
	if p.ValueURI != "" {
		return p.ValueURI
	}
	return p.ValueString
}

// DependencyInput is a handler-local alias to keep imports tidy.
type DependencyInput = translate.DependencyInput

// translateWarningsToIssues maps engine-level warnings into the wire-shape of the $translate response.
func translateWarningsToIssues(ws []translate.Warning) []fhir.TranslateIssue {
	if len(ws) == 0 {
		return nil
	}
	out := make([]fhir.TranslateIssue, 0, len(ws))
	for _, w := range ws {
		out = append(out, fhir.TranslateIssue{
			Severity:    "warning",
			Code:        w.Code,
			Diagnostics: w.Diagnostics,
		})
	}
	return out
}
