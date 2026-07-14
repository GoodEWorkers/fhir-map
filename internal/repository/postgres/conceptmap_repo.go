// Package postgres provides a PostgreSQL implementation of the ConceptMap repository.
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

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

var conceptMapMappingsTable = pgx.Identifier{"concept_map_mappings"}

// writeHistoryEntry appends a versioned snapshot to concept_map_history within the supplied transaction.
func writeHistoryEntry(ctx context.Context, tx pgx.Tx, conceptMapID string, versionID int, operation string, resourceJSON []byte) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO concept_map_history (concept_map_id, version_id, operation, resource_json)
		VALUES ($1, $2, $3, $4)
	`, conceptMapID, versionID, operation, resourceJSON)
	if err != nil {
		return fmt.Errorf("failed to write history entry: %w", err)
	}
	return nil
}

// ConceptMapRepo implements conceptmap.Repository using PostgreSQL.
type ConceptMapRepo struct {
	pool *pgxpool.Pool
}

// NewConceptMapRepo creates a new PostgreSQL-backed ConceptMap repository.
func NewConceptMapRepo(pool *pgxpool.Pool) *ConceptMapRepo {
	return &ConceptMapRepo{pool: pool}
}

// Create stores a new ConceptMap and returns it with server-assigned fields.
func (r *ConceptMapRepo) Create(ctx context.Context, cm *conceptmap.ConceptMap) (*conceptmap.ConceptMap, error) {
	if cm.ID == "" {
		cm.ID = uuid.New().String()
	}

	now := time.Now().UTC()
	cm.CreatedAt = now
	cm.UpdatedAt = now

	if cm.Meta == nil {
		cm.Meta = &fhir.Meta{}
	}
	cm.Meta.VersionID = "1"
	cm.Meta.LastUpdated = now.Format(time.RFC3339)

	resourceJSON, err := json.Marshal(cm)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ConceptMap: %w", err)
	}

	sourceCodes, targetCodes := extractCodes(cm)

	query := `
		INSERT INTO concept_maps (
			id, url, version, name, title, status, publisher, description, date,
			source_scope_uri, source_scope_canonical, target_scope_uri, target_scope_canonical,
			source_codes, target_codes, source_systems, target_systems,
			resource_json, version_id, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13,
			$14, $15, $16, $17,
			$18, $19, $20, $21
		)
		RETURNING pk`

	sourceSystems, targetSystems := extractSystems(cm)

	// Insert into concept_maps and populate the flat mapping table in the same
	// transaction to ensure consistent state between the main table and flat denormalization.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if Commit succeeded

	var conceptMapPK int64
	err = tx.QueryRow(ctx, query,
		cm.ID, cm.URL, cm.Version, cm.Name, cm.Title, cm.Status, cm.Publisher, cm.Description, cm.Date,
		cm.SourceScopeURI, cm.SourceScopeCanonical, cm.TargetScopeURI, cm.TargetScopeCanonical,
		sourceCodes, targetCodes, sourceSystems, targetSystems,
		resourceJSON, 1, now, now,
	).Scan(&conceptMapPK)
	if err != nil {
		return nil, fmt.Errorf("failed to insert ConceptMap: %w", err)
	}

	if err := writeFlatMappings(ctx, tx, conceptMapPK, cm); err != nil {
		return nil, err
	}

	if err := writeHistoryEntry(ctx, tx, cm.ID, 1, "create", resourceJSON); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit ConceptMap insert: %w", err)
	}
	return cm, nil
}

// writeFlatMappings populates concept_map_mappings and concept_map_unmapped for a ConceptMap.
func writeFlatMappings(ctx context.Context, tx pgx.Tx, conceptMapPK int64, cm *conceptmap.ConceptMap) error {
	rows := extractMappings(conceptMapPK, cm)
	if len(rows) > 0 {
		copied, err := tx.CopyFrom(ctx, conceptMapMappingsTable, mappingColumns, pgx.CopyFromRows(mappingRowsForCopy(rows)))
		if err != nil {
			return fmt.Errorf("failed to copy concept_map_mappings: %w", err)
		}
		if int(copied) != len(rows) {
			return fmt.Errorf("expected to copy %d mapping rows, got %d", len(rows), copied)
		}
	}
	unmappedRows := extractUnmapped(conceptMapPK, cm)
	for i := range unmappedRows {
		u := &unmappedRows[i]
		_, err := tx.Exec(ctx, `
			INSERT INTO concept_map_unmapped
				(concept_map_pk, group_index, group_source, group_target, mode, code, display, relationship, other_map)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (concept_map_pk, group_index) DO UPDATE SET
				mode = EXCLUDED.mode,
				code = EXCLUDED.code,
				display = EXCLUDED.display,
				relationship = EXCLUDED.relationship,
				other_map = EXCLUDED.other_map
		`, u.conceptMapPK, u.groupIndex, u.groupSource, u.groupTarget, u.mode, u.code, u.display, u.relationship, u.otherMap)
		if err != nil {
			return fmt.Errorf("failed to insert concept_map_unmapped row: %w", err)
		}
	}
	return nil
}

// Read retrieves a ConceptMap by its logical ID.
// Returns ErrGone for soft-deleted resources (FHIR requires 410, not 404).
// Returns ErrNotFound when the id has never existed.
func (r *ConceptMapRepo) Read(ctx context.Context, id string) (*conceptmap.ConceptMap, error) {
	query := `SELECT resource_json, created_at, updated_at, deleted_at FROM concept_maps WHERE id = $1`

	var resourceJSON []byte
	var createdAt, updatedAt time.Time
	var deletedAt *time.Time

	err := r.pool.QueryRow(ctx, query, id).Scan(&resourceJSON, &createdAt, &updatedAt, &deletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, conceptmap.ErrNotFound
		}
		return nil, fmt.Errorf("failed to read ConceptMap: %w", err)
	}

	if deletedAt != nil {
		return nil, conceptmap.ErrGone
	}

	var cm conceptmap.ConceptMap
	if err := json.Unmarshal(resourceJSON, &cm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ConceptMap: %w", err)
	}
	cm.CreatedAt = createdAt
	cm.UpdatedAt = updatedAt

	return &cm, nil
}

// Update replaces an existing ConceptMap.
// Uses optimistic concurrency: if cm.Meta.VersionID was populated from an
// `If-Match` request header, it must equal the stored version_id or this call
// returns conceptmap.ErrConflict instead of overwriting.
func (r *ConceptMapRepo) Update(ctx context.Context, id string, cm *conceptmap.ConceptMap) (*conceptmap.ConceptMap, error) {
	// Capture the caller's expected version BEFORE we replace cm.Meta with the
	// fresh server-side metadata below.
	expectedVersionID := ""
	if cm.Meta != nil {
		expectedVersionID = cm.Meta.VersionID
	}

	var currentVersionID int
	err := r.pool.QueryRow(ctx,
		`SELECT version_id FROM concept_maps WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(&currentVersionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Distinguish soft-deleted from never-existed so the handler can return 410 vs 404.
			var exists bool
			if existErr := r.pool.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM concept_maps WHERE id = $1)`, id,
			).Scan(&exists); existErr == nil && exists {
				return nil, conceptmap.ErrGone
			}
			return nil, conceptmap.ErrNotFound
		}
		return nil, fmt.Errorf("failed to check existing ConceptMap: %w", err)
	}

	if expectedVersionID != "" && expectedVersionID != strconv.Itoa(currentVersionID) {
		return nil, conceptmap.ErrConflict
	}

	now := time.Now().UTC()
	newVersionID := currentVersionID + 1
	cm.ID = id
	cm.UpdatedAt = now
	cm.Meta = &fhir.Meta{
		VersionID:   strconv.Itoa(newVersionID),
		LastUpdated: now.Format(time.RFC3339),
	}

	resourceJSON, err := json.Marshal(cm)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ConceptMap: %w", err)
	}

	sourceCodes, targetCodes := extractCodes(cm)
	sourceSystems, targetSystems := extractSystems(cm)

	// Scope the UPDATE to the version we just read: two concurrent PUTs both see version N,
	// both compute N+1, but only one will satisfy version_id = N in the WHERE clause,
	// causing the second to return zero rows and ErrConflict.
	query := `
		UPDATE concept_maps SET
			url = $2, version = $3, name = $4, title = $5, status = $6,
			publisher = $7, description = $8, date = $9,
			source_scope_uri = $10, source_scope_canonical = $11,
			target_scope_uri = $12, target_scope_canonical = $13,
			source_codes = $14, target_codes = $15,
			source_systems = $16, target_systems = $17,
			resource_json = $18, version_id = $19, updated_at = $20
		WHERE id = $1 AND deleted_at IS NULL AND version_id = $21`

	// Do the JSONB UPDATE and flat-table refresh in the same transaction to ensure consistency.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback is a no-op after commit; error unrecoverable in defer context

	queryWithReturning := query + " RETURNING pk"
	var conceptMapPK int64
	err = tx.QueryRow(ctx, queryWithReturning,
		id, cm.URL, cm.Version, cm.Name, cm.Title, cm.Status,
		cm.Publisher, cm.Description, cm.Date,
		cm.SourceScopeURI, cm.SourceScopeCanonical,
		cm.TargetScopeURI, cm.TargetScopeCanonical,
		sourceCodes, targetCodes,
		sourceSystems, targetSystems,
		resourceJSON, newVersionID, now,
		currentVersionID,
	).Scan(&conceptMapPK)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Zero rows means version_id no longer matches: another writer or concurrent delete raced us.
			return nil, conceptmap.ErrConflict
		}
		return nil, fmt.Errorf("failed to update ConceptMap: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM concept_map_mappings WHERE concept_map_pk = $1`, conceptMapPK); err != nil {
		return nil, fmt.Errorf("failed to clear stale mapping rows: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM concept_map_unmapped WHERE concept_map_pk = $1`, conceptMapPK); err != nil {
		return nil, fmt.Errorf("failed to clear stale unmapped rows: %w", err)
	}

	if err := writeFlatMappings(ctx, tx, conceptMapPK, cm); err != nil {
		return nil, err
	}

	if err := writeHistoryEntry(ctx, tx, cm.ID, newVersionID, "update", resourceJSON); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit ConceptMap update: %w", err)
	}
	return cm, nil
}

