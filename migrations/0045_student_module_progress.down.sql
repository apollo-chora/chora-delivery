-- =============================================================================
-- chora-delivery : 0045_student_module_progress.down.sql
--   (reverses 0045_student_module_progress.up.sql)
--
-- Policies + indexes drop implicitly with the table, but are dropped explicitly
-- for a clean, re-runnable teardown.
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON student_module_progress;
DROP INDEX IF EXISTS idx_student_module_progress_learner_course;
DROP INDEX IF EXISTS idx_student_module_progress_module;
DROP INDEX IF EXISTS uq_student_module_progress_learner_module;
DROP TABLE IF EXISTS student_module_progress;
