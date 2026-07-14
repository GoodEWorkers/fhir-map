package structuredefinition

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// Service errors mirror structuremap's so handlers can map both to HTTP
// statuses uniformly without importing a sibling domain package.
var (
	ErrNotFound      = errors.New("resource not found")
	ErrGone          = errors.New("resource gone")
	ErrInvalidInput  = errors.New("invalid input")
	ErrUnprocessable = errors.New("unprocessable entity")
	ErrConflict      = errors.New("resource conflict")
)

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(ctx context.Context, sd *StructureDefinition) (*StructureDefinition, error) {
	return s.CreateWithMode(ctx, sd, ModeStrict)
}

func (s *Service) CreateWithMode(ctx context.Context, sd *StructureDefinition, mode ValidationMode) (*StructureDefinition, error) {
	sd.ResourceType = "StructureDefinition"

	// Validation failures use ErrUnprocessable (→ HTTP 422),
	// not ErrInvalidInput (→ HTTP 400).
	if errs := sd.ValidateMode(mode); len(errs) > 0 {
		return nil, fmt.Errorf("%w: %v", ErrUnprocessable, errs)
	}

	if sd.ID == "" {
		sd.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	sd.CreatedAt = now
	sd.UpdatedAt = now
	sd.Meta = &fhir.Meta{
		VersionID:   "1",
		LastUpdated: now.Format(time.RFC3339),
	}

	return s.repo.Create(ctx, sd)
}

func (s *Service) Read(ctx context.Context, id string) (*StructureDefinition, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidInput)
	}
	return s.repo.Read(ctx, id)
}

func (s *Service) Update(ctx context.Context, id string, sd *StructureDefinition) (*StructureDefinition, error) {
	return s.UpdateWithMode(ctx, id, sd, ModeStrict)
}

func (s *Service) UpdateWithMode(ctx context.Context, id string, sd *StructureDefinition, mode ValidationMode) (*StructureDefinition, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidInput)
	}
	sd.ResourceType = "StructureDefinition"
	sd.ID = id

	if errs := sd.ValidateMode(mode); len(errs) > 0 {
		return nil, fmt.Errorf("%w: %v", ErrUnprocessable, errs)
	}

	return s.repo.Update(ctx, id, sd)
}

// Delete removes a StructureDefinition by id (soft delete in postgres).
func (s *Service) Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidInput)
	}
	return s.repo.Delete(ctx, id)
}

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

// FindByURL resolves a StructureDefinition by canonical URL; version="" means latest.
func (s *Service) FindByURL(ctx context.Context, url, version string) (*StructureDefinition, error) {
	if url == "" {
		return nil, fmt.Errorf("%w: url is required", ErrInvalidInput)
	}
	return s.repo.FindByURL(ctx, url, version)
}