// Delete performs a soft delete on a ConceptMap and writes a 'delete' history entry.
func (r *ConceptMapRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback is a no-op after commit; error unrecoverable in defer context

	// Read the pre-delete snapshot so the history row carries the last
	// version of the resource (FHIR _history convention).
	var (
		resourceJSON   []byte
		currentVersion int
	)
	err = tx.QueryRow(ctx,
		`SELECT resource_json, version_id FROM concept_maps WHERE id = $1 AND deleted_at IS NULL`,
		id,
	).Scan(&resourceJSON, &currentVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// If the row exists but is soft-deleted, return ErrGone so the handler
			// can treat repeat DELETEs as idempotent (204, not 404).
			var exists bool
			if existErr := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM concept_maps WHERE id = $1)`, id,
			).Scan(&exists); existErr == nil && exists {
				return conceptmap.ErrGone
			}
			return conceptmap.ErrNotFound
		}
		return fmt.Errorf("failed to read pre-delete snapshot: %w", err)
	}

	result, err := tx.Exec(ctx,
		`UPDATE concept_maps SET deleted_at = $2 WHERE id = $1 AND deleted_at IS NULL`,
		id, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("failed to delete ConceptMap: %w", err)
	}
	if result.RowsAffected() == 0 {
		// Concurrent delete: the row was active when snapshotted but another
		// goroutine deleted it between our SELECT and UPDATE.
		return conceptmap.ErrGone
	}

	// The delete entry advances version_id by 1 to create a consistent timeline.
	if err := writeHistoryEntry(ctx, tx, id, currentVersion+1, "delete", resourceJSON); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit ConceptMap delete: %w", err)
	}
	return nil
}

// History returns every version of the resource newest-first. Soft-deleted
// resources still expose history. Returns ErrNotFound if no rows exist for id.
func (r *ConceptMapRepo) History(ctx context.Context, id string) ([]conceptmap.HistoryEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT version_id, operation, occurred_at, resource_json
		FROM concept_map_history
		WHERE concept_map_id = $1
		ORDER BY version_id DESC
	`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to query history: %w", err)
	}
	defer rows.Close()

	var out []conceptmap.HistoryEntry
	for rows.Next() {
		var (
			entry        conceptmap.HistoryEntry
			occurredAt   time.Time
			resourceJSON []byte
		)
		if err := rows.Scan(&entry.VersionID, &entry.Operation, &occurredAt, &resourceJSON); err != nil {
			return nil, fmt.Errorf("failed to scan history row: %w", err)
		}
		entry.OccurredAt = occurredAt.UTC().Format(time.RFC3339)
		if len(resourceJSON) > 0 {
			var cm conceptmap.ConceptMap
			if err := json.Unmarshal(resourceJSON, &cm); err == nil {
				entry.Resource = &cm
			}
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, conceptmap.ErrNotFound
	}
	return out, nil
}

