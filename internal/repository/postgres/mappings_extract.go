package postgres

import (
	"encoding/json"
	"math"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
)

// idx32 converts a slice index to the int32 used by the *_index columns.
// Indices are bounded by in-memory slice lengths, so overflow is unreachable
// in practice; the explicit guard makes that bound checked (satisfies gosec
// G115) rather than relying on a silent wraparound.
func idx32(i int) int32 {
	if i < 0 || i > math.MaxInt32 {
		return 0
	}
	return int32(i)
}

// mappingRow is the row shape we COPY into concept_map_mappings. The column
// order in mappingColumns must match the field order here — pgx.CopyFromRows
// reads positionally.
type mappingRow struct {
	conceptMapPK   int64
	groupIndex     int32
	elementIndex   int32
	targetIndex    int32
	sourceSystem   string
	sourceVersion  string
	sourceCode     string
	sourceDisplay  string
	targetSystem   string
	targetVersion  string
	targetCode     string
	targetDisplay  string
	relationship   string
	equivalence    string
	noMap          bool
	comment        string
	dependsOnJSONB []byte
	productJSONB   []byte
	propertyJSONB  []byte
}

// mappingColumns is the on-disk column order (BIGSERIAL pk excluded).
var mappingColumns = []string{
	"concept_map_pk",
	"group_index",
	"element_index",
	"target_index",
	"source_system",
	"source_version",
	"source_code",
	"source_display",
	"target_system",
	"target_version",
	"target_code",
	"target_display",
	"relationship",
	"equivalence",
	"no_map",
	"comment",
	"depends_on_jsonb",
	"product_jsonb",
	"property_jsonb",
}

type unmappedRow struct {
	conceptMapPK int64
	groupIndex   int32
	groupSource  string
	groupTarget  string
	mode         string
	code         string
	display      string
	relationship string
	otherMap     string
}

// extractMappings flattens a ConceptMap into one row per (group, element, target) tuple.
// Elements without targets produce zero rows. equivalence is derived from relationship
// via the vocab table so it is always available for indexed lookups without re-deriving.
func extractMappings(conceptMapPK int64, cm *conceptmap.ConceptMap) []mappingRow {
	if cm == nil {
		return nil
	}
	// Pre-size optimistically — most maps have <100 targets.
	rows := make([]mappingRow, 0, 16)
	for gi := range cm.Group {
		g := &cm.Group[gi]
		for ei := range g.Element {
			e := &g.Element[ei]
			for ti := range e.Target {
				t := &e.Target[ti]
				rows = append(rows, mappingRow{
					conceptMapPK:   conceptMapPK,
					groupIndex:     idx32(gi),
					elementIndex:   idx32(ei),
					targetIndex:    idx32(ti),
					sourceSystem:   g.Source,
					sourceVersion:  g.SourceVersion,
					sourceCode:     e.Code,
					sourceDisplay:  e.Display,
					targetSystem:   g.Target,
					targetVersion:  g.TargetVersion,
					targetCode:     t.Code,
					targetDisplay:  t.Display,
					relationship:   t.Relationship,
					equivalence:    conceptmap.EquivalenceFromRelationship(t.Relationship),
					noMap:          boolPtrValue(e.NoMap),
					comment:        t.Comment,
					dependsOnJSONB: marshalIfPresent(t.DependsOn),
					productJSONB:   marshalIfPresent(t.Product),
					propertyJSONB:  marshalIfPresent(t.Property),
				})
			}
		}
	}
	return rows
}

func extractUnmapped(conceptMapPK int64, cm *conceptmap.ConceptMap) []unmappedRow {
	if cm == nil {
		return nil
	}
	var rows []unmappedRow
	for gi := range cm.Group {
		g := &cm.Group[gi]
		if g.Unmapped == nil {
			continue
		}
		rows = append(rows, unmappedRow{
			conceptMapPK: conceptMapPK,
			groupIndex:   idx32(gi),
			groupSource:  g.Source,
			groupTarget:  g.Target,
			mode:         g.Unmapped.Mode,
			code:         g.Unmapped.Code,
			display:      g.Unmapped.Display,
			relationship: g.Unmapped.Relationship,
			otherMap:     g.Unmapped.OtherMap,
		})
	}
	return rows
}

func boolPtrValue(b *bool) bool {
	return b != nil && *b
}

// marshalIfPresent returns nil for empty slices so the JSONB column is NULL instead of "null" or "[]".
func marshalIfPresent[T any](v []T) []byte {
	if len(v) == 0 {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

func mappingRowsForCopy(rows []mappingRow) [][]any {
	out := make([][]any, len(rows))
	for i := range rows {
		r := &rows[i]
		out[i] = []any{
			r.conceptMapPK,
			r.groupIndex,
			r.elementIndex,
			r.targetIndex,
			r.sourceSystem,
			nullableString(r.sourceVersion),
			r.sourceCode,
			nullableString(r.sourceDisplay),
			nullableString(r.targetSystem),
			nullableString(r.targetVersion),
			nullableString(r.targetCode),
			nullableString(r.targetDisplay),
			r.relationship,
			r.equivalence,
			r.noMap,
			nullableString(r.comment),
			r.dependsOnJSONB,
			r.productJSONB,
			r.propertyJSONB,
		}
	}
	return out
}

// nullableString returns nil for empty strings; source_system/source_code/relationship/equivalence are NOT NULL.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
