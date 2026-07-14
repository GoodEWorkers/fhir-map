package structuredefinition

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// inMemoryRepo is a minimal in-memory repository for service-layer unit tests.
// It is NOT safe for concurrent use — service tests are single-goroutine.
type inMemoryRepo struct {
	data    map[string]*StructureDefinition
	deleted map[string]bool
}

func newInMemoryRepo() *inMemoryRepo {
	return &inMemoryRepo{
		data:    make(map[string]*StructureDefinition),
		deleted: make(map[string]bool),
	}
}

func (r *inMemoryRepo) Create(_ context.Context, sd *StructureDefinition) (*StructureDefinition, error) {
	cp := *sd
	r.data[sd.ID] = &cp
	return &cp, nil
}

func (r *inMemoryRepo) Read(_ context.Context, id string) (*StructureDefinition, error) {
	if r.deleted[id] {
		return nil, ErrGone
	}
	sd, ok := r.data[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *sd
	return &cp, nil
}

func (r *inMemoryRepo) Update(_ context.Context, id string, sd *StructureDefinition) (*StructureDefinition, error) {
	if r.deleted[id] {
		return nil, ErrGone
	}
	if _, ok := r.data[id]; !ok {
		return nil, ErrNotFound
	}
	cp := *sd
	r.data[id] = &cp
	return &cp, nil
}

func (r *inMemoryRepo) Delete(_ context.Context, id string) error {
	if r.deleted[id] {
		return ErrGone
	}
	if _, ok := r.data[id]; !ok {
		return ErrNotFound
	}
	r.deleted[id] = true
	return nil
}

func (r *inMemoryRepo) Search(_ context.Context, params SearchParams) (*SearchResult, error) {
	var results []StructureDefinition
	for id, sd := range r.data {
		if r.deleted[id] {
			continue
		}
		if params.URL != "" && sd.URL != params.URL {
			continue
		}
		results = append(results, *sd)
	}
	return &SearchResult{StructureDefinitions: results, Total: len(results)}, nil
}

func (r *inMemoryRepo) FindByURL(_ context.Context, url, version string) (*StructureDefinition, error) {
	for id, sd := range r.data {
		if r.deleted[id] {
			continue
		}
		if sd.URL == url && (version == "" || sd.Version == version) {
			cp := *sd
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (r *inMemoryRepo) History(_ context.Context, id string) ([]HistoryEntry, error) {
	return nil, ErrNotFound
}

func (r *inMemoryRepo) ReadVersion(_ context.Context, id string, versionID int) (*StructureDefinition, error) {
	return nil, ErrNotFound
}

// Compile-time guard.
var _ Repository = (*inMemoryRepo)(nil)

func validSD() *StructureDefinition {
	return &StructureDefinition{
		ResourceType: "StructureDefinition",
		URL:          "http://example.org/StructureDefinition/MyProfile",
		Name:         "MyProfile",
		Status:       "active",
		Kind:         "resource",
		Type:         "Patient",
	}
}

func TestService_Create_AssignsIDAndMeta(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	sd, err := svc.Create(context.Background(), validSD())
	require.NoError(t, err)
	assert.NotEmpty(t, sd.ID)
	require.NotNil(t, sd.Meta)
	assert.Equal(t, "1", sd.Meta.VersionID)
	assert.Equal(t, "StructureDefinition", sd.ResourceType)
	assert.False(t, sd.CreatedAt.IsZero())
}

func TestService_Create_SetsTimestamps(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	svc := NewService(newInMemoryRepo())
	sd, err := svc.Create(context.Background(), validSD())
	require.NoError(t, err)
	assert.True(t, sd.CreatedAt.After(before), "CreatedAt should be set")
	assert.True(t, sd.UpdatedAt.After(before), "UpdatedAt should be set")
}

func TestService_Create_PreservesExplicitID(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	input := validSD()
	input.ID = "custom-id-123"
	sd, err := svc.Create(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "custom-id-123", sd.ID)
}

func TestService_Create_ValidationFailure_ReturnsUnprocessable(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	sd := &StructureDefinition{} // missing required fields
	_, err := svc.Create(context.Background(), sd)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnprocessable), "Create with missing fields should return ErrUnprocessable")
}

func TestService_Read_ReturnsStoredResource(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	created, err := svc.Create(context.Background(), validSD())
	require.NoError(t, err)

	got, err := svc.Read(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.URL, got.URL)
}

func TestService_Read_EmptyID_ReturnsInvalidInput(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	_, err := svc.Read(context.Background(), "")
	assert.True(t, errors.Is(err, ErrInvalidInput))
}

func TestService_Update_IncrementsMeta(t *testing.T) {
	repo := newInMemoryRepo()
	svc := NewService(repo)
	created, err := svc.Create(context.Background(), validSD())
	require.NoError(t, err)

	update := validSD()
	update.Title = "Updated Title"
	update.Meta = &fhir.Meta{VersionID: "1"}
	updated, err := svc.Update(context.Background(), created.ID, update)
	require.NoError(t, err)
	assert.Equal(t, "Updated Title", updated.Title)
}

func TestService_Update_EmptyID_ReturnsInvalidInput(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	_, err := svc.Update(context.Background(), "", validSD())
	assert.True(t, errors.Is(err, ErrInvalidInput))
}

func TestService_Update_ValidationFailure_ReturnsUnprocessable(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	created, err := svc.Create(context.Background(), validSD())
	require.NoError(t, err)

	bad := &StructureDefinition{Status: "invalid-status"}
	_, err = svc.Update(context.Background(), created.ID, bad)
	assert.True(t, errors.Is(err, ErrUnprocessable))
}

func TestService_Delete_RemovesResource(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	created, err := svc.Create(context.Background(), validSD())
	require.NoError(t, err)

	require.NoError(t, svc.Delete(context.Background(), created.ID))

	_, err = svc.Read(context.Background(), created.ID)
	assert.True(t, errors.Is(err, ErrGone))
}

func TestService_Delete_EmptyID_ReturnsInvalidInput(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	err := svc.Delete(context.Background(), "")
	assert.True(t, errors.Is(err, ErrInvalidInput))
}

func TestService_Search_DefaultsAndCaps(t *testing.T) {
	svc := NewService(newInMemoryRepo())

	// count=0 → default 20
	result, err := svc.Search(context.Background(), SearchParams{Count: 0})
	require.NoError(t, err)
	assert.NotNil(t, result)

	// count > 1000 → capped to 1000
	result, err = svc.Search(context.Background(), SearchParams{Count: 9999})
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestService_FindByURL_EmptyURL_ReturnsInvalidInput(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	_, err := svc.FindByURL(context.Background(), "", "")
	assert.True(t, errors.Is(err, ErrInvalidInput))
}

func TestService_FindByURL_ReturnsStoredResource(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	_, err := svc.Create(context.Background(), validSD())
	require.NoError(t, err)

	got, err := svc.FindByURL(context.Background(), "http://example.org/StructureDefinition/MyProfile", "")
	require.NoError(t, err)
	assert.Equal(t, "MyProfile", got.Name)
}

func TestService_CreateWithMode_Lenient_SkipsVocabulary(t *testing.T) {
	svc := NewService(newInMemoryRepo())
	sd := &StructureDefinition{
		ResourceType: "StructureDefinition",
		URL:          "http://example.org/sd",
		Name:         "X",
		Status:       "bogus-status",
		Type:         "Patient",
	}
	_, err := svc.CreateWithMode(context.Background(), sd, ModeLenient)
	require.NoError(t, err, "lenient mode should allow bogus status")
}
