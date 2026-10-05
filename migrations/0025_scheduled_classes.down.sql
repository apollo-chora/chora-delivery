-- =============================================================================
-- chora-delivery : 0025_scheduled_classes.down.sql  (reverses 0025_*.up.sql)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON scheduled_classes;
DROP INDEX IF EXISTS idx_scheduled_classes_tenant_week;
DROP TABLE IF EXISTS scheduled_classes;
