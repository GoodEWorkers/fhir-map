package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// setupTestDB creates a PostgreSQL container and returns a connected pool; skips in short mode.
func setupTestDB(t testing.TB) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	pgContainer, err := pgmodule.Run(ctx,
		"postgres:16-alpine",
		pgmodule.WithDatabase("fhir_test"),
		pgmodule.WithUsername("test"),
		pgmodule.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err)

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)

	// Run every up-migration in lexical order so all tables exist together.
	matches, err := filepath.Glob(filepath.Join("migrations", "*.up.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, matches, "no migrations found under migrations/")
	// filepath.Glob already returns sorted order on case-sensitive filesystems,
	// but be explicit so 002 always follows 001.
	sort.Strings(matches)
	for _, m := range matches {
		sqlBytes, err := os.ReadFile(m)
		require.NoError(t, err, "read migration %s", m)
		_, err = pool.Exec(ctx, string(sqlBytes))
		require.NoErrorf(t, err, "apply migration %s", m)
	}

	cleanup := func() {
		pool.Close()
		pgContainer.Terminate(ctx)
	}

	return pool, cleanup
}

func TestIntegration_CRUD(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "http://example.org/fhir/ConceptMap/integration-test",
		Version:      "1.0.0",
		Name:         "IntegrationTestMap",
		Title:        "Integration Test ConceptMap",
		Status:       "draft",
		Description:  "A concept map for integration testing",
		Group: []conceptmap.Group{
			{
				Source: "http://source.example.org",
				Target: "http://target.example.org",
				Element: []conceptmap.Element{
					{
						Code:    "src-1",
						Display: "Source One",
						Target: []conceptmap.Target{
							{Code: "tgt-1", Display: "Target One", Relationship: "equivalent"},
						},
					},
					{
						Code:    "src-2",
						Display: "Source Two",
						Target: []conceptmap.Target{
							{Code: "tgt-2", Display: "Target Two", Relationship: "source-is-broader-than-target"},
							{Code: "tgt-3", Display: "Target Three", Relationship: "source-is-broader-than-target"},
						},
					},
				},
			},
		},
	}

	created, err := repo.Create(ctx, cm)
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, "1", created.Meta.VersionID)
	assert.NotEmpty(t, created.Meta.LastUpdated)

	read, err := repo.Read(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, read.ID)
	assert.Equal(t, "IntegrationTestMap", read.Name)
	assert.Equal(t, "draft", read.Status)
	assert.Len(t, read.Group, 1)
	assert.Len(t, read.Group[0].Element, 2)

	read.Status = "active"
	read.Name = "UpdatedMap"
	updated, err := repo.Update(ctx, read.ID, read)
	require.NoError(t, err)
	assert.Equal(t, "active", updated.Status)
	assert.Equal(t, "UpdatedMap", updated.Name)
	assert.Equal(t, "2", updated.Meta.VersionID)

	err = repo.Delete(ctx, updated.ID)
	require.NoError(t, err)

	// FHIR spec: Read after soft-delete must return ErrGone (410), not ErrNotFound (404)
	_, err = repo.Read(ctx, updated.ID)
	assert.ErrorIs(t, err, conceptmap.ErrGone)
}

func TestIntegration_Read_NeverExisted_ReturnsErrNotFound(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewConceptMapRepo(pool)

	_, err := repo.Read(context.Background(), "00000000-0000-0000-0000-000000000000")
	assert.ErrorIs(t, err, conceptmap.ErrNotFound, "unknown id must return ErrNotFound, not ErrGone")
}

func TestIntegration_Search_BySourceSystem(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	icd10 := "http://hl7.org/fhir/sid/icd-10"
	snomed := "http://snomed.info/sct"

	_, err := repo.Create(ctx, &conceptmap.ConceptMap{
		Status: "active",
		Group:  []conceptmap.Group{{Source: icd10, Target: snomed, Element: []conceptmap.Element{{Code: "J18.9", Target: []conceptmap.Target{{Code: "233604007", Relationship: "equivalent"}}}}}},
	})
	require.NoError(t, err)

	_, err = repo.Create(ctx, &conceptmap.ConceptMap{
		Status: "active",
		Group:  []conceptmap.Group{{Source: snomed, Target: icd10, Element: []conceptmap.Element{{Code: "233604007", Target: []conceptmap.Target{{Code: "J18.9", Relationship: "equivalent"}}}}}},
	})
	require.NoError(t, err)

	result, err := repo.Search(ctx, conceptmap.SearchParams{SourceGroupSystem: icd10, Count: 20})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total, "only the ConceptMap with source system icd-10 should match")
	assert.Equal(t, icd10, result.ConceptMaps[0].Group[0].Source)
}

