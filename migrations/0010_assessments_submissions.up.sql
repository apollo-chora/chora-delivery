-- =============================================================================
-- chora-delivery : 0010_assessments_submissions.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : agent — chora-delivery Lane B (B-FE-X5)
-- Date          : 2026-05-16
-- Architecture  : ADR-155 §D7 (FSMs) + §D8 (demo scope) + §D9 (cohort scoping)
--
-- Aggregates owned:
--   - assessments         — Assessment aggregate root
--   - submissions         — Submission aggregate root (per-learner attempt)
--   - submission_answers  — child rows (one per question, idempotent merge)
--
-- Cross-aggregate (same-DB soft FK per ddd-enforcement #3):
--   - assessments.test_set_id → chora_delivery.test_sets.test_set_id
--
-- Resilience-priority directive (feedback_resilience_priority):
--   - UUIDv7 PKs — sortable creation-order
--   - Soft delete via deleted_at (ddd-enforcement #5)
--   - RLS policies on every table — tenant_id scope
--   - One-way visibility flip via results_released_at (NULL → SET); never cleared
--   - UNIQUE (assessment_id, learner_gcid, attempt_number) — one sub per attempt
--   - submission_answers UNIQUE (submission_id, test_set_question_id) —
--     idempotent autosave merge via UPSERT-on-conflict
-- =============================================================================

BEGIN;

CREATE TABLE assessments (
    assessment_id              UUID         PRIMARY KEY,
    tenant_id                  UUID         NOT NULL,
    instructor_gcid            UUID         NOT NULL,
    test_set_id                UUID         NOT NULL,
    test_set_revision_snapshot INTEGER      NOT NULL DEFAULT 1,
    class_id                   UUID,
    invited_gcids              UUID[]       NOT NULL DEFAULT '{}',
    title                      VARCHAR(256) NOT NULL,
    learner_facing_name        VARCHAR(256),
    state                      VARCHAR(32)  NOT NULL DEFAULT 'DRAFT'
        CHECK (state IN (
            'DRAFT', 'SCHEDULED', 'OPEN', 'CLOSED',
            'GRADING', 'GRADED', 'RELEASED', 'ARCHIVED'
        )),
    scheduled_open_at          TIMESTAMPTZ,
    scheduled_close_at         TIMESTAMPTZ,
    max_attempts               INTEGER      NOT NULL DEFAULT 1
        CHECK (max_attempts BETWEEN 1 AND 3),
    shuffle_questions          BOOLEAN      NOT NULL DEFAULT false,
    shuffle_mcq_options        BOOLEAN      NOT NULL DEFAULT true,
    accommodations_jsonb       JSONB,
    total_points               INTEGER      NOT NULL DEFAULT 0,
    question_count             INTEGER      NOT NULL DEFAULT 0,
    grading_config_snapshot    JSONB        NOT NULL DEFAULT '{}'::jsonb,
    release_announcement       TEXT,
    published_at               TIMESTAMPTZ,
    closed_at                  TIMESTAMPTZ,
    results_released_at        TIMESTAMPTZ,
    archived_at                TIMESTAMPTZ,
    deleted_at                 TIMESTAMPTZ,
    created_at                 TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_assessments_tenant_instructor
    ON assessments (tenant_id, instructor_gcid)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_assessments_tenant_state
    ON assessments (tenant_id, state)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_assessments_invited_gcids
    ON assessments USING GIN (invited_gcids)
    WHERE deleted_at IS NULL;

ALTER TABLE assessments ENABLE ROW LEVEL SECURITY;

CREATE POLICY assessments_tenant_isolation ON assessments
    USING (tenant_id::text = current_setting('chora.tenant_id', true));

CREATE TABLE submissions (
    submission_id              UUID         PRIMARY KEY,
    assessment_id              UUID         NOT NULL REFERENCES assessments(assessment_id),
    tenant_id                  UUID         NOT NULL,
    learner_gcid               UUID         NOT NULL,
    attempt_number             INTEGER      NOT NULL DEFAULT 1,
    state                      VARCHAR(32)  NOT NULL DEFAULT 'STARTED'
        CHECK (state IN (
            'STARTED', 'IN_PROGRESS', 'SUBMITTED',
            'PENDING_OE_GRADING', 'GRADED_PENDING_RELEASE',
            'RELEASED', 'ARCHIVED'
        )),
    opens_at                   TIMESTAMPTZ,
    closes_at                  TIMESTAMPTZ,
    time_limit_secs            INTEGER      NOT NULL DEFAULT 0,
    started_at                 TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_saved_at              TIMESTAMPTZ,
    submitted_at               TIMESTAMPTZ,
    graded_at                  TIMESTAMPTZ,
    released_at                TIMESTAMPTZ,
    mcq_score                  NUMERIC(8,2) NOT NULL DEFAULT 0,
    oe_score                   NUMERIC(8,2) NOT NULL DEFAULT 0,
    total_score                NUMERIC(8,2) NOT NULL DEFAULT 0,
    max_score                  INTEGER      NOT NULL DEFAULT 0,
    passing_percent            INTEGER      NOT NULL DEFAULT 70,
    passed                     BOOLEAN,
    deleted_at                 TIMESTAMPTZ,
    created_at                 TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (assessment_id, learner_gcid, attempt_number)
);

CREATE INDEX idx_submissions_assessment_state
    ON submissions (assessment_id, state)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_submissions_learner
    ON submissions (tenant_id, learner_gcid)
    WHERE deleted_at IS NULL;

ALTER TABLE submissions ENABLE ROW LEVEL SECURITY;

CREATE POLICY submissions_tenant_isolation ON submissions
    USING (tenant_id::text = current_setting('chora.tenant_id', true));

CREATE TABLE submission_answers (
    answer_id            UUID         PRIMARY KEY,
    submission_id        UUID         NOT NULL REFERENCES submissions(submission_id),
    test_set_question_id UUID         NOT NULL,
    question_id          UUID         NOT NULL,
    question_type        VARCHAR(16)  NOT NULL CHECK (question_type IN ('mcq', 'oe')),
    mcq_choice_id        UUID,
    mcq_choice_ids       UUID[],
    oe_response_text     TEXT,
    answered_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    mcq_correct          BOOLEAN,
    points_earned        NUMERIC(8,2) NOT NULL DEFAULT 0,
    points_possible      INTEGER      NOT NULL DEFAULT 0,
    oe_feedback          TEXT,
    oe_criterion_jsonb   JSONB,
    grading_dispatch     VARCHAR(32),
    oe_graded_at         TIMESTAMPTZ,
    UNIQUE (submission_id, test_set_question_id)
);

CREATE INDEX idx_submission_answers_submission
    ON submission_answers (submission_id);

ALTER TABLE submission_answers ENABLE ROW LEVEL SECURITY;

CREATE POLICY submission_answers_tenant_isolation ON submission_answers
    USING (
        submission_id IN (
            SELECT submission_id FROM submissions
            WHERE tenant_id::text = current_setting('chora.tenant_id', true)
        )
    );

GRANT SELECT, INSERT, UPDATE, DELETE ON assessments         TO chora_delivery_app_rw;
GRANT SELECT, INSERT, UPDATE, DELETE ON submissions         TO chora_delivery_app_rw;
GRANT SELECT, INSERT, UPDATE, DELETE ON submission_answers  TO chora_delivery_app_rw;

COMMIT;
