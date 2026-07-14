package handler

import (
	"encoding/json"
	"fmt"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// historyEntryView is the view converted from HistoryEntry[T] for serialization into a FHIR history Bundle.
type historyEntryView struct {
	VersionID    int
	Operation    string
	OccurredAt   string
	ResourceJSON json.RawMessage // nil for delete-op entries
}

// newSearchBundle wraps fhir.NewSearchBundle (selfLink already encodes resource name).
func newSearchBundle(baseURL string, total int, entries []fhir.BundleEntry, selfLink string) *fhir.Bundle {
	b := fhir.NewSearchBundle(baseURL, total, entries, selfLink)
	return &b
}

func newHistoryBundle(baseURL, prefix, resourceName, id string, entries []historyEntryView) map[string]any {
	bundleEntries := make([]any, 0, len(entries))
	for _, e := range entries {
		req := map[string]any{}
		switch e.Operation {
		case "create":
			req["method"] = "POST"
			req["url"] = resourceName
		case "update":
			req["method"] = "PUT"
			req["url"] = fmt.Sprintf("%s/%s", resourceName, id)
		case "delete":
			req["method"] = "DELETE"
			req["url"] = fmt.Sprintf("%s/%s", resourceName, id)
		}
		// FHIR Bundle invariant bdl-8: fullUrl cannot be a version-specific
		// reference. The version is conveyed by entry.response.etag and (when
		// present) entry.resource.meta.versionId — never on fullUrl itself.
		entryMap := map[string]any{
			"fullUrl": fmt.Sprintf("%s%s/%s/%s",
				baseURL, prefix, resourceName, id),
			"request": req,
			"response": map[string]any{
				"status":       statusForOperation(e.Operation),
				"lastModified": e.OccurredAt,
				"etag":         formatWeakETag(e.VersionID),
			},
		}
		if len(e.ResourceJSON) > 0 {
			entryMap["resource"] = e.ResourceJSON
		}
		bundleEntries = append(bundleEntries, entryMap)
	}
	return map[string]any{
		"resourceType": "Bundle",
		"type":         "history",
		"total":        len(entries),
		"entry":        bundleEntries,
	}
}

func statusForOperation(op string) string {
	switch op {
	case "create":
		return "201 Created"
	case "delete":
		return "204 No Content"
	default:
		return "200 OK"
	}
}
