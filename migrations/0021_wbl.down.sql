-- =============================================================================
-- chora-delivery : 0021_wbl.down.sql  (reverses 0021_wbl.up.sql)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON wbl_placements;
DROP INDEX IF EXISTS idx_wbl_placements_tenant;
DROP TABLE IF EXISTS wbl_placements;
