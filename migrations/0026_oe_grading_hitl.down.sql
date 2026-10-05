-- =============================================================================
-- chora-delivery : 0026_oe_grading_hitl.down.sql  (reverse of 0026 up)
-- =============================================================================

DROP TABLE IF EXISTS grade_overrides;

ALTER TABLE submissions
    DROP CONSTRAINT IF EXISTS submissions_review_status_chk;

ALTER TABLE submissions
    DROP COLUMN IF EXISTS review_status,
    DROP COLUMN IF EXISTS approved_by_gcid,
    DROP COLUMN IF EXISTS approved_at,
    DROP COLUMN IF EXISTS overall_comment,
    DROP COLUMN IF EXISTS ai_overall_comment,
    DROP COLUMN IF EXISTS overall_comment_provenance;

ALTER TABLE submission_answers
    DROP COLUMN IF EXISTS oe_comment,
    DROP COLUMN IF EXISTS oe_ai_comment,
    DROP COLUMN IF EXISTS ai_points_earned,
    DROP COLUMN IF EXISTS score_provenance,
    DROP COLUMN IF EXISTS comment_provenance,
    DROP COLUMN IF EXISTS amended_model_answer,
    DROP COLUMN IF EXISTS model_answer_provenance,
    DROP COLUMN IF EXISTS quality_flagged,
    DROP COLUMN IF EXISTS grading_model_id,
    DROP COLUMN IF EXISTS grading_response_id,
    DROP COLUMN IF EXISTS overridden_by_gcid,
    DROP COLUMN IF EXISTS overridden_at;
