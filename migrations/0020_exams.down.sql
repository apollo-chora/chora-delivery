-- =============================================================================
-- chora-delivery : 0020_exams.down.sql  (reverses 0020_exams.up.sql)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON exams;
DROP INDEX IF EXISTS idx_exams_tenant;
DROP TABLE IF EXISTS exams;
