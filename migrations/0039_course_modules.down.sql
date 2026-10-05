-- =============================================================================
-- chora-delivery : 0039_course_modules.down.sql  (reverses 0039_course_modules.up.sql)
--
-- Drops in dependency order: the child table (course_module_items, whose
-- module_id FK references course_modules) first, then the root table. Policies +
-- indexes drop implicitly with their tables, but are dropped explicitly for a
-- clean, re-runnable teardown.
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON course_module_items;
DROP INDEX IF EXISTS uq_course_module_items_content;
DROP INDEX IF EXISTS idx_course_module_items_module;
DROP TABLE IF EXISTS course_module_items;

DROP POLICY IF EXISTS tenant_isolation ON course_modules;
DROP INDEX IF EXISTS uq_course_modules_course_position;
DROP TABLE IF EXISTS course_modules;
