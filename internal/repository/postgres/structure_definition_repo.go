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

	"github.com/goodeworkers/fhir-map/internal/domain/structuredefinition"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// StructureDefinitionRepo is the Postgres implementation of structuredefinition.Repository.
// Mirrors StructureMapRepo with extra columns: kind, type, base_definition, derivation.
type StructureDefinitionRepo struct {
	pool *pgxpool.Pool
}

// NewStructureDefinitionRepo wires a StructureDefinitionRepo to its pool.
func NewStructureDefinitionRepo(pool *pgxpool.Pool) *StructureDefinitionRepo {
	return &StructureDefinitionRepo{pool: pool}
}

// Create inserts a fresh StructureDefinition and writes the create-history row in
// the same transaction.
func (r *StructureDefinitionRepo) Create(ctx context.Context, sd *structuredefinition.StructureDefinition) (*structuredefinition.StructureDefinition, error) {
	if sd.ID == "" {
		sd.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	sd.CreatedAt = now
	sd.UpdatedAt = now
	if sd.Meta == nil {
		sd.Meta = &fhir.Meta{}
	}
	sd.Meta.VersionID = "1"
	sd.Meta.LastUpdated = now.Format(time.RFC3339)

	resourceJSON, err := json.Marshal(sd)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal StructureDefinition: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback is a no-op after commit; error unrecoverable in defer context

	_, err = tx.Exec(ctx, `
		INSERT INTO structure_definitions (
			id, url, version, name, title, status, kind, type, base_definition, derivation,
			publisher, description, date, resource_json, version_id, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`,
		sd.ID, sd.URL, sd.Version, sd.Name, sd.Title, sd.Status,
		sd.Kind, sd.Type, sd.BaseDefinition, sd.Derivation,
		sd.Publisher, sd.Description, sd.Date,
		resourceJSON, 1, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert StructureDefinition: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO structure_definition_history (structure_definition_id, version_id, operation, resource_json)
		VALUES ($1, $2, $3, $4)
	`, sd.ID, 1, "create", resourceJSON); err != nil {
		return nil, fmt.Errorf("insert history row: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return sd, nil
}

// Read returns the StructureDefinition by id.
// Returns ErrGone for soft-deleted resources; ErrNotFound when the id has never existed.
func (r *StructureDefinitionRepo) Read(ctx context.Context, id string) (*structuredefinition.StructureDefinition, error) {
	var resourceJSON []byte
	var createdAt, updatedAt time.Time
	var deletedAt *time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT resource_json, created_at, updated_at, deleted_at FROM structure_definitions WHERE id = $1`,
		id,
	).Scan(&resourceJSON, &createdAt, &updatedAt, &deletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, structuredefinition.ErrNotFound
		}
		return nil, fmt.Errorf("read StructureDefinition: %w", err)
	}
	if deletedAt != nil {
		return nil, structuredefinition.ErrGone
	}
	var sd structuredefinition.StructureDefinition
	if err := json.Unmarshal(resourceJSON, &sd); err != nil {
		return nil, fmt.Errorf("unmarshal StructureDefinition: %w", err)
	}
	sd.CreatedAt = createdAt
	sd.UpdatedAt = updatedAt
	return &sd, nil
}

