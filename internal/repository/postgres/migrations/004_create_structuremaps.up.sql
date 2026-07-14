-- M5a — StructureMap resource storage. Mirrors the concept_maps + history
-- shape from migrations 001 + 003, minus the M3 flat mapping table (which
-- is ConceptMap-specific because StructureMaps don't have row-level lookups
-- — engine work lives in M5d+ and operates on the parsed FML AST cached
-- alongside the JSONB resource, not a normalised SQL table).

CREATE TABLE IF NOT EXISTS structure_maps (
    pk             BIGSERIAL PRIMARY KEY,
    id             TEXT UNIQUE NOT NULL,
    url            TEXT,
    version        TEXT,
    name           TEXT,
    title          TEXT,
    status         TEXT NOT NULL DEFAULT 'draft',
    publisher      TEXT,
    description    TEXT,
    date           TEXT,
    resource_json  JSONB NOT NULL,
    version_id     INTEGER NOT NULL DEFAULT 1,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_structure_maps_url ON structure_maps (url) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_structure_maps_url_version ON structure_maps (url, version) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_structure_maps_name ON structure_maps (name) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_structure_maps_status ON structure_maps (status) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_structure_maps_updated_at ON structure_maps (updated_at DESC) WHERE deleted_at IS NULL;

-- Append-only history mirror of concept_map_history.
CREATE TABLE IF NOT EXISTS structure_map_history (
    pk               BIGSERIAL PRIMARY KEY,
    structure_map_id TEXT      NOT NULL,
    version_id       INTEGER   NOT NULL,
    operation        TEXT      NOT NULL CHECK (operation IN ('create','update','delete')),
    resource_json    JSONB,
    occurred_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_structure_map_history_id_version
    ON structure_map_history (structure_map_id, version_id DESC);