// ReadVersion returns the snapshot of the resource at the given version.
// vread semantics: returns the exact bytes that were stored at that version,
// regardless of later updates. Returns ErrNotFound when the (id, versionID)
// pair has no corresponding history row.
func (r *ConceptMapRepo) ReadVersion(ctx context.Context, id string, versionID int) (*conceptmap.ConceptMap, error) {
	var resourceJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT resource_json FROM concept_map_history
		WHERE concept_map_id = $1 AND version_id = $2
	`, id, versionID).Scan(&resourceJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, conceptmap.ErrNotFound
		}
		return nil, fmt.Errorf("failed to vread: %w", err)
	}
	var cm conceptmap.ConceptMap
	if err := json.Unmarshal(resourceJSON, &cm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal vread snapshot: %w", err)
	}
	return &cm, nil
}

// Search finds ConceptMaps matching the given search parameters.
// [SYM-GR-0220] Uses parameterized queries to prevent SQL injection.
func (r *ConceptMapRepo) Search(ctx context.Context, params conceptmap.SearchParams) (*conceptmap.SearchResult, error) {
	var conditions []string
	var args []any
	argIdx := 1

	conditions = append(conditions, "deleted_at IS NULL")

	if params.ID != "" {
		conditions = append(conditions, fmt.Sprintf("id = $%d", argIdx))
		args = append(args, params.ID)
		argIdx++
	}
	if params.URL != "" {
		conditions = append(conditions, fmt.Sprintf("url = $%d", argIdx))
		args = append(args, params.URL)
		argIdx++
	}
	if params.Version != "" {
		conditions = append(conditions, fmt.Sprintf("version = $%d", argIdx))
		args = append(args, params.Version)
		argIdx++
	}
	if params.Name != "" {
		conditions = append(conditions, fmt.Sprintf("name ILIKE $%d", argIdx))
		args = append(args, "%"+params.Name+"%")
		argIdx++
	}
	if params.Title != "" {
		conditions = append(conditions, fmt.Sprintf("title ILIKE $%d", argIdx))
		args = append(args, "%"+params.Title+"%")
		argIdx++
	}
	if params.Status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, params.Status)
		argIdx++
	}
	if params.Publisher != "" {
		conditions = append(conditions, fmt.Sprintf("publisher ILIKE $%d", argIdx))
		args = append(args, "%"+params.Publisher+"%")
		argIdx++
	}
	if params.Description != "" {
		conditions = append(conditions, fmt.Sprintf("description ILIKE $%d", argIdx))
		args = append(args, "%"+params.Description+"%")
		argIdx++
	}
	if params.SourceCode != "" {
		conditions = append(conditions, fmt.Sprintf("$%d = ANY(source_codes)", argIdx))
		args = append(args, params.SourceCode)
		argIdx++
	}
	if params.TargetCode != "" {
		conditions = append(conditions, fmt.Sprintf("$%d = ANY(target_codes)", argIdx))
		args = append(args, params.TargetCode)
		argIdx++
	}
	if params.SourceGroupSystem != "" {
		conditions = append(conditions, fmt.Sprintf("$%d = ANY(source_systems)", argIdx))
		args = append(args, params.SourceGroupSystem)
		argIdx++
	}
	if params.TargetGroupSystem != "" {
		conditions = append(conditions, fmt.Sprintf("$%d = ANY(target_systems)", argIdx))
		args = append(args, params.TargetGroupSystem)
		argIdx++
	}
	if params.SourceScope != "" {
		conditions = append(conditions, fmt.Sprintf("source_scope_canonical = $%d", argIdx))
		args = append(args, params.SourceScope)
		argIdx++
	}
	if params.SourceScopeURI != "" {
		conditions = append(conditions, fmt.Sprintf("source_scope_uri = $%d", argIdx))
		args = append(args, params.SourceScopeURI)
		argIdx++
	}
	if params.TargetScope != "" {
		conditions = append(conditions, fmt.Sprintf("target_scope_canonical = $%d", argIdx))
		args = append(args, params.TargetScope)
		argIdx++
	}
	if params.TargetScopeURI != "" {
		conditions = append(conditions, fmt.Sprintf("target_scope_uri = $%d", argIdx))
		args = append(args, params.TargetScopeURI)
		argIdx++
	}

	where := strings.Join(conditions, " AND ")

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM concept_maps WHERE %s", where)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count ConceptMaps: %w", err)
	}

	dataQuery := fmt.Sprintf(
		"SELECT resource_json, created_at, updated_at FROM concept_maps WHERE %s ORDER BY updated_at DESC LIMIT $%d OFFSET $%d",
		where, argIdx, argIdx+1,
	)
	args = append(args, params.Count, params.Offset)

	rows, err := r.pool.Query(ctx, dataQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search ConceptMaps: %w", err)
	}
	defer rows.Close()

	var results []conceptmap.ConceptMap
	for rows.Next() {
		var resourceJSON []byte
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&resourceJSON, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan ConceptMap row: %w", err)
		}
		var cm conceptmap.ConceptMap
		if err := json.Unmarshal(resourceJSON, &cm); err != nil {
			return nil, fmt.Errorf("failed to unmarshal ConceptMap: %w", err)
		}
		cm.CreatedAt = createdAt
		cm.UpdatedAt = updatedAt
		results = append(results, cm)
	}

	return &conceptmap.SearchResult{
		ConceptMaps: results,
		Total:       total,
	}, nil
}

// FindByURL looks up a ConceptMap by its canonical URL and optional version.
func (r *ConceptMapRepo) FindByURL(ctx context.Context, url, version string) (*conceptmap.ConceptMap, error) {
	var query string
	var args []any

	if version != "" {
		query = `SELECT resource_json, created_at, updated_at FROM concept_maps WHERE url = $1 AND version = $2 AND deleted_at IS NULL ORDER BY updated_at DESC LIMIT 1`
		args = []any{url, version}
	} else {
		query = `SELECT resource_json, created_at, updated_at FROM concept_maps WHERE url = $1 AND deleted_at IS NULL ORDER BY updated_at DESC LIMIT 1`
		args = []any{url}
	}

	var resourceJSON []byte
	var createdAt, updatedAt time.Time

	err := r.pool.QueryRow(ctx, query, args...).Scan(&resourceJSON, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, conceptmap.ErrNotFound
		}
		return nil, fmt.Errorf("failed to find ConceptMap by URL: %w", err)
	}

	var cm conceptmap.ConceptMap
	if err := json.Unmarshal(resourceJSON, &cm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ConceptMap: %w", err)
	}
	cm.CreatedAt = createdAt
	cm.UpdatedAt = updatedAt

	return &cm, nil
}

// FindBySourceScope finds a ConceptMap by its source scope URI or canonical.
// [SYM-GR-0220] Uses parameterized queries to prevent SQL injection.
func (r *ConceptMapRepo) FindBySourceScope(ctx context.Context, sourceScope string) (*conceptmap.ConceptMap, error) {
	query := `SELECT resource_json, created_at, updated_at FROM concept_maps 
		WHERE (source_scope_uri = $1 OR source_scope_canonical = $1) AND deleted_at IS NULL 
		ORDER BY updated_at DESC LIMIT 1`

	var resourceJSON []byte
	var createdAt, updatedAt time.Time

	err := r.pool.QueryRow(ctx, query, sourceScope).Scan(&resourceJSON, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, conceptmap.ErrNotFound
		}
		return nil, fmt.Errorf("failed to find ConceptMap by source scope: %w", err)
	}

	var cm conceptmap.ConceptMap
	if err := json.Unmarshal(resourceJSON, &cm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ConceptMap: %w", err)
	}
	cm.CreatedAt = createdAt
	cm.UpdatedAt = updatedAt

	return &cm, nil
}

// extractCodes extracts all source and target codes from a ConceptMap for indexing.
func extractCodes(cm *conceptmap.ConceptMap) (sourceCodes, targetCodes []string) {
	seen := make(map[string]bool)
	seenTarget := make(map[string]bool)

	for _, group := range cm.Group {
		for _, elem := range group.Element {
			if elem.Code != "" && !seen[elem.Code] {
				sourceCodes = append(sourceCodes, elem.Code)
				seen[elem.Code] = true
			}
			for ti := range elem.Target {
				target := &elem.Target[ti]
				if target.Code != "" && !seenTarget[target.Code] {
					targetCodes = append(targetCodes, target.Code)
					seenTarget[target.Code] = true
				}
			}
		}
	}
	return
}

// extractSystems extracts all source and target systems from a ConceptMap for indexing.
func extractSystems(cm *conceptmap.ConceptMap) (sourceSystems, targetSystems []string) {
	seenSource := make(map[string]bool)
	seenTarget := make(map[string]bool)

	for _, group := range cm.Group {
		if group.Source != "" && !seenSource[group.Source] {
			sourceSystems = append(sourceSystems, group.Source)
			seenSource[group.Source] = true
		}
		if group.Target != "" && !seenTarget[group.Target] {
			targetSystems = append(targetSystems, group.Target)
			seenTarget[group.Target] = true
		}
	}
	return
}
