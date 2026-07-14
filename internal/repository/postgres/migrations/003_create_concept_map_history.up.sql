-- M4.2 — append-only history table for $history and vread.
--
-- Every Create / Update / Delete writes a row here in the same transaction
-- so the timeline is byte-faithful with what /fhir/ConceptMap/{id} returned
-- at the time. (operation = 'create' | 'update' | 'delete'.)
--
-- Indexed on (concept_map_id, version_id DESC) so `_history` (newest first)
-- and `_history/{vid}` (vread) are both single-row index hits.

CREATE TABLE IF NOT EXISTS concept_map_history (
    pk             BIGSERIAL PRIMARY KEY,
    concept_map_id TEXT      NOT NULL,
    version_id     INTEGER   NOT NULL,
    operation      TEXT      NOT NULL CHECK (operation IN ('create','update','delete')),
    resource_json  JSONB,
    occurred_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_concept_map_history_id_version
    ON concept_map_history (concept_map_id, version_id DESC);

CREATE INDEX IF NOT EXISTS idx_concept_map_history_id_occurred
    ON concept_map_history (concept_map_id, occurred_at DESC);
