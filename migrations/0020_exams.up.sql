-- =============================================================================
-- chora-delivery : 0020_exams.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : R+ durability sweep — pg-back the Exam aggregate
-- Date          : 2026-06-01
-- Story         : CHO-1580 (R+ Stage C-lite) — durability debt clearance
--
-- Purpose:
--   Persist the Exam aggregate (proctored exam sitting, FSM
--   DRAFT->SCHEDULED->OPEN->CLOSED->GRADED) so it survives a pod restart and is
--   durably listable. Before this table, exams lived only in inmem.ExamRepo —
--   ephemeral. The sibling of CHO-1622 Bookings; first of the R+ durability
--   sweep (Exams/Wbl/ProjectGroups/SkillsFutures).
--
--   Storage = JSONB aggregate snapshot + extracted columns for keying/listing,
--   identical to 0018's live_polls (the Exam record is a 20-field FSM aggregate
--   — a flat-column mapping would be brittle; every field is exported so the
--   JSONB round-trip is lossless).
--
-- ROW LEVEL SECURITY (tenant_isolation): exams is admin CRUD, always
--   tenant-scoped on every path (create/list/get all carry X-Tenant-Id) — so,
--   unlike 0017/0018 whose tenant-less WS by-id resolve forced handler-only
--   isolation, RLS is ENABLED here for defence-in-depth (the 0019_bookings
--   rationale). pg.ExamRepo calls rls.ApplySession (SET LOCAL chora.tenant_id)
--   before every query.
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   set in 9999_grant_app_roles.sql (same mechanism 0009..0019 rely on).
-- =============================================================================

CREATE TABLE IF NOT EXISTS exams (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    state       TEXT        NOT NULL,
    data        JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_exams_tenant
    ON exams (tenant_id) WHERE deleted_at IS NULL;

ALTER TABLE exams ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exams
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
