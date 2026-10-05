-- =============================================================================
-- chora-delivery : 0054_course_learner_progress.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : R+ Four-Mode DoD - ASYNC analytics gap
-- Story         : CHO-1827 (epic)
-- ADR           : ADR-190 (ONE Content Delivery context for graduate/short/async)
--
-- Purpose:
--   Persist the CourseLearnerProgress aggregate - the per-learner (GCID),
--   per-COURSE self-paced traversal projection that backs the R+ ASYNC Analytics
--   tab's avg_progress_pct + completion_rate (§10.3 step 3: "R+ Analytics
--   reflects the enrolment/progress").
--
--   Learner progress lives in chora_consumption; analytics lives here. Cross-DB
--   queries are FORBIDDEN, so this table is fed ONLY by Pub/Sub:
--
--     chora.consumption.learning_path.advanced.v1   → completed/total atom counts
--     chora.consumption.learning_path.completed.v1  → is_complete + completed_at
--
--   Both already exist and already carry course_id (the delivery BINDING that
--   chora.delivery.enrollment.created.v1 put on the LearningPath at bootstrap),
--   so NO new topic is coined and no schema is widened.
--
--   Distinct from student_module_progress (0045): that one is keyed on MODULE and
--   is structurally empty for an async product, which has no module structure (the
--   async workspace has no Curriculum tab). This one is keyed on COURSE and needs
--   only atoms. Different grain, different feed, no overlap.
--
-- Cross-aggregate refs: course_id → courses(id) and path_id → a chora_consumption
--   LearningPath are UUIDs with NO FK, per .claude/rules/ddd-enforcement.md
--   invariant #3 (path_id is in ANOTHER DATABASE, so an FK is impossible as well
--   as forbidden; it is correlation only and is never joined).
--
-- ROW LEVEL SECURITY (tenant_isolation): tenant-scoped, read by the instructor
--   (cohort roll-up) and written only by the push-inbox subscriber. RLS filters by
--   tenant; the per-role gate is enforced at the handler, mirroring 0045. The
--   USING/WITH CHECK pair uses the NULLIF-safe cast (mig-0019 lesson: a pooled
--   conn can leave the GUC at '' and ''::uuid throws 22P02) and blocks writing a
--   row into a foreign tenant. pg.CourseProgressRepo calls rls.ApplySession
--   (SET LOCAL chora.tenant_id) before every query; the app role is NOBYPASSRLS.
--
-- Uniqueness: at most one ACTIVE projection per (tenant, gcid, course) - a partial
--   UNIQUE index excluding soft-deleted rows. This is the DURABLE idempotency
--   guarantee for the inbox: the dedupe key in idempotent.Store only suppresses a
--   same-message retry storm and is TTL'd, whereas this constraint holds forever
--   and across pods, so a redelivered advance can never fork a learner's progress.
--
-- Counts, not percentages: completed_atoms/total_atoms are stored as the INTEGERS
--   the producer sends (current_index/total_atoms). The wire's `progress_percent`
--   is misnamed at the source - LearningPath.ProgressPercent() returns a FRACTION
--   in [0.0,1.0] - so storing it as a "percent" would under-report by 100x. The
--   roll-up divides exactly once, at read time.
--
-- Grants: explicit per-DB app-role grants (0039/0045 form). These MUST target
--   chora_delivery_app_rw / chora_delivery_app_ro - a global app_rw role does not
--   exist and would abort + roll back the migration.
-- =============================================================================

CREATE TABLE IF NOT EXISTS course_learner_progress (
    id              UUID PRIMARY KEY,
    tenant_id       UUID        NOT NULL,
    gcid            UUID        NOT NULL,
    course_id       UUID        NOT NULL,
 -- Correlation back to the chora_consumption LearningPath. NOT a UUID column:
 -- it is opaque here and never joined, and a non-UUID producer value must not
 -- be able to hard-fail an INSERT the way certifications.course_id did on the
 -- live EXAM walk (SQLSTATE 22P02, a permanently un-retryable message).
    path_id         TEXT,
    completed_atoms INTEGER     NOT NULL DEFAULT 0,
    total_atoms     INTEGER     NOT NULL DEFAULT 0,
    is_complete     BOOLEAN     NOT NULL DEFAULT false,
    completed_at    TIMESTAMPTZ,
 -- Producer-clock watermark of the newest advance folded in. Pub/Sub does not
 -- order messages; this is what stops an older redelivery from rewinding
 -- progress. Advance stream only - completion is idempotent via is_complete.
    last_advance_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,

 -- The counters must be coherent at rest. The domain refuses these already;
 -- the constraint means a future writer that skips the aggregate cannot quietly
 -- store a cursor past the end of its own atom list.
    CONSTRAINT ck_course_learner_progress_counts
        CHECK (completed_atoms >= 0 AND total_atoms >= 0 AND completed_atoms <= total_atoms)
);

-- One ACTIVE projection per learner-course. THE durable idempotency guarantee.
CREATE UNIQUE INDEX IF NOT EXISTS uq_course_learner_progress_learner_course
    ON course_learner_progress (tenant_id, gcid, course_id)
    WHERE deleted_at IS NULL;

-- Analytics roll-up: every learner's progress across an offering's courses.
CREATE INDEX IF NOT EXISTS idx_course_learner_progress_course
    ON course_learner_progress (tenant_id, course_id)
    WHERE deleted_at IS NULL;

ALTER TABLE course_learner_progress ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON course_learner_progress
    USING (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

-- ----------------------------------------------------------------------------
-- Per-DB app-role grants (MUST be chora_delivery_app_rw / _app_ro).
-- ----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON course_learner_progress TO chora_delivery_app_rw;
GRANT SELECT                         ON course_learner_progress TO chora_delivery_app_ro;