// Update replaces an existing StructureDefinition. Honours If-Match via
// sd.Meta.VersionID (optimistic concurrency — mirrors structuremap_repo.go).
func (r *StructureDefinitionRepo) Update(ctx context.Context, id string, sd *structuredefinition.StructureDefinition) (*structuredefinition.StructureDefinition, error) {
	expectedVersionID := ""
	if sd.Meta != nil {
		expectedVersionID = sd.Meta.VersionID
	}

	var currentVersionID int
	err := r.pool.QueryRow(ctx,
		`SELECT version_id FROM structure_definitions WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(&currentVersionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if existErr := r.pool.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM structure_definitions WHERE id = $1)`, id,
			).Scan(&exists); existErr == nil && exists {
				return nil, structuredefinition.ErrGone
			}
			return nil, structuredefinition.ErrNotFound
		}
		return nil, fmt.Errorf("check existing: %w", err)
	}
	if expectedVersionID != "" && expectedVersionID != strconv.Itoa(currentVersionID) {
		return nil, structuredefinition.ErrConflict
	}

	now := time.Now().UTC()
	newVersionID := currentVersionID + 1
	sd.ID = id
	sd.UpdatedAt = now
	sd.Meta = &fhir.Meta{
		VersionID:   strconv.Itoa(newVersionID),
		LastUpdated: now.Format(time.RFC3339),
	}

	resourceJSON, err := json.Marshal(sd)
	if err != nil {
		return nil, fmt.Errorf("marshal StructureDefinition: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback is a no-op after commit; error unrecoverable in defer context

	tag, err := tx.Exec(ctx, `
		UPDATE structure_definitions SET
			url = $2, version = $3, name = $4, title = $5, status = $6,
			kind = $7, type = $8, base_definition = $9, derivation = $10,
			publisher = $11, description = $12, date = $13,
			resource_json = $14, version_id = $15, updated_at = $16
		WHERE id = $1 AND deleted_at IS NULL
	`,
		id, sd.URL, sd.Version, sd.Name, sd.Title, sd.Status,
		sd.Kind, sd.Type, sd.BaseDefinition, sd.Derivation,
		sd.Publisher, sd.Description, sd.Date,
		resourceJSON, newVersionID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("update StructureDefinition: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, structuredefinition.ErrNotFound
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO structure_definition_history (structure_definition_id, version_id, operation, resource_json)
		VALUES ($1, $2, $3, $4)
	`, id, newVersionID, "update", resourceJSON); err != nil {
		return nil, fmt.Errorf("history row: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return sd, nil
}

// Delete soft-deletes a StructureDefinition and writes the delete-history row.
func (r *StructureDefinitionRepo) Delete(ctx context.Context, id string) error {
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
		`SELECT resource_json, version_id FROM structure_definitions WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(&resourceJSON, &currentVID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if existErr := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM structure_definitions WHERE id = $1)`, id,
			).Scan(&exists); existErr == nil && exists {
				return structuredefinition.ErrGone
			}
			return structuredefinition.ErrNotFound
		}
		return fmt.Errorf("read pre-delete snapshot: %w", err)
	}

	tag, err := tx.Exec(ctx,
		`UPDATE structure_definitions SET deleted_at = $2 WHERE id = $1 AND deleted_at IS NULL`,
		id, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("delete StructureDefinition: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return structuredefinition.ErrGone
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO structure_definition_history (structure_definition_id, version_id, operation, resource_json)
		VALUES ($1, $2, $3, $4)
	`, id, currentVID+1, "delete", resourceJSON); err != nil {
		return fmt.Errorf("history row: %w", err)
	}

	return tx.Commit(ctx)
}

// Search runs a paginated search over StructureDefinition canonical metadata.
func (r *StructureDefinitionRepo) Search(ctx context.Context, params structuredefinition.SearchParams) (*structuredefinition.SearchResult, error) {
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
	if params.Status != "" {
		add("status = $%d", params.Status)
	}
	if params.Kind != "" {
		add("kind = $%d", params.Kind)
	}
	if params.Type != "" {
		add("type = $%d", params.Type)
	}

	where := strings.Join(conditions, " AND ")

	var total int
	if err := r.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM structure_definitions WHERE %s`, where), args...,
	).Scan(&total); err != nil {
		return nil, fmt.Errorf("count: %w", err)
	}

	dataQuery := fmt.Sprintf(
		`SELECT resource_json, created_at, updated_at FROM structure_definitions
		 WHERE %s ORDER BY updated_at DESC LIMIT $%d OFFSET $%d`,
		where, argIdx, argIdx+1,
	)
	args = append(args, params.Count, params.Offset)

	rows, err := r.pool.Query(ctx, dataQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("search StructureDefinitions: %w", err)
	}
	defer rows.Close()

	var results []structuredefinition.StructureDefinition
	for rows.Next() {
		var resourceJSON []byte
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&resourceJSON, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		var sd structuredefinition.StructureDefinition
		if err := json.Unmarshal(resourceJSON, &sd); err != nil {
			return nil, fmt.Errorf("unmarshal: %w", err)
		}
		sd.CreatedAt = createdAt
		sd.UpdatedAt = updatedAt
		results = append(results, sd)
	}
	return &structuredefinition.SearchResult{StructureDefinitions: results, Total: total}, nil
}

// FindByURL resolves a canonical URL to the latest non-deleted StructureDefinition.
func (r *StructureDefinitionRepo) FindByURL(ctx context.Context, url, version string) (*structuredefinition.StructureDefinition, error) {
	var query string
	var args []any
	if version != "" {
		query = `SELECT resource_json, created_at, updated_at FROM structure_definitions
		         WHERE url = $1 AND version = $2 AND deleted_at IS NULL
		         ORDER BY updated_at DESC LIMIT 1`
		args = []any{url, version}
	} else {
		query = `SELECT resource_json, created_at, updated_at FROM structure_definitions
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
				existQuery = `SELECT EXISTS(SELECT 1 FROM structure_definitions WHERE url = $1 AND version = $2)`
				existArgs = []any{url, version}
			} else {
				existQuery = `SELECT EXISTS(SELECT 1 FROM structure_definitions WHERE url = $1)`
				existArgs = []any{url}
			}
			if existErr := r.pool.QueryRow(ctx, existQuery, existArgs...).Scan(&exists); existErr == nil && exists {
				return nil, structuredefinition.ErrGone
			}
			return nil, structuredefinition.ErrNotFound
		}
		return nil, fmt.Errorf("find StructureDefinition by URL: %w", err)
	}
	var sd structuredefinition.StructureDefinition
	if err := json.Unmarshal(resourceJSON, &sd); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	sd.CreatedAt = createdAt
	sd.UpdatedAt = updatedAt
	return &sd, nil
}

