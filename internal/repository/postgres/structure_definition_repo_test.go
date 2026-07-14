//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuredefinition"
)

func sampleStructureDefinition() *structuredefinition.StructureDefinition {
	return &structuredefinition.StructureDefinition{
		ResourceType:   "StructureDefinition",
		URL:            "http://example.org/StructureDefinition/sample",
		Name:           "Sample",
		Status:         "active",
		Kind:           "resource",
		Type:           "Patient",
		BaseDefinition: "http://hl7.org/fhir/StructureDefinition/Patient",
		Derivation:     "constraint",
	}
}

func TestStructureDefinitionRepo_CRUD(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureDefinitionRepo(pool)
	ctx := context.Background()

	sd := sampleStructureDefinition()
	created, err := repo.Create(ctx, sd)
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	assert.Equal(t, "1", created.Meta.VersionID)

	read, err := repo.Read(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "Sample", read.Name)
	assert.Equal(t, "Patient", read.Type)
	assert.Equal(t, "constraint", read.Derivation)

	upd := *read
	upd.Title = "Renamed"
	upd.Version = "2.0.0"
	updated, err := repo.Update(ctx, created.ID, &upd)
	require.NoError(t, err)
	assert.Equal(t, "2", updated.Meta.VersionID)
	assert.Equal(t, "Renamed", updated.Title)

	require.NoError(t, repo.Delete(ctx, created.ID))

	_, err = repo.Read(ctx, created.ID)
	assert.ErrorIs(t, err, structuredefinition.ErrGone)
}

func TestStructureDefinitionRepo_History(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureDefinitionRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureDefinition())
	require.NoError(t, err)

	upd := *created
	upd.Title = "Update 1"
	_, err = repo.Update(ctx, created.ID, &upd)
	require.NoError(t, err)

	upd.Title = "Update 2"
	upd.Meta = nil
	_, err = repo.Update(ctx, created.ID, &upd)
	require.NoError(t, err)

	require.NoError(t, repo.Delete(ctx, created.ID))

	hist, err := repo.History(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, hist, 4)
	assert.Equal(t, "delete", hist[0].Operation)
	assert.Equal(t, 4, hist[0].VersionID)
	assert.Equal(t, "create", hist[3].Operation)
}

func TestStructureDefinitionRepo_FindByURL(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureDefinitionRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureDefinition())
	require.NoError(t, err)

	found, err := repo.FindByURL(ctx, created.URL, "")
	require.NoError(t, err)
	assert.Equal(t, created.ID, found.ID)

	_, err = repo.FindByURL(ctx, "http://example.org/unknown", "")
	assert.ErrorIs(t, err, structuredefinition.ErrNotFound)
}

func TestStructureDefinitionRepo_OptimisticConcurrency(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureDefinitionRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureDefinition())
	require.NoError(t, err)

	upd := *created
	upd.Title = "v2"
	_, err = repo.Update(ctx, created.ID, &upd)
	require.NoError(t, err)

	// Stale If-Match: still at VersionID="1" but server is on v2.
	stale := *created
	stale.Title = "stale-write"
	_, err = repo.Update(ctx, created.ID, &stale)
	assert.ErrorIs(t, err, structuredefinition.ErrConflict)
}

func TestStructureDefinitionRepo_Read_AfterDelete_ReturnsErrGone(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureDefinitionRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureDefinition())
	require.NoError(t, err)

	require.NoError(t, repo.Delete(ctx, created.ID))

	_, err = repo.Read(ctx, created.ID)
	assert.ErrorIs(t, err, structuredefinition.ErrGone)
}

func TestStructureDefinitionRepo_Update_OnDeleted_ReturnsErrGone(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureDefinitionRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureDefinition())
	require.NoError(t, err)
	require.NoError(t, repo.Delete(ctx, created.ID))

	upd := sampleStructureDefinition()
	upd.ID = created.ID
	_, err = repo.Update(ctx, created.ID, upd)
	assert.ErrorIs(t, err, structuredefinition.ErrGone)
}

func TestStructureDefinitionRepo_Delete_AlreadyDeleted_ReturnsErrGone(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureDefinitionRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureDefinition())
	require.NoError(t, err)
	require.NoError(t, repo.Delete(ctx, created.ID))

	err = repo.Delete(ctx, created.ID)
	assert.ErrorIs(t, err, structuredefinition.ErrGone)
}

func TestStructureDefinitionRepo_FindByURL_OnDeleted_ReturnsErrGone(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureDefinitionRepo(pool)
	ctx := context.Background()

	sample := sampleStructureDefinition()
	canonicalURL := sample.URL
	created, err := repo.Create(ctx, sample)
	require.NoError(t, err)
	require.NoError(t, repo.Delete(ctx, created.ID))

	_, err = repo.FindByURL(ctx, canonicalURL, "")
	assert.ErrorIs(t, err, structuredefinition.ErrGone)
}
