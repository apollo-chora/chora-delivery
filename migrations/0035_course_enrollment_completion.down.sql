-- =============================================================================
-- chora-delivery : 0035_course_enrollment_completion.down.sql
--                  (reverses 0035_course_enrollment_completion.up.sql)
--
-- Idempotent: DROP ... IF EXISTS so a re-run never errors. Drops the CHECK
-- constraint before the columns it guards.
-- =============================================================================

ALTER TABLE course_enrollments DROP CONSTRAINT IF EXISTS course_enrollments_status_check;
ALTER TABLE course_enrollments DROP COLUMN IF EXISTS passed;
ALTER TABLE course_enrollments DROP COLUMN IF EXISTS completed_at;
ALTER TABLE course_enrollments DROP COLUMN IF EXISTS status;