func TestIntegration_Search(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	maps := []*conceptmap.ConceptMap{
		{
			ResourceType: "ConceptMap",
			URL:          "http://example.org/cm/1",
			Name:         "MapOne",
			Status:       "active",
			Meta:         &fhir.Meta{VersionID: "1"},
			Group: []conceptmap.Group{
				{
					Source: "http://system-a",
					Target: "http://system-b",
					Element: []conceptmap.Element{
						{Code: "alpha", Target: []conceptmap.Target{{Code: "beta", Relationship: "equivalent"}}},
					},
				},
			},
		},
		{
			ResourceType: "ConceptMap",
			URL:          "http://example.org/cm/2",
			Name:         "MapTwo",
			Status:       "draft",
			Meta:         &fhir.Meta{VersionID: "1"},
			Group: []conceptmap.Group{
				{
					Source: "http://system-c",
					Target: "http://system-d",
					Element: []conceptmap.Element{
						{Code: "gamma", Target: []conceptmap.Target{{Code: "delta", Relationship: "equivalent"}}},
					},
				},
			},
		},
		{
			ResourceType: "ConceptMap",
			URL:          "http://example.org/cm/3",
			Name:         "MapThree",
			Status:       "active",
			Meta:         &fhir.Meta{VersionID: "1"},
			Group: []conceptmap.Group{
				{
					Source: "http://system-a",
					Target: "http://system-d",
					Element: []conceptmap.Element{
						{Code: "alpha", Target: []conceptmap.Target{{Code: "epsilon", Relationship: "related-to"}}},
					},
				},
			},
		},
	}

	for _, cm := range maps {
		_, err := repo.Create(ctx, cm)
		require.NoError(t, err)
	}

	result, err := repo.Search(ctx, conceptmap.SearchParams{Status: "active", Count: 20})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)

	result, err = repo.Search(ctx, conceptmap.SearchParams{URL: "http://example.org/cm/2", Count: 20})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total)
	assert.Equal(t, "MapTwo", result.ConceptMaps[0].Name)

	result, err = repo.Search(ctx, conceptmap.SearchParams{SourceCode: "alpha", Count: 20})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)

	result, err = repo.Search(ctx, conceptmap.SearchParams{TargetCode: "delta", Count: 20})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total)

	result, err = repo.Search(ctx, conceptmap.SearchParams{SourceGroupSystem: "http://system-a", Count: 20})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)

	result, err = repo.Search(ctx, conceptmap.SearchParams{TargetGroupSystem: "http://system-d", Count: 20})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)

	result, err = repo.Search(ctx, conceptmap.SearchParams{Name: "Two", Count: 20})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total)

	result, err = repo.Search(ctx, conceptmap.SearchParams{Count: 1, Offset: 0})
	require.NoError(t, err)
	assert.Equal(t, 3, result.Total) // Total always full count
	assert.Len(t, result.ConceptMaps, 1)
}

func TestIntegration_FindByURL(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          "http://example.org/fhir/ConceptMap/findable",
		Version:      "2.0.0",
		Status:       "active",
		Meta:         &fhir.Meta{VersionID: "1"},
		Group: []conceptmap.Group{
			{Element: []conceptmap.Element{{Code: "x", Target: []conceptmap.Target{{Code: "y", Relationship: "equivalent"}}}}},
		},
	}
	_, err := repo.Create(ctx, cm)
	require.NoError(t, err)

	found, err := repo.FindByURL(ctx, "http://example.org/fhir/ConceptMap/findable", "")
	require.NoError(t, err)
	assert.Equal(t, "active", found.Status)

	found, err = repo.FindByURL(ctx, "http://example.org/fhir/ConceptMap/findable", "2.0.0")
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", found.Version)

	_, err = repo.FindByURL(ctx, "http://nonexistent", "")
	assert.ErrorIs(t, err, conceptmap.ErrNotFound)
}

func TestIntegration_LoadAllFHIRExamples(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	examplesDir := filepath.Join("..", "..", "..", "docs")
	examples, err := filepath.Glob(filepath.Join(examplesDir, "conceptmap-*.json"))
	require.NoError(t, err)
	require.Len(t, examples, 8, "expected 8 FHIR ConceptMap examples in docs/")

	for _, path := range examples {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)

			var cm conceptmap.ConceptMap
			err = json.Unmarshal(data, &cm)
			require.NoError(t, err)
			assert.Equal(t, "ConceptMap", cm.ResourceType)

			created, err := repo.Create(ctx, &cm)
			require.NoError(t, err)
			assert.NotEmpty(t, created.ID)

			retrieved, err := repo.Read(ctx, created.ID)
			require.NoError(t, err)
			assert.Equal(t, cm.Name, retrieved.Name)
			assert.Equal(t, cm.Status, retrieved.Status)

			// If it has a URL, find by URL
			if cm.URL != "" {
				found, err := repo.FindByURL(ctx, cm.URL, "")
				require.NoError(t, err)
				assert.Equal(t, cm.ID, found.ID)
			}
		})
	}
}

