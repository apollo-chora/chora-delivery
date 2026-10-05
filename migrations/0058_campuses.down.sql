-- =============================================================================
-- chora-delivery : 0058_campuses.down.sql
--
-- Reverses 0058_campuses.up.sql (CHO-2293).
--
-- WARNING: dropping this table discards every durably-persisted campus. It does
-- NOT restore the in-memory Registry path, which the accompanying wiring change
-- retired; rolling back the schema without also rolling back the service image
-- leaves the campus endpoints failing loud (which is the correct behaviour: a
-- missing relation must not read as an empty tenant).
-- =============================================================================

BEGIN;

DROP POLICY IF EXISTS tenant_isolation ON campuses;
DROP INDEX IF EXISTS idx_campuses_tenant;
DROP TABLE IF EXISTS campuses;

COMMIT;
