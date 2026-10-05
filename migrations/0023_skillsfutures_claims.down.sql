-- =============================================================================
-- chora-delivery : 0023_skillsfutures_claims.down.sql
--   (reverses 0023_skillsfutures_claims.up.sql)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON skillsfutures_claims;
DROP INDEX IF EXISTS idx_skillsfutures_claims_tenant;
DROP TABLE IF EXISTS skillsfutures_claims;
