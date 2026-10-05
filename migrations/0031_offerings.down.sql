-- =============================================================================
-- chora-delivery : 0031_offerings.down.sql  (reverses 0031_offerings.up.sql)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON offerings;
DROP INDEX IF EXISTS idx_offerings_tenant_delivery_type;
DROP INDEX IF EXISTS idx_offerings_tenant;
DROP TABLE IF EXISTS offerings;
