-- =============================================================================
-- chora-delivery : 0047_exam_candidates.up.sql
--
-- Domain        : Content Delivery (5 core) — Proctored-Exam bounded context
-- Database      : chora_delivery
-- Author        : W4 Brick 3 — Candidate aggregate + ID-verification admission
-- ADR           : ADR-190 D2 (Exam BC carve-out; "Candidate = a real GCID,
--                 never fire-and-forget"; admission gated by an Identity-owned
--                 verification claim)
--
-- Purpose:
--   Persist the Candidate aggregate — an identity-verified allocation of a REAL
--   learner GCID to a proctored Exam sitting, FSM
--   ALLOCATED -> ID_VERIFIED -> ADMITTED (with REJECTED / WITHDRAWN branches).
--   A candidate is admitted ONLY when it holds a VERIFIED verification claim.
--
--   A row is its OWN aggregate root, keyed by (tenant_id, exam_id, gcid). It
--   holds CROSS-AGGREGATE / CROSS-DOMAIN references — exam_id -> exams(id)
--   (Brick 1, same DB) and gcid -> the learner's opaque GCID (chora_identity) —
--   BY UUID with NO FK, per .claude/rules/ddd-enforcement.md Invariant #3
--   (cross-aggregate + cross-domain refs are UUIDs without FK, validated via
--   events / the verification-claim port). Intra-chora_delivery only —
--   cross-DB queries FORBIDDEN. Candidate is SEPARATE from Exam.EnrolledCount
--   (Brick 1 owns that; migrating EnrolledCount -> candidate-count is follow-up).
--
--   Storage = JSONB aggregate snapshot (`data`) + extracted columns
--   (tenant_id, exam_id, gcid, state, verification_status) for RLS scoping +
--   roster listing + the (tenant, exam, gcid) admission lookup — the 0020
--   exams / live_polls JSONB-snapshot pattern (every Candidate field is
--   exported, so the round-trip is lossless).
--
-- ROW LEVEL SECURITY (tenant_isolation): exam_candidates is admin CRUD, always
--   tenant-scoped, so RLS is ENABLED for defence-in-depth. The USING/WITH CHECK
--   pair uses the NULLIF-safe cast (mig-0019/0045 lesson: a pooled conn can
--   leave the GUC at '' and ''::uuid throws 22P02) and blocks writing a row
--   into a foreign tenant. pg.CandidateRepo calls rls.ApplySession (SET LOCAL
--   chora.tenant_id) before every query; the app role is NOBYPASSRLS.
--
-- Uniqueness: at most one ACTIVE candidate per (tenant, exam, gcid) — a partial
--   UNIQUE index excluding soft-deleted rows, so a concurrent double-allocate
--   fails loud instead of forking a candidate.
--
-- Grants: explicit per-DB app-role grants (0045 form). These MUST target
--   chora_delivery_app_rw / chora_delivery_app_ro — a global app_rw role does
--   not exist and would abort + roll back the migration. (They also inherit via
--   9999_grant_app_roles.sql's ALTER DEFAULT PRIVILEGES; the explicit grants are
--   belt-and-suspenders — the current 0045 convention, a superset of 0020.)
-- =============================================================================

CREATE TABLE IF NOT EXISTS exam_candidates (
    id                  UUID PRIMARY KEY,
    tenant_id           UUID        NOT NULL,
    exam_id             UUID        NOT NULL,
    gcid                UUID        NOT NULL,
    state               TEXT        NOT NULL,
    verification_status TEXT        NOT NULL,
    data                JSONB       NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ
);

-- One ACTIVE candidate per learner-per-exam (concurrent double-allocate fails loud).
CREATE UNIQUE INDEX IF NOT EXISTS uq_exam_candidates_exam_gcid
    ON exam_candidates (tenant_id, exam_id, gcid)
    WHERE deleted_at IS NULL;

-- Roster listing: all active candidates for one sitting.
CREATE INDEX IF NOT EXISTS idx_exam_candidates_exam
    ON exam_candidates (tenant_id, exam_id)
    WHERE deleted_at IS NULL;

ALTER TABLE exam_candidates ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_candidates
    USING (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

-- ----------------------------------------------------------------------------
-- Per-DB app-role grants (MUST be chora_delivery_app_rw / _app_ro).
-- ----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON exam_candidates TO chora_delivery_app_rw;
GRANT SELECT                         ON exam_candidates TO chora_delivery_app_ro;
