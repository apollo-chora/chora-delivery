-- =============================================================================
-- chora-delivery : 0054_course_learner_progress.down.sql
--   (reverses 0054_course_learner_progress.up.sql)
--
-- Policies + indexes drop implicitly with the table, but are dropped explicitly
-- for a clean, re-runnable teardown.
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON course_learner_progress;
DROP INDEX IF EXISTS idx_course_learner_progress_course;
DROP INDEX IF EXISTS uq_course_learner_progress_learner_course;
DROP TABLE IF EXISTS course_learner_progress;
