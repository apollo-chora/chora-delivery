-- =============================================================================
-- chora-delivery : 0056_franchise_satellite.down.sql
--   (reverses 0056_franchise_satellite.up.sql; ADR-192 Rollback step 1)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON franchise_satellite;
DROP INDEX IF EXISTS uq_franchise_satellite_live;
DROP TABLE IF EXISTS franchise_satellite;