func TestIntegration_LargeConceptMap_SpecimenType(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "conceptmap-example-specimen-type-102.json"))
	require.NoError(t, err)

	var cm conceptmap.ConceptMap
	err = json.Unmarshal(data, &cm)
	require.NoError(t, err)

	created, err := repo.Create(ctx, &cm)
	require.NoError(t, err)

	result, err := repo.Search(ctx, conceptmap.SearchParams{SourceCode: "ACNE", Count: 20})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total)
	assert.Equal(t, created.ID, result.ConceptMaps[0].ID)
}

// Both goroutines read version_id=1 via MVCC (no blocking), but Postgres
// row-level locking serialises the two UPDATEs so the loser's WHERE matches
// zero rows and surfaces as ErrConflict.
func TestIntegration_Update_ConcurrentPUT_Returns409(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, &conceptmap.ConceptMap{
		Status: "active",
		Group: []conceptmap.Group{{
			Source: "http://s", Target: "http://t",
			Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}}},
		}},
	})
	require.NoError(t, err)

	// Acquire the row lock that both Update calls will block on.
	blocker, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = blocker.Exec(ctx, `SELECT pk FROM concept_maps WHERE id = $1 FOR UPDATE`, created.ID)
	require.NoError(t, err)

	var (
		wg         sync.WaitGroup
		successes  atomic.Int32
		conflicts  atomic.Int32
		unexpected []error
		unexpectMu sync.Mutex
	)
	spawn := func(label string) {
		defer wg.Done()
		update := *created
		update.Name = "concurrent-" + label
		update.Meta = nil // no If-Match — pure unconditional PUT
		_, uerr := repo.Update(ctx, created.ID, &update)
		switch {
		case uerr == nil:
			successes.Add(1)
		case errors.Is(uerr, conceptmap.ErrConflict):
			conflicts.Add(1)
		default:
			unexpectMu.Lock()
			unexpected = append(unexpected, fmt.Errorf("goroutine %s: %w", label, uerr))
			unexpectMu.Unlock()
		}
	}
	wg.Add(2)
	go spawn("a")
	go spawn("b")

	// Give both goroutines time to complete their SELECT and queue at the
	// UPDATE row lock. 200ms is generous; the SELECT-to-UPDATE gap is ~µs.
	time.Sleep(200 * time.Millisecond)

	// Release the lock; both UPDATEs race. Postgres' row-level write lock
	// serialises them; the loser's WHERE clause no longer matches.
	require.NoError(t, blocker.Rollback(ctx))
	wg.Wait()

	unexpectMu.Lock()
	defer unexpectMu.Unlock()
	require.Empty(t, unexpected, "unexpected error from racing goroutines: %v", unexpected)
	assert.Equal(t, int32(1), successes.Load(), "exactly one PUT must win")
	assert.Equal(t, int32(1), conflicts.Load(), "the other PUT must see ErrConflict")
}

// Run via: go test -bench=BenchmarkCreate_LargeConceptMap -benchtime=1x ./internal/repository/postgres/
// CI enforces the 2s target without -race in the benchmark-perf workflow job.
func BenchmarkCreate_LargeConceptMap(b *testing.B) {
	pool, cleanup := setupTestDB(b)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	const n = 100_000
	elements := make([]conceptmap.Element, n)
	for i := range elements {
		elements[i] = conceptmap.Element{
			Code:   fmt.Sprintf("SRC-%d", i),
			Target: []conceptmap.Target{{Code: fmt.Sprintf("TGT-%d", i), Relationship: "equivalent"}},
		}
	}

	b.ResetTimer()
	for i := range b.N {
		cm := &conceptmap.ConceptMap{
			Status: "active",
			URL:    fmt.Sprintf("http://example.org/large-%d", i),
			Group:  []conceptmap.Group{{Source: "http://src", Target: "http://tgt", Element: elements}},
		}
		if _, err := repo.Create(ctx, cm); err != nil {
			b.Fatal(err)
		}
	}
}

