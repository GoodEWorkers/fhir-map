package handler

import (
	"context"
	"net/url"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// HistoryEntry is the generic history record returned by Adapter[T].History, version-agnostic.
type HistoryEntry[T Resource] struct {
	VersionID  int
	Operation  string
	OccurredAt string // RFC3339
	Resource   T
}

// Adapter is the per-resource contract the generic ResourceHandler[T] dispatches into.
type Adapter[T Resource] interface {
	// Identity
	ResourceName() string
	New() T // returns a pointer to a zero-value struct for JSON decode

	// CRUD service shims
	Create(ctx context.Context, t T, mode ValidationMode) (T, error)
	Read(ctx context.Context, id string) (T, error)
	Update(ctx context.Context, id string, t T, mode ValidationMode) (T, error)
	Delete(ctx context.Context, id string) error
	Search(ctx context.Context, q url.Values) (results []T, total int, err error)
	FindByURL(ctx context.Context, rawURL, version string) (T, error)

	// History — Vread walks History so delete-op detection applies uniformly across resources.
	HasHistory() bool
	History(ctx context.Context, id string) ([]HistoryEntry[T], error)

	MapServiceError(err error) (httpStatus int, code string, diagnostic string)

	// R4 wire shaping
	ProjectForWire(t T, version fhir.FHIRVersion) T
	CanonicaliseFromR4(t T) // no-op for resources without an R4/R5 vocab split
	R5OnlyFields() []string // empty → no R4 ingress strictness scan
}
