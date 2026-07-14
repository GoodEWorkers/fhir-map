// Package postgres implements the FlatStore that backs the flat-table translate engine,
// reading from indexed concept_map_mappings table.
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/translate"
)

// MappingStore implements translate.FlatStore.
type MappingStore struct {
	pool *pgxpool.Pool
}

func NewMappingStore(pool *pgxpool.Pool) *MappingStore {
	return &MappingStore{pool: pool}
}

// ResolveConceptMap finds the concept_maps row and returns its pk + canonical URL.
// Must mirror the JSONB engine's resolveConceptMap for consistent behavior.
func (m *MappingStore) ResolveConceptMap(ctx context.Context, req translate.Request) (translate.FlatConceptMapRef, error) {
	switch {
	case req.ConceptMapID != "":
		return m.refByID(ctx, req.ConceptMapID)
	case req.URL != "":
		return m.refByURL(ctx, req.URL, req.Version)
	case req.SourceScope != "":
		return m.refBySourceScope(ctx, req.SourceScope)
	default:
		return translate.FlatConceptMapRef{}, errors.New("either url, conceptMap, or a specific ConceptMap ID must be provided")
	}
}

func (m *MappingStore) refByID(ctx context.Context, id string) (translate.FlatConceptMapRef, error) {
	var ref translate.FlatConceptMapRef
	err := m.pool.QueryRow(ctx,
		`SELECT pk, url FROM concept_maps WHERE id = $1 AND deleted_at IS NULL`,
		id,
	).Scan(&ref.PK, &ref.URL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ref, conceptmap.ErrNotFound
		}
		return ref, err
	}
	return ref, nil
}

func (m *MappingStore) refByURL(ctx context.Context, url, version string) (translate.FlatConceptMapRef, error) {
	var ref translate.FlatConceptMapRef
	var err error
	if version != "" {
		err = m.pool.QueryRow(ctx,
			`SELECT pk, url FROM concept_maps
			 WHERE url = $1 AND version = $2 AND deleted_at IS NULL
			 ORDER BY updated_at DESC LIMIT 1`,
			url, version,
		).Scan(&ref.PK, &ref.URL)
	} else {
		err = m.pool.QueryRow(ctx,
			`SELECT pk, url FROM concept_maps
			 WHERE url = $1 AND deleted_at IS NULL
			 ORDER BY updated_at DESC LIMIT 1`,
			url,
		).Scan(&ref.PK, &ref.URL)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ref, conceptmap.ErrNotFound
		}
		return ref, err
	}
	return ref, nil
}

func (m *MappingStore) refBySourceScope(ctx context.Context, scope string) (translate.FlatConceptMapRef, error) {
	var ref translate.FlatConceptMapRef
	err := m.pool.QueryRow(ctx,
		`SELECT pk, url FROM concept_maps
		 WHERE (source_scope_uri = $1 OR source_scope_canonical = $1) AND deleted_at IS NULL
		 ORDER BY updated_at DESC LIMIT 1`,
		scope,
	).Scan(&ref.PK, &ref.URL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ref, conceptmap.ErrNotFound
		}
		return ref, err
	}
	return ref, nil
}

