-- 0027_live_quiz_session_join_code.up.sql — L5.2 Live Classroom stage (ADR-179, CHO-1704).
--
-- The LiveQuizSession aggregate is a JSONB snapshot (0017); the new
-- join_code/participants/asked_question_ids/final_scoreboard fields persist
-- inside `data` with no column changes. The ONLY schema need is an expression
-- index so GET /api/v1/classroom-sessions/by-code/{code} resolves a tenant's
-- ACTIVE session without a seq scan.
CREATE INDEX IF NOT EXISTS idx_live_quiz_sessions_join_code
    ON live_quiz_sessions (tenant_id, (data->>'join_code'))
    WHERE deleted_at IS NULL;
