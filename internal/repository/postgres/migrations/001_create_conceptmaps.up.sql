-- Create the concept_maps table for storing FHIR ConceptMap resources.
-- [SYM-GR-0220] Uses parameterized queries via application layer.
-- Performance: GIN indexes on array columns for fast search by source/target codes.

CREATE TABLE IF NOT EXISTS concept_maps (
    id                     TEXT PRIMARY KEY,
    url                    TEXT,
    version                TEXT,
    name                   TEXT,
    title                  TEXT,
    status                 TEXT NOT NULL DEFAULT 'draft',
    publisher              TEXT,
    description            TEXT,
    date                   TEXT,
    source_scope_uri       TEXT,
    source_scope_canonical TEXT,
    target_scope_uri       TEXT,
    target_scope_canonical TEXT,
    source_codes           TEXT[] DEFAULT '{}',
    target_codes           TEXT[] DEFAULT '{}',
    source_systems         TEXT[] DEFAULT '{}',
    target_systems         TEXT[] DEFAULT '{}',
    resource_json          JSONB NOT NULL,
    version_id             INTEGER NOT NULL DEFAULT 1,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at             TIMESTAMPTZ
);

-- Indexes for FHIR search parameters
CREATE INDEX IF NOT EXISTS idx_concept_maps_url ON concept_maps (url) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_maps_url_version ON concept_maps (url, version) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_maps_name ON concept_maps (name) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_maps_status ON concept_maps (status) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_maps_publisher ON concept_maps (publisher) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_maps_source_scope ON concept_maps (source_scope_canonical) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_maps_target_scope ON concept_maps (target_scope_canonical) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_maps_updated_at ON concept_maps (updated_at DESC) WHERE deleted_at IS NULL;

-- GIN indexes for array-based search (source-code, target-code, systems)
CREATE INDEX IF NOT EXISTS idx_concept_maps_source_codes ON concept_maps USING GIN (source_codes) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_maps_target_codes ON concept_maps USING GIN (target_codes) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_maps_source_systems ON concept_maps USING GIN (source_systems) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_maps_target_systems ON concept_maps USING GIN (target_systems) WHERE deleted_at IS NULL;