// QueryForward runs the forward $translate lookup.
// Empty sourceSystem matches empty stored system (group had no source URL); same for targetSystemFilter.
func (m *MappingStore) QueryForward(ctx context.Context, conceptMapPK int64, sourceSystem, sourceCode, targetSystemFilter string) ([]translate.FlatRow, error) {
	const q = `
		SELECT group_index, element_index, target_index,
		       COALESCE(source_system, ''), source_code, COALESCE(source_display, ''),
		       COALESCE(target_system, ''), COALESCE(target_code, ''), COALESCE(target_display, ''),
		       relationship,
		       depends_on_jsonb, product_jsonb
		FROM concept_map_mappings
		WHERE concept_map_pk = $1
		  AND source_code = $2
		  AND ($3::text = '' OR source_system = $3)
		  AND ($4::text = '' OR COALESCE(target_system, '') = $4)
		ORDER BY group_index, element_index, target_index`
	rows, err := m.pool.Query(ctx, q, conceptMapPK, sourceCode, sourceSystem, targetSystemFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFlatRows(rows)
}

// QueryReverse is the reverse of QueryForward, predicating on target columns.
func (m *MappingStore) QueryReverse(ctx context.Context, conceptMapPK int64, targetSystem, targetCode, targetSystemFilter string) ([]translate.FlatRow, error) {
	const q = `
		SELECT group_index, element_index, target_index,
		       COALESCE(source_system, ''), source_code, COALESCE(source_display, ''),
		       COALESCE(target_system, ''), COALESCE(target_code, ''), COALESCE(target_display, ''),
		       relationship,
		       depends_on_jsonb, product_jsonb
		FROM concept_map_mappings
		WHERE concept_map_pk = $1
		  AND target_code = $2
		  AND ($3::text = '' OR COALESCE(target_system, '') = $3)
		  AND ($4::text = '' OR COALESCE(target_system, '') = $4)
		ORDER BY group_index, element_index, target_index`
	rows, err := m.pool.Query(ctx, q, conceptMapPK, targetCode, targetSystem, targetSystemFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFlatRows(rows)
}

// GroupUnmapped looks up a group's unmapped strategy by group_source; returns first if multiple groups share a source URL.
func (m *MappingStore) GroupUnmapped(ctx context.Context, conceptMapPK int64, groupSource string) (*translate.FlatUnmapped, error) {
	row := m.pool.QueryRow(ctx, `
		SELECT mode, COALESCE(code, ''), COALESCE(display, ''),
		       COALESCE(relationship, ''), COALESCE(other_map, ''),
		       COALESCE(group_source, ''), COALESCE(group_target, '')
		FROM concept_map_unmapped
		WHERE concept_map_pk = $1
		  AND ($2::text = '' OR COALESCE(group_source, '') = $2)
		ORDER BY group_index
		LIMIT 1
	`, conceptMapPK, groupSource)
	var u translate.FlatUnmapped
	err := row.Scan(&u.Mode, &u.Code, &u.Display, &u.Relationship, &u.OtherMap, &u.GroupSource, &u.GroupTarget)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

// BatchQueryForward batches N probes into a single lookup instead of N round-trips.
func (m *MappingStore) BatchQueryForward(ctx context.Context, conceptMapPK int64, probes []translate.BatchProbe, targetSystemFilter string) ([][]translate.FlatRow, error) {
	if len(probes) == 0 {
		return nil, nil
	}
	sysArr := make([]string, len(probes))
	codeArr := make([]string, len(probes))
	for i, p := range probes {
		sysArr[i] = p.SourceSystem
		codeArr[i] = p.SourceCode
	}

	const q = `
		WITH probes AS (
			SELECT ord, source_system, source_code
			FROM unnest($2::text[], $3::text[]) WITH ORDINALITY
			    AS p(source_system, source_code, ord)
		)
		SELECT
			p.ord,
			m.group_index, m.element_index, m.target_index,
			COALESCE(m.source_system, ''), m.source_code, COALESCE(m.source_display, ''),
			COALESCE(m.target_system, ''), COALESCE(m.target_code, ''), COALESCE(m.target_display, ''),
			m.relationship,
			m.depends_on_jsonb, m.product_jsonb
		FROM probes p
		LEFT JOIN concept_map_mappings m
		  ON m.concept_map_pk = $1
		 AND m.source_code = p.source_code
		 AND (p.source_system = '' OR m.source_system = p.source_system)
		 AND ($4::text = '' OR COALESCE(m.target_system, '') = $4)
		WHERE m.pk IS NOT NULL
		ORDER BY p.ord, m.group_index, m.element_index, m.target_index`

	rows, err := m.pool.Query(ctx, q, conceptMapPK, sysArr, codeArr, targetSystemFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([][]translate.FlatRow, len(probes))
	for rows.Next() {
		var ord int64
		var r translate.FlatRow
		if err := rows.Scan(
			&ord,
			&r.GroupIndex, &r.ElementIndex, &r.TargetIndex,
			&r.SourceSystem, &r.SourceCode, &r.SourceDisplay,
			&r.TargetSystem, &r.TargetCode, &r.TargetDisplay,
			&r.Relationship,
			&r.DependsOnJSONB, &r.ProductJSONB,
		); err != nil {
			return nil, err
		}
		idx := int(ord - 1)
		if idx < 0 || idx >= len(out) {
			continue
		}
		out[idx] = append(out[idx], r)
	}
	return out, rows.Err()
}

func scanFlatRows(rows pgx.Rows) ([]translate.FlatRow, error) {
	out := make([]translate.FlatRow, 0)
	for rows.Next() {
		var r translate.FlatRow
		if err := rows.Scan(
			&r.GroupIndex, &r.ElementIndex, &r.TargetIndex,
			&r.SourceSystem, &r.SourceCode, &r.SourceDisplay,
			&r.TargetSystem, &r.TargetCode, &r.TargetDisplay,
			&r.Relationship,
			&r.DependsOnJSONB, &r.ProductJSONB,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
