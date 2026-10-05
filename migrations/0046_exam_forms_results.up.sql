-- =============================================================================
-- chora-delivery : 0046_exam_forms_results.up.sql
--
-- Domain        : Content Delivery (5 core) — Exam bounded context (ADR-190 D2)
-- Database      : chora_delivery  (NO 14th DB — the Exam BC is co-located)
-- Author        : W4 Brick-1 — the exam SCORING SPINE (ExamForm + ExamResult)
-- Story         : W4 (four-mode delivery refactor §8 #1 — cut-score → pass/fail)
--
-- Purpose:
--   Persist the two W4 Brick-1 aggregates of the Proctored-Exam bounded context
--   (ADR-190 D2: the BC GROWS from the live internal/domain/exam aggregate,
--   co-located in chora_delivery — NO new database):
--
--     exam_forms   — a revision-pinned, exposure-locked assembly of question-
--                    bank items (FSM DRAFT→ASSEMBLED→EXPOSED→RETIRED) + a
--                    compliance cut-score.
--     exam_results — the DURABLE SOURCE OF TRUTH for a candidate's cut-score
--                    → PASS/FAIL outcome against a specific exam_form.
--
--   Storage = JSONB aggregate snapshot + extracted columns for keying/listing,
--   identical to 0020_exams (the aggregates are variable-shape FSM records
--   carrying pinned-item lists + a cut-score value object — a flat-column
--   mapping would be brittle; every field is exported so the JSONB round-trip
--   is lossless).
--
--   Cross-domain / cross-BC references (exam_id, item_bank_id, candidate_ref,
--   and each pinned item's item_id/atom_revision_id) are FK-LESS opaque UUIDs
--   validated over Pub/Sub (.claude/rules/ddd-enforcement.md §3.5 #3) — no FK
--   to chora_creation (question bank) or the Candidate aggregate.
--
-- ROW LEVEL SECURITY (tenant_isolation): both tables are admin CRUD, always
--   tenant-scoped on every path — RLS is ENABLED for defence-in-depth (the
--   0019_bookings / 0020_exams rationale). pg.ExamFormRepo / pg.ExamResultRepo
--   call rls.ApplySession (SET LOCAL chora.tenant_id) before every query.
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   set in 9999_grant_app_roles.sql (same mechanism 0009..0045 rely on — this
--   migration adds NO grant statements).
--
-- Soft-delete: deleted_at (never hard-delete). Default queries filter
--   deleted_at IS NULL.
-- =============================================================================

-- ----------------------------------------------------------------------------
-- exam_forms — revision-pinned, exposure-locked exam form
-- ----------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS exam_forms (
    id           UUID PRIMARY KEY,
    tenant_id    UUID        NOT NULL,
    exam_id      UUID        NOT NULL,                 -- opaque ref (exam sitting), no FK
    item_bank_id UUID        NOT NULL,                 -- opaque ref (questionbank / chora_creation), no FK
    state        TEXT        NOT NULL,                 -- DRAFT | ASSEMBLED | EXPOSED | RETIRED
    data         JSONB       NOT NULL,                 -- lossless aggregate snapshot (items + cut_score)
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    exposed_at   TIMESTAMPTZ,
    retired_at   TIMESTAMPTZ,
    deleted_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_exam_forms_tenant_exam
    ON exam_forms (tenant_id, exam_id) WHERE deleted_at IS NULL;

ALTER TABLE exam_forms ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_forms
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- ----------------------------------------------------------------------------
-- exam_results — durable per-candidate PASS/FAIL outcome (write-once)
-- ----------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS exam_results (
    id            UUID PRIMARY KEY,
    tenant_id     UUID        NOT NULL,
    exam_id       UUID        NOT NULL,                -- opaque ref, no FK
    exam_form_id  UUID        NOT NULL,                -- opaque ref to exam_forms.id (same DB; kept FK-less to mirror the JSONB-snapshot aggregates + soft-delete lifecycle)
    candidate_ref UUID        NOT NULL,                -- opaque ref (Candidate owned elsewhere, ADR-190 D2), no FK
    outcome       TEXT        NOT NULL,                -- PASS | FAIL
    raw_score     INTEGER     NOT NULL,
    max_score     INTEGER     NOT NULL,
    data          JSONB       NOT NULL,                -- lossless aggregate snapshot
    scored_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_exam_results_tenant_form
    ON exam_results (tenant_id, exam_form_id) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_exam_results_tenant_candidate
    ON exam_results (tenant_id, candidate_ref) WHERE deleted_at IS NULL;

ALTER TABLE exam_results ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_results
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
