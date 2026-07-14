DROP TABLE IF EXISTS concept_map_unmapped;
DROP TABLE IF EXISTS concept_map_mappings;
DROP INDEX IF EXISTS idx_concept_maps_pk;
ALTER TABLE concept_maps DROP COLUMN IF EXISTS pk;
