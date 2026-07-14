//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

// sampleStructureMap is a minimal valid StructureMap used across these tests.
func sampleStructureMap() *structuremap.StructureMap {
	return &structuremap.StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/StructureMap/sample",
		Name:         "Sample",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name:   "r1",
				Source: []structuremap.Source{{Context: "src"}},
				Target: []structuremap.Target{{Context: "tgt", Transform: "copy"}},
			}},
		}},
	}
}

func TestIntegration_M5a_StructureMap_CRUD(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureMapRepo(pool)
	ctx := context.Background()

	sm := sampleStructureMap()
	created, err := repo.Create(ctx, sm)
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	assert.Equal(t, "1", created.Meta.VersionID)

	read, err := repo.Read(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "Sample", read.Name)
	assert.Equal(t, "active", read.Status)
	assert.Equal(t, 1, len(read.Group))
	assert.Equal(t, "copy", read.Group[0].Rule[0].Target[0].Transform)

	upd := *read
	upd.Title = "Renamed"
	upd.Version = "2.0.0"
	updated, err := repo.Update(ctx, created.ID, &upd)
	require.NoError(t, err)
	assert.Equal(t, "2", updated.Meta.VersionID)

	require.NoError(t, repo.Delete(ctx, created.ID))

	_, err = repo.Read(ctx, created.ID)
	assert.ErrorIs(t, err, structuremap.ErrGone)
}

func TestIntegration_M5a_StructureMap_HistoryTimeline(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureMapRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureMap())
	require.NoError(t, err)

	upd := *created
	upd.Title = "Update 1"
	_, err = repo.Update(ctx, created.ID, &upd)
	require.NoError(t, err)

	upd.Title = "Update 2"
	upd.Meta = nil // skip If-Match
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

func TestIntegration_M5a_StructureMap_Vread(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureMapRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureMap())
	require.NoError(t, err)

	upd := *created
	upd.Title = "v2-snapshot"
	_, err = repo.Update(ctx, created.ID, &upd)
	require.NoError(t, err)

	v1, err := repo.ReadVersion(ctx, created.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, "", v1.Title, "v1 had no title; must not see v2 update")

	v2, err := repo.ReadVersion(ctx, created.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, "v2-snapshot", v2.Title)

	_, err = repo.ReadVersion(ctx, created.ID, 99)
	assert.ErrorIs(t, err, structuremap.ErrNotFound)
}

func TestIntegration_M5a_StructureMap_Search(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureMapRepo(pool)
	ctx := context.Background()

	a := sampleStructureMap()
	a.URL = "http://example.org/StructureMap/a"
	a.Name = "Alpha"
	_, err := repo.Create(ctx, a)
	require.NoError(t, err)

	b := sampleStructureMap()
	b.URL = "http://example.org/StructureMap/b"
	b.Name = "Beta"
	b.Status = "draft"
	_, err = repo.Create(ctx, b)
	require.NoError(t, err)

	res, err := repo.Search(ctx, structuremap.SearchParams{URL: a.URL, Count: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Total)
	assert.Equal(t, "Alpha", res.StructureMaps[0].Name)

	res, err = repo.Search(ctx, structuremap.SearchParams{Status: "active", Count: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Total)
}

func TestIntegration_M5a_StructureMap_OptimisticConcurrency(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureMapRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureMap())
	require.NoError(t, err)

	upd := *created
	upd.Title = "v2"
	_, err = repo.Update(ctx, created.ID, &upd)
	require.NoError(t, err)

	stale := *created
	stale.Title = "stale-write"
	// Stale Meta.VersionID="1" triggers ErrConflict when server is on v2.
	_, err = repo.Update(ctx, created.ID, &stale)
	assert.ErrorIs(t, err, structuremap.ErrConflict)
}

// TestIntegration_M2_StructureMap_Read_AfterDelete_ReturnsErrGone verifies soft-deleted StructureMap returns ErrGone (FHIR 410 Gone), not ErrNotFound.
func TestIntegration_M2_StructureMap_Read_AfterDelete_ReturnsErrGone(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureMapRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureMap())
	require.NoError(t, err)

	require.NoError(t, repo.Delete(ctx, created.ID))

	_, err = repo.Read(ctx, created.ID)
	assert.ErrorIs(t, err, structuremap.ErrGone, "Read after Delete must return ErrGone (not ErrNotFound)")
}

// TestIntegration_StructureMap_Update_OnDeleted_ReturnsErrGone verifies Update on tombstoned id returns ErrGone (not ErrNotFound), preventing PK-collision on upsert.
func TestIntegration_StructureMap_Update_OnDeleted_ReturnsErrGone(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureMapRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureMap())
	require.NoError(t, err)
	require.NoError(t, repo.Delete(ctx, created.ID))

	upd := sampleStructureMap()
	upd.ID = created.ID
	upd.Name = "ResurrectAttempt"
	_, err = repo.Update(ctx, created.ID, upd)
	assert.ErrorIs(t, err, structuremap.ErrGone,
		"Update against tombstoned id must return ErrGone (not ErrNotFound — that would trigger handler upsert and PK-collide)")
}

// TestIntegration_StructureMap_Delete_AlreadyDeleted_ReturnsErrGone verifies repeat Delete on tombstone returns ErrGone for idempotent 204 (not 404).
func TestIntegration_StructureMap_Delete_AlreadyDeleted_ReturnsErrGone(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureMapRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sampleStructureMap())
	require.NoError(t, err)
	require.NoError(t, repo.Delete(ctx, created.ID))

	err = repo.Delete(ctx, created.ID)
	assert.ErrorIs(t, err, structuremap.ErrGone, "repeat Delete on tombstone must return ErrGone for idempotent 204")
}

// TestIntegration_StructureMap_FindByURL_OnDeleted_ReturnsErrGone verifies FindByURL on tombstoned URL returns ErrGone so $transform can produce 410.
func TestIntegration_StructureMap_FindByURL_OnDeleted_ReturnsErrGone(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewStructureMapRepo(pool)
	ctx := context.Background()

	sample := sampleStructureMap()
	canonicalURL := sample.URL
	require.NotEmpty(t, canonicalURL, "sampleStructureMap must set a URL")

	created, err := repo.Create(ctx, sample)
	require.NoError(t, err)
	require.NoError(t, repo.Delete(ctx, created.ID))

	_, err = repo.FindByURL(ctx, canonicalURL, "")
	assert.ErrorIs(t, err, structuremap.ErrGone, "FindByURL against tombstoned URL must return ErrGone (not ErrNotFound)")
}
