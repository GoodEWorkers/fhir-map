package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// writeOperationOutcome writes a FHIR OperationOutcome error response.
func writeOperationOutcome(w http.ResponseWriter, status int, code, message string) {
	outcome := map[string]any{
		"resourceType": "OperationOutcome",
		"issue": []map[string]any{{
			"severity":    severityForStatus(status),
			"code":        code,
			"diagnostics": message,
		}},
	}
	w.Header().Set("Content-Type", "application/fhir+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(outcome)
}

// severityForStatus returns the FHIR issue severity for an HTTP status code.
func severityForStatus(status int) string {
	if status >= 500 {
		return "fatal"
	}
	return "error"
}

// validateAndParseIfMatchETag returns the bare versionId for a syntactically valid weak or strong FHIR ETag per RFC 7232 §2.3.
// Version is treated as opaque; mismatch surfaces later as 412 Precondition Failed, not 400.
// Currently handles single entity-tag only; comma-separated lists and wildcards return 400.
func validateAndParseIfMatchETag(raw string) (versionID string, err error) {
	v := strings.TrimSpace(raw)
	v = strings.TrimPrefix(v, "W/")
	if len(v) < 3 || v[0] != '"' || v[len(v)-1] != '"' {
		return "", fmt.Errorf(`If-Match header is malformed; expected "<etag>" or W/"<etag>"`)
	}
	inner := v[1 : len(v)-1]
	if inner == "" {
		return "", fmt.Errorf("If-Match header is malformed; etag value is empty")
	}
	if strings.Contains(inner, `"`) {
		return "", fmt.Errorf("If-Match header is malformed; etag contains a quote")
	}
	return inner, nil
}

// formatWeakETag returns the W/"…" weak-ETag encoding. The type constraint
// carries both real callers: the string Meta.versionId (resource_handler.go)
// and the int history VersionID (bundle.go / vread).
func formatWeakETag[T ~string | ~int](versionID T) string {
	return fmt.Sprintf(`W/"%v"`, versionID)
}
