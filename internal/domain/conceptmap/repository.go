package conceptmap

import "context"

// SearchParams represents the supported FHIR search parameters for ConceptMap.
type SearchParams struct {
	// Canonical metadata search params
	ID          string
	URL         string
	Version     string
	Name        string
	Title       string
	Status      string
	Publisher   string
	Description string
	Date        string
	Identifier  string // token: system|value

	// ConceptMap-specific search params
	SourceCode        string
	TargetCode        string
	SourceGroupSystem string
	TargetGroupSystem string
	SourceScope       string
	SourceScopeURI    string
	TargetScope       string
	TargetScopeURI    string

	// Pagination
	Count  int
	Offset int
}

// SearchResult wraps search results with total count for pagination.
type SearchResult struct {
	ConceptMaps []ConceptMap
	Total       int
}

// Repository defines the persistence interface for ConceptMap resources.
// Implementations must be safe for concurrent access.
type Repository interface {
	// Create stores a new ConceptMap and returns it with server-assigned fields (id, meta).
	Create(ctx context.Context, cm *ConceptMap) (*ConceptMap, error)

	// Read retrieves a ConceptMap by its logical ID.
	Read(ctx context.Context, id string) (*ConceptMap, error)

	// Update replaces an existing ConceptMap. Returns ErrNotFound if the resource doesn't exist.
	Update(ctx context.Context, id string, cm *ConceptMap) (*ConceptMap, error)

	// Delete removes a ConceptMap by its logical ID. Returns ErrNotFound if it doesn't exist.
	Delete(ctx context.Context, id string) error

	// Search finds ConceptMaps matching the given search parameters.
	Search(ctx context.Context, params SearchParams) (*SearchResult, error)

	// FindByURL looks up a ConceptMap by its canonical URL (and optional version).
	FindByURL(ctx context.Context, url string, version string) (*ConceptMap, error)

	// FindBySourceScope finds a ConceptMap by its source scope URI or canonical.
	// Used when $translate is called without an explicit URL but with a sourceScope parameter.
	FindBySourceScope(ctx context.Context, sourceScope string) (*ConceptMap, error)
}

// HistoryEntry is a single point on the timeline of a ConceptMap.
// Entries are append-only; the operation column records create/update/delete.
// For 'delete', the Resource is the last snapshot before deletion.
type HistoryEntry struct {
	VersionID  int
	Operation  string // "create" | "update" | "delete"
	OccurredAt string // RFC3339 UTC
	Resource   *ConceptMap
}
