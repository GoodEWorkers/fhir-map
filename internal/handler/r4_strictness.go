package handler

import "encoding/json"

// firstUnknownTopLevelField scans a top-level JSON object for any field whose
// name is in r5OnlyFields and returns the first one found, or "" if none.
// Used by the R4 tree on Create/Update to reject ingress of R5-only fields that
// would silently survive in canonical storage and leak on R5 GET.
// Returns "" on malformed JSON (the caller's regular parse error path covers that).
func firstUnknownTopLevelField(rawBody []byte, r5OnlyFields []string) string {
	if len(r5OnlyFields) == 0 {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(rawBody, &raw); err != nil {
		return ""
	}
	for _, k := range r5OnlyFields {
		if _, ok := raw[k]; ok {
			return k
		}
	}
	return ""
}
