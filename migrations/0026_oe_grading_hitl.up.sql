-- =============================================================================
-- chora-delivery : 0026_oe_grading_hitl.up.sql
--
-- Domain        : Content Delivery (core)
-- Database      : chora_delivery
-- ADR           : ADR-172 (OE grading evaluator+moderator loop + HITL gate)
--
-- Purpose:
--   Adds the HITL grade-review layer for per-submission OE grading:
--     1. submission_answers — AI-vs-Human provenance + override columns +
--        per-question comment + quality flag + LLM provenance.
--     2. submissions — review_status gate (PENDING_REVIEW → APPROVED) +
--        approver + the whole-assessment overall comment (AI/Human).
--     3. grade_overrides — append-only audit of every instructor override
--        (ADR-172 §D7 / ADR-170 append-to-resolve discipline).
--
-- All ALTERs are additive (ADD COLUMN IF NOT EXISTS) — no existing column is
-- renamed or dropped; the live MCQ flow is undisturbed.
--
-- Idempotency: ADD COLUMN IF NOT EXISTS + CREATE TABLE IF NOT EXISTS +
-- CREATE POLICY guarded by a DO block. Re-apply is a no-op.
-- =============================================================================

-- ---------------------------------------------------------------------------
-- 1. submission_answers — provenance + override + per-question comment
-- ---------------------------------------------------------------------------
ALTER TABLE submission_answers
    ADD COLUMN IF NOT EXISTS oe_comment              TEXT,
    ADD COLUMN IF NOT EXISTS oe_ai_comment           TEXT,
    ADD COLUMN IF NOT EXISTS ai_points_earned        NUMERIC(8,2),
    ADD COLUMN IF NOT EXISTS score_provenance        VARCHAR(8),
    ADD COLUMN IF NOT EXISTS comment_provenance      VARCHAR(8),
    ADD COLUMN IF NOT EXISTS amended_model_answer    TEXT,
    ADD COLUMN IF NOT EXISTS model_answer_provenance VARCHAR(8),
    ADD COLUMN IF NOT EXISTS quality_flagged         BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS grading_model_id        VARCHAR(128),
    ADD COLUMN IF NOT EXISTS grading_response_id     VARCHAR(256),
    ADD COLUMN IF NOT EXISTS overridden_by_gcid      UUID,
    ADD COLUMN IF NOT EXISTS overridden_at           TIMESTAMPTZ;

-- ---------------------------------------------------------------------------
-- 2. submissions — review_status gate + overall comment
-- ---------------------------------------------------------------------------
ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS review_status              VARCHAR(16),
    ADD COLUMN IF NOT EXISTS approved_by_gcid           UUID,
    ADD COLUMN IF NOT EXISTS approved_at                TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS overall_comment            TEXT,
    ADD COLUMN IF NOT EXISTS ai_overall_comment         TEXT,
    ADD COLUMN IF NOT EXISTS overall_comment_provenance VARCHAR(8);

-- review_status is NULL for MCQ-only submissions (no HITL gate); set to
-- 'PENDING_REVIEW' when AI OE grading lands, 'APPROVED' on instructor sign-off.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'submissions_review_status_chk'
    ) THEN
        ALTER TABLE submissions
            ADD CONSTRAINT submissions_review_status_chk
            CHECK (review_status IS NULL OR review_status IN ('PENDING_REVIEW', 'APPROVED'));
    END IF;
END$$;

-- ---------------------------------------------------------------------------
-- 3. grade_overrides — append-only HITL audit (ADR-172 §D7)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS grade_overrides (
    override_id          UUID         PRIMARY KEY,
    tenant_id            UUID         NOT NULL,
    submission_id        UUID         NOT NULL REFERENCES submissions(submission_id),
    answer_id            UUID,                       -- NULL for OVERALL_COMMENT
    test_set_question_id UUID,                       -- NULL for OVERALL_COMMENT
    question_id          UUID,
    field                VARCHAR(24)  NOT NULL
        CHECK (field IN ('SCORE', 'COMMENT', 'MODEL_ANSWER', 'OVERALL_COMMENT')),
    old_value            TEXT,
    new_value            TEXT,
    actor_gcid           UUID         NOT NULL,
    reason               TEXT,
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_grade_overrides_submission
    ON grade_overrides (tenant_id, submission_id, created_at);

ALTER TABLE grade_overrides ENABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE tablename = 'grade_overrides' AND policyname = 'grade_overrides_tenant_isolation'
    ) THEN
        CREATE POLICY grade_overrides_tenant_isolation ON grade_overrides
            USING (tenant_id::text = current_setting('chora.tenant_id', true));
    END IF;
END$$;

GRANT SELECT, INSERT ON grade_overrides TO chora_delivery_app_rw;
