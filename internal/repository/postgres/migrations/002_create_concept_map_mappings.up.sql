-- M3a — flat normalised mapping table on top of the existing JSONB column.
-- One row per (group, element, target) tuple, plus a sibling table for
-- per-group `unmapped` strategy. The hot $translate query joins through the
-- numeric `pk` we add to concept_maps so the planner doesn't have to do a
-- text-id comparison on every lookup.

ALTER TABLE concept_maps ADD COLUMN IF NOT EXISTS pk BIGSERIAL;
-- The BIGSERIAL above auto-fills existing rows, but pk needs to be UNIQUE
-- before it can be the target of a foreign key.
CREATE UNIQUE INDEX IF NOT EXISTS idx_concept_maps_pk ON concept_maps (pk);

CREATE TABLE IF NOT EXISTS concept_map_mappings (
    pk               BIGSERIAL PRIMARY KEY,
    concept_map_pk   BIGINT      NOT NULL REFERENCES concept_maps (pk) ON DELETE CASCADE,
    group_index      INT         NOT NULL,
    element_index    INT         NOT NULL,
    target_index     INT         NOT NULL,
    source_system    TEXT        NOT NULL,
    source_version   TEXT,
    source_code      TEXT        NOT NULL,
    source_display   TEXT,
    target_system    TEXT,
    target_version   TEXT,
    target_code      TEXT,
    target_display   TEXT,
    relationship     TEXT        NOT NULL,
    equivalence      TEXT        NOT NULL,
    no_map           BOOLEAN     NOT NULL DEFAULT FALSE,
    comment          TEXT,
    depends_on_jsonb JSONB,
    product_jsonb    JSONB,
    property_jsonb   JSONB
);

-- Forward $translate hot path: WHERE source_system = ? AND source_code = ?
-- (and optionally concept_map_pk = ?). Putting concept_map_pk last keeps the
-- index useful for both the "search every map for this code" and "translate
-- within a specific map" forms.
CREATE INDEX IF NOT EXISTS idx_cmm_forward
    ON concept_map_mappings (source_system, source_code, concept_map_pk);

-- Reverse $translate hot path.
CREATE INDEX IF NOT EXISTS idx_cmm_reverse
    ON concept_map_mappings (target_system, target_code, concept_map_pk);

-- Within-map ordered scans (preserves the wire-order of group/element/target).
CREATE INDEX IF NOT EXISTS idx_cmm_by_map_order
    ON concept_map_mappings (concept_map_pk, group_index, element_index, target_index);

CREATE TABLE IF NOT EXISTS concept_map_unmapped (
    concept_map_pk BIGINT NOT NULL REFERENCES concept_maps (pk) ON DELETE CASCADE,
    group_index    INT    NOT NULL,
    group_source   TEXT,
    group_target   TEXT,
    mode           TEXT   NOT NULL,
    code           TEXT,
    display        TEXT,
    relationship   TEXT,
    other_map      TEXT,
    PRIMARY KEY (concept_map_pk, group_index)
);
