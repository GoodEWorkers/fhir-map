CREATE TABLE IF NOT EXISTS structure_definitions (
    pk             BIGSERIAL PRIMARY KEY,
    id             TEXT UNIQUE NOT NULL,
    url            TEXT,
    version        TEXT,
    name           TEXT,
    title          TEXT,
    status         TEXT NOT NULL DEFAULT 'draft',
    kind           TEXT,
    type           TEXT,
    base_definition TEXT,
    derivation     TEXT,
    publisher      TEXT,
    description    TEXT,
    date           TEXT,
    resource_json  JSONB NOT NULL,
    version_id     INTEGER NOT NULL DEFAULT 1,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_structure_definitions_url ON structure_definitions (url) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_structure_definitions_url_version ON structure_definitions (url, version) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_structure_definitions_name ON structure_definitions (name) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_structure_definitions_status ON structure_definitions (status) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_structure_definitions_kind ON structure_definitions (kind) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_structure_definitions_type ON structure_definitions (type) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_structure_definitions_updated_at ON structure_definitions (updated_at DESC) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS structure_definition_history (
    pk                       BIGSERIAL PRIMARY KEY,
    structure_definition_id  TEXT      NOT NULL,
    version_id               INTEGER   NOT NULL,
    operation                TEXT      NOT NULL CHECK (operation IN ('create','update','delete')),
    resource_json            JSONB,
    occurred_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_structure_definition_history_id_version
    ON structure_definition_history (structure_definition_id, version_id DESC);
