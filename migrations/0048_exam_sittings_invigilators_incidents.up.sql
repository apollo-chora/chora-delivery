-- =============================================================================
-- chora-delivery : 0048_exam_sittings_invigilators_incidents.up.sql
--
-- Domain        : Content Delivery (5 core) — Proctored-Exam bounded context
-- Database      : chora_delivery
-- Author        : W4 Brick-B — ExamSitting + ExamInvigilator + IncidentReport
-- ADR           : ADR-190 D2 (Exam BC carve-out — the operational proctored
--                 sitting: room-linked, capacity, FSM) + ADR-191 (D1 per-sitting
--                 RANK enum {chief_invigilator, invigilator, technical_support,
--                 observer}; O1 at most ONE chief_invigilator per sitting).
--
-- Purpose:
--   Persist the three operational Brick-B aggregates:
--     1. exam_sittings     — the scheduled venue+time INSTANCE of an exam
--                            (SCHEDULED -> OPEN -> IN_PROGRESS -> CLOSED, +CANCELLED).
--     2. exam_invigilators — per-sitting proctor assignments carrying a rank.
--     3. incident_reports  — APPEND-ONLY audit trail filed during a sitting.
--
--   Each row is its OWN aggregate root (UUIDv7 PK). Cross-aggregate refs —
--   exam_id / exam_form_id -> exams / exam_forms (Brick-1, same DB),
--   sitting_id -> exam_sittings (this migration), room_id -> a venue's `rooms`
--   JSONB room_id (venue brick out of scope), invigilator_gcid / reported_by_gcid
--   / candidate_ref -> opaque GCIDs / candidate ids — are ALL carried BY UUID
--   with NO FK, per .claude/rules/ddd-enforcement.md Invariant #3 (validated via
--   events / ports, never a cross-DB or even intra-DB FK across aggregates).
--   Intra-chora_delivery only — cross-DB queries FORBIDDEN.
--
--   ROOM REFERENCE (assumption): room_id rides in the exam_sittings `data` JSONB
--   snapshot ONLY (optional, not a query axis) — an opaque UUID reference to a
--   venue's `rooms` JSONB room_id (design doc exam-administration-domain.md §4.4),
--   distinct from the legacy free-text delivery.Class.Room string. No dedicated
--   column ⇒ no column-type coupling.
--
--   Storage = JSONB aggregate snapshot (`data`) + extracted columns for RLS
--   scoping + the list queries + the single-chief partial-unique index — the
--   0020 exams / 0047 candidates JSONB-snapshot pattern (every aggregate field
--   is exported, so the round-trip is lossless).
--
-- ROW LEVEL SECURITY (tenant_isolation): all three tables are admin CRUD, always
--   tenant-scoped, so RLS is ENABLED for defence-in-depth. The USING/WITH CHECK
--   pair uses the NULLIF-safe cast (mig-0019/0045/0047 lesson: a pooled conn can
--   leave the GUC at '' and ''::uuid throws 22P02). The pg repos call
--   rls.ApplySession (SET LOCAL chora.tenant_id) before every query; the app
--   role is NOBYPASSRLS.
--
-- SINGLE-CHIEF INVARIANT (ADR-191 O1): a partial-UNIQUE index on
--   exam_invigilators (tenant_id, sitting_id) WHERE rank = 'chief_invigilator'
--   AND deleted_at IS NULL guarantees at most one ACTIVE chief per sitting — a
--   racing double-assign fails loud instead of forking a second chief. The
--   domain guard exam.EnsureSingleChief is the first line; this index is the
--   concurrency backstop.
--
-- APPEND-ONLY (incident_reports): the table carries NO updated_at / deleted_at,
--   and the app_rw role is granted SELECT + INSERT ONLY (no UPDATE/DELETE) — so
--   the AtomRevision-style append-only ethos is enforced at the DB, not merely
--   in code. Corrections are new rows.
--
-- Grants: explicit per-DB app-role grants (0045/0047 form). These MUST target
--   chora_delivery_app_rw / chora_delivery_app_ro — a global app_rw role does
--   not exist and would abort + roll back the migration.
-- =============================================================================

-- -----------------------------------------------------------------------------
-- 1. exam_sittings — the scheduled venue+time instance of an exam.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS exam_sittings (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    exam_id     UUID        NOT NULL,
    state       TEXT        NOT NULL,
    data        JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

-- Sitting roster for an exam: all active sittings of one exam.
CREATE INDEX IF NOT EXISTS idx_exam_sittings_exam
    ON exam_sittings (tenant_id, exam_id)
    WHERE deleted_at IS NULL;

ALTER TABLE exam_sittings ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_sittings
    USING (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON exam_sittings TO chora_delivery_app_rw;
GRANT SELECT                         ON exam_sittings TO chora_delivery_app_ro;

-- -----------------------------------------------------------------------------
-- 2. exam_invigilators — per-sitting proctor assignment (rank-bearing).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS exam_invigilators (
    id               UUID PRIMARY KEY,
    tenant_id        UUID        NOT NULL,
    sitting_id       UUID        NOT NULL,
    invigilator_gcid UUID        NOT NULL,
    rank             TEXT        NOT NULL,
    data             JSONB       NOT NULL,
    assigned_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ
);

-- Roster listing: all active invigilators for one sitting.
CREATE INDEX IF NOT EXISTS idx_exam_invigilators_sitting
    ON exam_invigilators (tenant_id, sitting_id)
    WHERE deleted_at IS NULL;

-- ADR-191 O1: at most ONE active chief_invigilator per sitting (fail loud).
CREATE UNIQUE INDEX IF NOT EXISTS uq_exam_invigilators_one_chief
    ON exam_invigilators (tenant_id, sitting_id)
    WHERE rank = 'chief_invigilator' AND deleted_at IS NULL;

-- One ACTIVE assignment per person per sitting (no double-assign fork).
CREATE UNIQUE INDEX IF NOT EXISTS uq_exam_invigilators_sitting_gcid
    ON exam_invigilators (tenant_id, sitting_id, invigilator_gcid)
    WHERE deleted_at IS NULL;

ALTER TABLE exam_invigilators ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_invigilators
    USING (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON exam_invigilators TO chora_delivery_app_rw;
GRANT SELECT                         ON exam_invigilators TO chora_delivery_app_ro;

-- -----------------------------------------------------------------------------
-- 3. incident_reports — APPEND-ONLY audit trail (no updated_at / deleted_at).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS incident_reports (
    id               UUID PRIMARY KEY,
    tenant_id        UUID        NOT NULL,
    sitting_id       UUID        NOT NULL,
    reported_by_gcid UUID        NOT NULL,
    kind             TEXT        NOT NULL,
    data             JSONB       NOT NULL,
    occurred_at      TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Audit trail for one sitting (chronological by UUIDv7 id).
CREATE INDEX IF NOT EXISTS idx_incident_reports_sitting
    ON incident_reports (tenant_id, sitting_id);

ALTER TABLE incident_reports ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON incident_reports
    USING (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

-- APPEND-ONLY: SELECT + INSERT only (NO UPDATE/DELETE) — DB-enforced immutability.
GRANT SELECT, INSERT ON incident_reports TO chora_delivery_app_rw;
GRANT SELECT         ON incident_reports TO chora_delivery_app_ro;
