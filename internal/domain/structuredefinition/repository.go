package structuredefinition

import "context"

// SearchParams covers the StructureDefinition search params we serve.
// Mirrors structuremap.SearchParams so the handler/repo split stays consistent.
type SearchParams struct {
	ID      string
	URL     string
	Version string
	Name    string
	Status  string
	Kind    string
	Type    string

	Count  int
	Offset int
}

// SearchResult wraps a search hit list with the total before pagination.
type SearchResult struct {
	StructureDefinitions []StructureDefinition
	Total                int
}

// HistoryEntry mirrors structuremap.HistoryEntry for the StructureDefinition resource.
type HistoryEntry struct {
	VersionID  int
	Operation  string // create | update | delete
	OccurredAt string // RFC3339
	Resource   *StructureDefinition
}

// Repository is the persistence contract for StructureDefinition resources.
// Implementations must be safe for concurrent access.
type Repository interface {
	Create(ctx context.Context, sd *StructureDefinition) (*StructureDefinition, error)
	Read(ctx context.Context, id string) (*StructureDefinition, error)
	Update(ctx context.Context, id string, sd *StructureDefinition) (*StructureDefinition, error)
	Delete(ctx context.Context, id string) error
	Search(ctx context.Context, params SearchParams) (*SearchResult, error)

	// FindByURL resolves a canonical URL to the latest non-deleted StructureDefinition.
	// Returns ErrNotFound when no match exists. Returns ErrGone for soft-deleted resources.
	// The resolver calls this with version="" (latest-version semantics).
	FindByURL(ctx context.Context, url string, version string) (*StructureDefinition, error)

	// History returns the timeline newest-first. ErrNotFound if no versions exist.
	History(ctx context.Context, id string) ([]HistoryEntry, error)
	// ReadVersion is FHIR vread — exact snapshot at the given version.
	ReadVersion(ctx context.Context, id string, versionID int) (*StructureDefinition, error)
}
