package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
)

// Every Create/Update/Delete must write an append-only history row in the same transaction.

func TestIntegration_M4_2_HistoryTimeline(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "http://example.org/history/test",
		Status:       "draft",
		Group: []conceptmap.Group{{
			Source: "http://s", Target: "http://t",
			Element: []conceptmap.Element{
				{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}},
			},
		}},
	}

	created, err := repo.Create(ctx, cm)
	require.NoError(t, err)

	upd := *created
	upd.Status = "active"
	_, err = repo.Update(ctx, created.ID, &upd)
	require.NoError(t, err)

	upd.Status = "retired"
	upd.Meta = nil // clear If-Match expectation
	_, err = repo.Update(ctx, created.ID, &upd)
	require.NoError(t, err)

	require.NoError(t, repo.Delete(ctx, created.ID))

	hist, err := repo.History(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, hist, 4)

	assert.Equal(t, "delete", hist[0].Operation)
	assert.Equal(t, 4, hist[0].VersionID)
	assert.Equal(t, "update", hist[1].Operation)
	assert.Equal(t, 3, hist[1].VersionID)
	assert.Equal(t, "update", hist[2].Operation)
	assert.Equal(t, 2, hist[2].VersionID)
	assert.Equal(t, "create", hist[3].Operation)
	assert.Equal(t, 1, hist[3].VersionID)
}

// vread — fetching a specific historical version must return the snapshot
// stored at that version, regardless of subsequent updates.
func TestIntegration_M4_2_Vread_ReturnsExactVersion(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "http://example.org/vread/test",
		Status:       "draft",
		Group: []conceptmap.Group{{
			Source: "http://s", Target: "http://t",
			Element: []conceptmap.Element{
				{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}},
			},
		}},
	}
	created, err := repo.Create(ctx, cm)
	require.NoError(t, err)

	upd := *created
	upd.Status = "active"
	upd.Title = "Renamed in v2"
	_, err = repo.Update(ctx, created.ID, &upd)
	require.NoError(t, err)

	v1, err := repo.ReadVersion(ctx, created.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, "draft", v1.Status, "vread v1 must see the original status, not the post-update value")
	assert.Equal(t, "", v1.Title)

	v2, err := repo.ReadVersion(ctx, created.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, "active", v2.Status)
	assert.Equal(t, "Renamed in v2", v2.Title)
}

// Reading a non-existent version returns ErrNotFound.
func TestIntegration_M4_2_Vread_NotFound(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "http://example.org/vread/notfound",
		Status:       "active",
		Group: []conceptmap.Group{{
			Source: "http://s", Target: "http://t",
			Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}}},
		}},
	}
	created, err := repo.Create(ctx, cm)
	require.NoError(t, err)

	_, err = repo.ReadVersion(ctx, created.ID, 999)
	require.ErrorIs(t, err, conceptmap.ErrNotFound)
}
