-- =============================================================================
-- chora-delivery : 0014_cj2_course_state_fsm.down.sql
--
-- Inverse of 0014_cj2_course_state_fsm.up.sql.
--
-- Drops:
--   - 2 indexes on courses (state-aware)
--   - 9 columns added by the .up migration
--
-- Idempotent via IF EXISTS.
-- Data loss WARNING: this drops the state column + companion fields, so
-- any course rows in AWAITING_REVIEW / ARCHIVED state lose that
-- classification on down-migration.
-- =============================================================================

BEGIN;

DROP INDEX IF EXISTS courses_author_state_idx;
DROP INDEX IF EXISTS courses_state_tenant_idx;

ALTER TABLE courses DROP COLUMN IF EXISTS published_at;
ALTER TABLE courses DROP COLUMN IF EXISTS prerequisites;
ALTER TABLE courses DROP COLUMN IF EXISTS learning_objectives;
ALTER TABLE courses DROP COLUMN IF EXISTS review_notes;
ALTER TABLE courses DROP COLUMN IF EXISTS scheduled_open_at;
ALTER TABLE courses DROP COLUMN IF EXISTS instructor_gcids;
ALTER TABLE courses DROP COLUMN IF EXISTS test_set_ids;
ALTER TABLE courses DROP COLUMN IF EXISTS author_gcid;
ALTER TABLE courses DROP COLUMN IF EXISTS state;

COMMIT;
