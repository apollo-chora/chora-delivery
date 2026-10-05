-- =============================================================================
-- chora-delivery : 0041_course_prerequisites.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- ADR           : ADR-226 (Course Prerequisite DAG — structured, cycle-checked,
--                 alongside the free-text PrerequisiteNotes)
--
-- Purpose:
--   Persist the first-class course→course prerequisite EDGE. A row
--   {course_id requires prerequisite_course_id} forms, per tenant, a directed
--   graph over courses that the application service keeps ACYCLIC (fail-loud
--   cycle detection in internal/domain/delivery/course_prerequisite.go). Both
--   ids are intra-chora_delivery course references (cross-aggregate, no FK,
--   never cross-DB — .claude/rules/ddd-enforcement.md HARD RULE #1/#3). kind
--   ∈ {hard_gate, advisory}; NEITHER is enforced at enrol yet (ADR-226 §4 —
--   author + validate + display now, gate later).
--
-- ROW LEVEL SECURITY (tenant_isolation): admin CRUD, always tenant-scoped;
--   pg.CoursePrerequisiteRepo calls rls.ApplySession (SET LOCAL chora.tenant_id)
--   before every query. RLS ENABLED for defence-in-depth. The USING/WITH CHECK
--   pair uses the NULLIF-safe cast (mig-0019 lesson: a pooled conn can leave the
--   GUC at '' and ''::uuid throws 22P02) — the 0039/0040 write-path form.
--
-- SOFT-DELETE + REVIVE: the unique index on (tenant, course, prerequisite) is
--   NON-partial, so there is at most one physical row per edge across its whole
--   history. Remove sets deleted_at; a re-add UPSERTs the same row and clears
--   deleted_at (revive) rather than inserting a duplicate. List queries filter
--   deleted_at IS NULL. DELETE is deliberately NOT granted — never hard-delete.
--
-- Grants: explicit per-DB app-role grants (0039/0040 form). These MUST target
--   chora_delivery_app_rw / chora_delivery_app_ro — a global app_rw role does
--   not exist in chora_delivery and would abort + roll back the whole migration.
--   (ALTER DEFAULT PRIVILEGES in 9999_grant_app_roles.sql also covers this; the
--   explicit grants are belt-and-suspenders + self-documenting.)
-- =============================================================================

CREATE TABLE IF NOT EXISTS course_prerequisites (
    id                     UUID PRIMARY KEY,
    tenant_id              UUID        NOT NULL,
    course_id              UUID        NOT NULL,
    prerequisite_course_id UUID        NOT NULL,
    kind                   TEXT        NOT NULL DEFAULT 'hard_gate'
        CHECK (kind IN ('hard_gate', 'advisory')),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at             TIMESTAMPTZ
);

-- One physical row per (tenant, course, prerequisite) edge across all history —
-- NON-partial so the Upsert revive path (ON CONFLICT DO UPDATE ... deleted_at =
-- NULL) reuses the same row instead of accreting duplicates. Also serves the
-- forward lookup "what does course X require" (index prefix tenant_id, course_id).
CREATE UNIQUE INDEX IF NOT EXISTS uq_course_prerequisites_edge
    ON course_prerequisites (tenant_id, course_id, prerequisite_course_id);

-- Reverse lookup "what requires course X" over active edges only.
CREATE INDEX IF NOT EXISTS idx_course_prerequisites_reverse
    ON course_prerequisites (tenant_id, prerequisite_course_id)
    WHERE deleted_at IS NULL;

ALTER TABLE course_prerequisites ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON course_prerequisites
    USING (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

-- Per-DB app-role grants (MUST be chora_delivery_app_rw / _app_ro). The upsert +
-- soft-delete paths need SELECT/INSERT/UPDATE; DELETE is intentionally withheld
-- (soft-delete only — never hard-delete).
GRANT SELECT, INSERT, UPDATE ON course_prerequisites TO chora_delivery_app_rw;
GRANT SELECT                 ON course_prerequisites TO chora_delivery_app_ro;