func TestIntegration_FindByURL_ReturnsLatestByDefault(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	const url = "http://example.org/cm/multi-version"
	v1, err := repo.Create(ctx, &conceptmap.ConceptMap{
		URL: url, Version: "1.0", Status: "active",
		Group: []conceptmap.Group{{Source: "http://s", Target: "http://t",
			Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}}}}},
	})
	require.NoError(t, err)
	// FindByURL orders by updated_at DESC, so a stable monotonic gap makes
	// the test deterministic on fast CI hardware (otherwise both rows can
	// share the same wall-clock second).
	time.Sleep(10 * time.Millisecond)
	v2, err := repo.Create(ctx, &conceptmap.ConceptMap{
		URL: url, Version: "2.0", Status: "active",
		Group: []conceptmap.Group{{Source: "http://s", Target: "http://t",
			Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B2", Relationship: "equivalent"}}}}}},
	})
	require.NoError(t, err)

	readV1, err := repo.Read(ctx, v1.ID)
	require.NoError(t, err)
	assert.Equal(t, "1.0", readV1.Version)
	readV2, err := repo.Read(ctx, v2.ID)
	require.NoError(t, err)
	assert.Equal(t, "2.0", readV2.Version)

	found, err := repo.FindByURL(ctx, url, "")
	require.NoError(t, err)
	assert.Equal(t, "2.0", found.Version, "FindByURL with no version pin must return the latest")
}

func TestIntegration_FindByURL_PinnedVersion(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	const url = "http://example.org/cm/pinned"
	_, err := repo.Create(ctx, &conceptmap.ConceptMap{
		URL: url, Version: "1.0", Status: "active",
		Group: []conceptmap.Group{{Source: "http://s", Target: "http://t",
			Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}}}}},
	})
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	_, err = repo.Create(ctx, &conceptmap.ConceptMap{
		URL: url, Version: "2.0", Status: "active",
		Group: []conceptmap.Group{{Source: "http://s", Target: "http://t",
			Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B2", Relationship: "equivalent"}}}}}},
	})
	require.NoError(t, err)

	pinned, err := repo.FindByURL(ctx, url, "1.0")
	require.NoError(t, err)
	assert.Equal(t, "1.0", pinned.Version, "explicit version pin must return that exact version")
}

func TestIntegration_TranslateFallsBackOnDeletedLatest(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	const url = "http://example.org/cm/fallback"
	_, err := repo.Create(ctx, &conceptmap.ConceptMap{
		URL: url, Version: "1.0", Status: "active",
		Group: []conceptmap.Group{{Source: "http://s", Target: "http://t",
			Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B-v1", Relationship: "equivalent"}}}}}},
	})
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	v2, err := repo.Create(ctx, &conceptmap.ConceptMap{
		URL: url, Version: "2.0", Status: "active",
		Group: []conceptmap.Group{{Source: "http://s", Target: "http://t",
			Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B-v2", Relationship: "equivalent"}}}}}},
	})
	require.NoError(t, err)

	latest, err := repo.FindByURL(ctx, url, "")
	require.NoError(t, err)
	require.Equal(t, "2.0", latest.Version)

	// deleted_at IS NULL in FindByURL skips the tombstone, so v1 becomes the latest.
	require.NoError(t, repo.Delete(ctx, v2.ID))
	fallback, err := repo.FindByURL(ctx, url, "")
	require.NoError(t, err)
	assert.Equal(t, "1.0", fallback.Version, "after deleting the latest, the previous version must be served")
	require.Len(t, fallback.Group, 1)
	require.Len(t, fallback.Group[0].Element, 1)
	require.Len(t, fallback.Group[0].Element[0].Target, 1)
	assert.Equal(t, "B-v1", fallback.Group[0].Element[0].Target[0].Code,
		"the v1 target payload must surface once v2 is tombstoned")
}

func TestIntegration_TranslateNoVersionsLeft_ReturnsErrNotFound(t *testing.T) {
	pool, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewConceptMapRepo(pool)
	ctx := context.Background()

	const url = "http://example.org/cm/exhausted"
	only, err := repo.Create(ctx, &conceptmap.ConceptMap{
		URL: url, Version: "1.0", Status: "active",
		Group: []conceptmap.Group{{Source: "http://s", Target: "http://t",
			Element: []conceptmap.Element{{Code: "A", Target: []conceptmap.Target{{Code: "B", Relationship: "equivalent"}}}}}},
	})
	require.NoError(t, err)
	require.NoError(t, repo.Delete(ctx, only.ID))

	_, err = repo.FindByURL(ctx, url, "")
	assert.ErrorIs(t, err, conceptmap.ErrNotFound,
		"once every version at a URL is tombstoned, FindByURL must return ErrNotFound — the handler converts that into result=false (AC-4)")
}
