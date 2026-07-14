package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// StructureMapRepo is the Postgres implementation of structuremap.Repository.
type StructureMapRepo struct {
	pool *pgxpool.Pool
}

// NewStructureMapRepo wires a StructureMapRepo to its pool.
func NewStructureMapRepo(pool *pgxpool.Pool) *StructureMapRepo {
	return &StructureMapRepo{pool: pool}
}

// Create inserts a fresh StructureMap and writes the create-history row in
// the same transaction.
func (r *StructureMapRepo) Create(ctx context.Context, sm *structuremap.StructureMap) (*structuremap.StructureMap, error) {
	if sm.ID == "" {
		sm.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	sm.CreatedAt = now
	sm.UpdatedAt = now
	if sm.Meta == nil {
		sm.Meta = &fhir.Meta{}
	}
	sm.Meta.VersionID = "1"
	sm.Meta.LastUpdated = now.Format(time.RFC3339)

	resourceJSON, err := json.Marshal(sm)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal StructureMap: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback is a no-op after commit; error unrecoverable in defer context

	_, err = tx.Exec(ctx, `
		INSERT INTO structure_maps (
			id, url, version, name, title, status, publisher, description, date,
			resource_json, version_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		sm.ID, sm.URL, sm.Version, sm.Name, sm.Title, sm.Status,
		sm.Publisher, sm.Description, sm.Date,
		resourceJSON, 1, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert StructureMap: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO structure_map_history (structure_map_id, version_id, operation, resource_json)
		VALUES ($1, $2, $3, $4)
	`, sm.ID, 1, "create", resourceJSON); err != nil {
		return nil, fmt.Errorf("insert history row: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return sm, nil
}

// Read returns the StructureMap by id.
// Returns ErrGone for soft-deleted resources (FHIR requires 410, not 404).
// Returns ErrNotFound when the id has never existed.
func (r *StructureMapRepo) Read(ctx context.Context, id string) (*structuremap.StructureMap, error) {
	var resourceJSON []byte
	var createdAt, updatedAt time.Time
	var deletedAt *time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT resource_json, created_at, updated_at, deleted_at FROM structure_maps WHERE id = $1`,
		id,
	).Scan(&resourceJSON, &createdAt, &updatedAt, &deletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, structuremap.ErrNotFound
		}
		return nil, fmt.Errorf("read StructureMap: %w", err)
	}
	if deletedAt != nil {
		return nil, structuremap.ErrGone
	}
	var sm structuremap.StructureMap
	if err := json.Unmarshal(resourceJSON, &sm); err != nil {
		return nil, fmt.Errorf("unmarshal StructureMap: %w", err)
	}
	sm.CreatedAt = createdAt
	sm.UpdatedAt = updatedAt
	return &sm, nil
}

// Update replaces an existing StructureMap with If-Match validation via sm.Meta.VersionID.
func (r *StructureMapRepo) Update(ctx context.Context, id string, sm *structuremap.StructureMap) (*structuremap.StructureMap, error) {
	expectedVersionID := ""
	if sm.Meta != nil {
		expectedVersionID = sm.Meta.VersionID
	}

	var currentVersionID int
	err := r.pool.QueryRow(ctx,
		`SELECT version_id FROM structure_maps WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(&currentVersionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if existErr := r.pool.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM structure_maps WHERE id = $1)`, id,
			).Scan(&exists); existErr == nil && exists {
				return nil, structuremap.ErrGone
			}
			return nil, structuremap.ErrNotFound
		}
		return nil, fmt.Errorf("check existing: %w", err)
	}
	if expectedVersionID != "" && expectedVersionID != strconv.Itoa(currentVersionID) {
		return nil, structuremap.ErrConflict
	}

	now := time.Now().UTC()
	newVersionID := currentVersionID + 1
	sm.ID = id
	sm.UpdatedAt = now
	sm.Meta = &fhir.Meta{
		VersionID:   strconv.Itoa(newVersionID),
		LastUpdated: now.Format(time.RFC3339),
	}

	resourceJSON, err := json.Marshal(sm)
	if err != nil {
		return nil, fmt.Errorf("marshal StructureMap: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback is a no-op after commit; error unrecoverable in defer context

	tag, err := tx.Exec(ctx, `
		UPDATE structure_maps SET
			url = $2, version = $3, name = $4, title = $5, status = $6,
			publisher = $7, description = $8, date = $9,
			resource_json = $10, version_id = $11, updated_at = $12
		WHERE id = $1 AND deleted_at IS NULL
	`,
		id, sm.URL, sm.Version, sm.Name, sm.Title, sm.Status,
		sm.Publisher, sm.Description, sm.Date,
		resourceJSON, newVersionID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("update StructureMap: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, structuremap.ErrNotFound
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO structure_map_history (structure_map_id, version_id, operation, resource_json)
		VALUES ($1, $2, $3, $4)
	`, id, newVersionID, "update", resourceJSON); err != nil {
		return nil, fmt.Errorf("history row: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return sm, nil
}

// Delete soft-deletes a StructureMap and writes the delete-history row.
func (r *StructureMapRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback is a no-op after commit; error unrecoverable in defer context

	var (
		resourceJSON []byte
		currentVID   int
	)
	err = tx.QueryRow(ctx,
		`SELECT resource_json, version_id FROM structure_maps WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(&resourceJSON, &currentVID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if existErr := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM structure_maps WHERE id = $1)`, id,
			).Scan(&exists); existErr == nil && exists {
				return structuremap.ErrGone
			}
			return structuremap.ErrNotFound
		}
		return fmt.Errorf("read pre-delete snapshot: %w", err)
	}

	tag, err := tx.Exec(ctx,
		`UPDATE structure_maps SET deleted_at = $2 WHERE id = $1 AND deleted_at IS NULL`,
		id, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("delete StructureMap: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Concurrent delete raced ahead; snapshot read succeeded but update found row already soft-deleted.
		return structuremap.ErrGone
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO structure_map_history (structure_map_id, version_id, operation, resource_json)
		VALUES ($1, $2, $3, $4)
	`, id, currentVID+1, "delete", resourceJSON); err != nil {
		return fmt.Errorf("history row: %w", err)
	}

	return tx.Commit(ctx)
}

// Search runs a paginated search over StructureMap canonical metadata.
func (r *StructureMapRepo) Search(ctx context.Context, params structuremap.SearchParams) (*structuremap.SearchResult, error) {
	conditions := []string{"deleted_at IS NULL"}
	args := []any{}
	argIdx := 1
	add := func(sql string, val any) {
		conditions = append(conditions, fmt.Sprintf(sql, argIdx))
		args = append(args, val)
		argIdx++
	}
	if params.ID != "" {
		add("id = $%d", params.ID)
	}
	if params.URL != "" {
		add("url = $%d", params.URL)
	}
	if params.Version != "" {
		add("version = $%d", params.Version)
	}
	if params.Name != "" {
		add("name ILIKE $%d", "%"+params.Name+"%")
	}
	if params.Title != "" {
		add("title ILIKE $%d", "%"+params.Title+"%")
	}
	if params.Status != "" {
		add("status = $%d", params.Status)
	}
	if params.Publisher != "" {
		add("publisher ILIKE $%d", "%"+params.Publisher+"%")
	}
	if params.Description != "" {
		add("description ILIKE $%d", "%"+params.Description+"%")
	}

	where := strings.Join(conditions, " AND ")

	var total int
	if err := r.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM structure_maps WHERE %s`, where), args...,
	).Scan(&total); err != nil {
		return nil, fmt.Errorf("count: %w", err)
	}

	dataQuery := fmt.Sprintf(
		`SELECT resource_json, created_at, updated_at FROM structure_maps
		 WHERE %s ORDER BY updated_at DESC LIMIT $%d OFFSET $%d`,
		where, argIdx, argIdx+1,
	)
	args = append(args, params.Count, params.Offset)

	rows, err := r.pool.Query(ctx, dataQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("search StructureMaps: %w", err)
	}
	defer rows.Close()

	var results []structuremap.StructureMap
	for rows.Next() {
		var resourceJSON []byte
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&resourceJSON, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		var sm structuremap.StructureMap
		if err := json.Unmarshal(resourceJSON, &sm); err != nil {
			return nil, fmt.Errorf("unmarshal: %w", err)
		}
		sm.CreatedAt = createdAt
		sm.UpdatedAt = updatedAt
		results = append(results, sm)
	}
	return &structuremap.SearchResult{StructureMaps: results, Total: total}, nil
}

// FindByURL resolves a canonical URL to the latest non-deleted StructureMap.
func (r *StructureMapRepo) FindByURL(ctx context.Context, url, version string) (*structuremap.StructureMap, error) {
	var query string
	var args []any
	if version != "" {
		query = `SELECT resource_json, created_at, updated_at FROM structure_maps
		         WHERE url = $1 AND version = $2 AND deleted_at IS NULL
		         ORDER BY updated_at DESC LIMIT 1`
		args = []any{url, version}
	} else {
		query = `SELECT resource_json, created_at, updated_at FROM structure_maps
		         WHERE url = $1 AND deleted_at IS NULL
		         ORDER BY updated_at DESC LIMIT 1`
		args = []any{url}
	}

	var resourceJSON []byte
	var createdAt, updatedAt time.Time
	err := r.pool.QueryRow(ctx, query, args...).Scan(&resourceJSON, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			var existQuery string
			var existArgs []any
			if version != "" {
				existQuery = `SELECT EXISTS(SELECT 1 FROM structure_maps WHERE url = $1 AND version = $2)`
				existArgs = []any{url, version}
			} else {
				existQuery = `SELECT EXISTS(SELECT 1 FROM structure_maps WHERE url = $1)`
				existArgs = []any{url}
			}
			if existErr := r.pool.QueryRow(ctx, existQuery, existArgs...).Scan(&exists); existErr == nil && exists {
				return nil, structuremap.ErrGone
			}
			return nil, structuremap.ErrNotFound
		}
		return nil, fmt.Errorf("find StructureMap by URL: %w", err)
	}
	var sm structuremap.StructureMap
	if err := json.Unmarshal(resourceJSON, &sm); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	sm.CreatedAt = createdAt
	sm.UpdatedAt = updatedAt
	return &sm, nil
}

// History returns the full timeline of a StructureMap, newest-first.
func (r *StructureMapRepo) History(ctx context.Context, id string) ([]structuremap.HistoryEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT version_id, operation, occurred_at, resource_json
		FROM structure_map_history
		WHERE structure_map_id = $1
		ORDER BY version_id DESC
	`, id)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	var out []structuremap.HistoryEntry
	for rows.Next() {
		var entry structuremap.HistoryEntry
		var occurredAt time.Time
		var resourceJSON []byte
		if err := rows.Scan(&entry.VersionID, &entry.Operation, &occurredAt, &resourceJSON); err != nil {
			return nil, fmt.Errorf("scan history row: %w", err)
		}
		entry.OccurredAt = occurredAt.UTC().Format(time.RFC3339)
		if len(resourceJSON) > 0 {
			var sm structuremap.StructureMap
			if err := json.Unmarshal(resourceJSON, &sm); err == nil {
				entry.Resource = &sm
			}
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, structuremap.ErrNotFound
	}
	return out, nil
}

// ReadVersion is vread — returns the exact snapshot stored at versionID.
func (r *StructureMapRepo) ReadVersion(ctx context.Context, id string, versionID int) (*structuremap.StructureMap, error) {
	var resourceJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT resource_json FROM structure_map_history
		WHERE structure_map_id = $1 AND version_id = $2
	`, id, versionID).Scan(&resourceJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, structuremap.ErrNotFound
		}
		return nil, fmt.Errorf("vread: %w", err)
	}
	var sm structuremap.StructureMap
	if err := json.Unmarshal(resourceJSON, &sm); err != nil {
		return nil, fmt.Errorf("unmarshal vread snapshot: %w", err)
	}
	return &sm, nil
}

// Compile-time guard: StructureMapRepo satisfies structuremap.Repository.
var _ structuremap.Repository = (*StructureMapRepo)(nil)
