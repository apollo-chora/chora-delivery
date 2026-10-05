-- =============================================================================
-- chora-delivery : 0013_test_set_questions_question_id.down.sql
-- Reverse of 0013_test_set_questions_question_id.up.sql.
-- =============================================================================

BEGIN;

DROP INDEX IF EXISTS idx_test_set_questions_question_id;

ALTER TABLE test_set_questions
    DROP COLUMN IF EXISTS question_id;

COMMIT;
