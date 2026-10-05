-- =============================================================================
-- chora-delivery : 0022_project_groups.down.sql  (reverses 0022_project_groups.up.sql)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON project_groups;
DROP INDEX IF EXISTS idx_project_groups_tenant_course;
DROP INDEX IF EXISTS idx_project_groups_tenant;
DROP TABLE IF EXISTS project_groups;