// History returns the full timeline of a StructureDefinition, newest-first.
func (r *StructureDefinitionRepo) History(ctx context.Context, id string) ([]structuredefinition.HistoryEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT version_id, operation, occurred_at, resource_json
		FROM structure_definition_history
		WHERE structure_definition_id = $1
		ORDER BY version_id DESC
	`, id)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	var out []structuredefinition.HistoryEntry
	for rows.Next() {
		var entry structuredefinition.HistoryEntry
		var occurredAt time.Time
		var resourceJSON []byte
		if err := rows.Scan(&entry.VersionID, &entry.Operation, &occurredAt, &resourceJSON); err != nil {
			return nil, fmt.Errorf("scan history row: %w", err)
		}
		entry.OccurredAt = occurredAt.UTC().Format(time.RFC3339)
		if len(resourceJSON) > 0 {
			var sd structuredefinition.StructureDefinition
			if err := json.Unmarshal(resourceJSON, &sd); err == nil {
				entry.Resource = &sd
			}
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, structuredefinition.ErrNotFound
	}
	return out, nil
}

// ReadVersion is vread — returns the exact snapshot stored at versionID.
func (r *StructureDefinitionRepo) ReadVersion(ctx context.Context, id string, versionID int) (*structuredefinition.StructureDefinition, error) {
	var resourceJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT resource_json FROM structure_definition_history
		WHERE structure_definition_id = $1 AND version_id = $2
	`, id, versionID).Scan(&resourceJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, structuredefinition.ErrNotFound
		}
		return nil, fmt.Errorf("vread: %w", err)
	}
	var sd structuredefinition.StructureDefinition
	if err := json.Unmarshal(resourceJSON, &sd); err != nil {
		return nil, fmt.Errorf("unmarshal vread snapshot: %w", err)
	}
	return &sd, nil
}

var _ structuredefinition.Repository = (*StructureDefinitionRepo)(nil)
