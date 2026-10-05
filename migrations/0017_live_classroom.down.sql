-- =============================================================================
-- chora-delivery : 0017_live_classroom.down.sql  (reverse of the .up)
-- =============================================================================

DROP INDEX IF EXISTS idx_live_quiz_sessions_quiz;
DROP INDEX IF EXISTS idx_live_quiz_sessions_tenant;
DROP TABLE IF EXISTS live_quiz_sessions;

DROP INDEX IF EXISTS idx_live_quizzes_tenant;
DROP TABLE IF EXISTS live_quizzes;
