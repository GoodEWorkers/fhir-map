package structuremap

import "context"

// SearchParams mirrors conceptmap.SearchParams to keep the handler/repo split consistent.
type SearchParams struct {
	ID          string
	URL         string
	Version     string
	Name        string
	Title       string
	Status      string
	Publisher   string
	Description string
	Date        string
	Identifier  string

	Count  int
	Offset int
}

// SearchResult wraps a search hit list with the total before pagination.
type SearchResult struct {
	StructureMaps []StructureMap
	Total         int
}

// Repository is the persistence contract for StructureMap resources; implementations must be thread-safe.
type Repository interface {
	Create(ctx context.Context, sm *StructureMap) (*StructureMap, error)
	Read(ctx context.Context, id string) (*StructureMap, error)
	Update(ctx context.Context, id string, sm *StructureMap) (*StructureMap, error)
	Delete(ctx context.Context, id string) error
	Search(ctx context.Context, params SearchParams) (*SearchResult, error)
	FindByURL(ctx context.Context, url string, version string) (*StructureMap, error)

	// History returns the timeline newest-first. ErrNotFound if no versions exist.
	History(ctx context.Context, id string) ([]HistoryEntry, error)
	// ReadVersion is FHIR vread — exact snapshot at the given version.
	ReadVersion(ctx context.Context, id string, versionID int) (*StructureMap, error)
}

// HistoryEntry mirrors conceptmap.HistoryEntry for the StructureMap resource.
type HistoryEntry struct {
	VersionID  int
	Operation  string // create | update | delete
	OccurredAt string // RFC3339
	Resource   *StructureMap
}
