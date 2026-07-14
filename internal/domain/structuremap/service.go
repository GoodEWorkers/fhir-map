package structuremap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// Service errors mirror conceptmap's so handlers can map both to HTTP statuses
// uniformly. Kept package-local so callers don't import conceptmap just for
// these names.
var (
	ErrNotFound      = errors.New("resource not found")
	ErrGone          = errors.New("resource gone")
	ErrInvalidInput  = errors.New("invalid input")
	ErrUnprocessable = errors.New("unprocessable entity")
	ErrConflict      = errors.New("resource conflict")
)

// Service implements the StructureMap business logic on top of a Repository.
type Service struct {
	repo Repository
}

// NewService wires a Service to its repository.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Create validates and stores a new StructureMap using strict validation.
func (s *Service) Create(ctx context.Context, sm *StructureMap) (*StructureMap, error) {
	return s.CreateWithMode(ctx, sm, ModeStrict)
}

// CreateWithMode is Create with an explicit ValidationMode.
func (s *Service) CreateWithMode(ctx context.Context, sm *StructureMap, mode ValidationMode) (*StructureMap, error) {
	sm.ResourceType = "StructureMap"

	if errs := sm.ValidateMode(mode); len(errs) > 0 {
		return nil, fmt.Errorf("%w: %v", ErrUnprocessable, errs)
	}

	if sm.ID == "" {
		sm.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	sm.CreatedAt = now
	sm.UpdatedAt = now
	sm.Meta = &fhir.Meta{
		VersionID:   "1",
		LastUpdated: now.Format(time.RFC3339),
	}

	return s.repo.Create(ctx, sm)
}

// Read returns a StructureMap by id.
func (s *Service) Read(ctx context.Context, id string) (*StructureMap, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidInput)
	}
	return s.repo.Read(ctx, id)
}

// Update replaces an existing StructureMap using strict validation.
func (s *Service) Update(ctx context.Context, id string, sm *StructureMap) (*StructureMap, error) {
	return s.UpdateWithMode(ctx, id, sm, ModeStrict)
}

// UpdateWithMode is Update with an explicit ValidationMode.
func (s *Service) UpdateWithMode(ctx context.Context, id string, sm *StructureMap, mode ValidationMode) (*StructureMap, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidInput)
	}
	sm.ResourceType = "StructureMap"
	sm.ID = id

	if errs := sm.ValidateMode(mode); len(errs) > 0 {
		return nil, fmt.Errorf("%w: %v", ErrUnprocessable, errs)
	}

	return s.repo.Update(ctx, id, sm)
}

// Delete removes a StructureMap by id (soft delete in the postgres impl).
func (s *Service) Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidInput)
	}
	return s.repo.Delete(ctx, id)
}

// Search runs a paginated search; applies default + cap on Count.
func (s *Service) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	if params.Count <= 0 {
		params.Count = 20
	}
	if params.Count > 1000 {
		params.Count = 1000
	}
	if params.Offset < 0 {
		params.Offset = 0
	}
	return s.repo.Search(ctx, params)
}

// FindByURL resolves a StructureMap canonical URL (+ optional version).
func (s *Service) FindByURL(ctx context.Context, url, version string) (*StructureMap, error) {
	if url == "" {
		return nil, fmt.Errorf("%w: url is required", ErrInvalidInput)
	}
	return s.repo.FindByURL(ctx, url, version)
}
