package fhir

import "encoding/json"

// Bundle represents a FHIR Bundle resource for search results.
type Bundle struct {
	ResourceType string        `json:"resourceType"`
	ID           string        `json:"id,omitempty"`
	Type         string        `json:"type"`
	Total        int           `json:"total"`
	Link         []BundleLink  `json:"link,omitempty"`
	Entry        []BundleEntry `json:"entry,omitempty"`
}

// BundleLink represents a link in a FHIR Bundle (self, next, prev).
type BundleLink struct {
	Relation string `json:"relation"`
	URL      string `json:"url"`
}

// BundleEntry represents an entry in a FHIR Bundle.
type BundleEntry struct {
	FullURL  string          `json:"fullUrl,omitempty"`
	Resource json.RawMessage `json:"resource"`
	Search   *BundleSearch   `json:"search,omitempty"`
}

// BundleSearch provides search-related information for a bundle entry.
type BundleSearch struct {
	Mode string `json:"mode,omitempty"`
}

// NewSearchBundle creates a new searchset Bundle.
func NewSearchBundle(baseURL string, total int, entries []BundleEntry, selfLink string) Bundle {
	links := []BundleLink{
		{Relation: "self", URL: selfLink},
	}
	return Bundle{
		ResourceType: ResourceTypeBundle,
		Type:         "searchset",
		Total:        total,
		Link:         links,
		Entry:        entries,
	}
}
