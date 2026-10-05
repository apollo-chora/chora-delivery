-- =============================================================================
-- chora-delivery : 0022_project_groups.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : R+ durability sweep — pg-back the ProjectGroup aggregate
-- Date          : 2026-06-01
-- Story         : CHO-1580 (R+ Stage C-lite) — durability debt clearance
--
-- Purpose:
--   Persist the ProjectGroup aggregate (group of learners collaborating on a
--   course project, FSM FORMING->ACTIVE->SUBMITTED->GRADED) so it survives a
--   pod restart and is durably listable by tenant AND by course. Before this
--   table, project groups lived only in inmem.ProjectGroupRepo — ephemeral.
--   The sibling of CHO-1580 Exams; part of the R+ durability sweep
--   (Exams/Wbl/ProjectGroups/SkillsFutures).
--
--   Storage = JSONB aggregate snapshot + extracted columns for keying/listing,
--   identical to 0020's exams (the ProjectGroup record is a multi-field FSM
--   aggregate with a nested members slice — a flat-column mapping would be
--   brittle; every field is exported so the JSONB round-trip is lossless).
--   course_id is extracted ALONGSIDE tenant_id/state (unlike exams) because
--   the ProjectGroup surface lists by-course (?course_id=) as well as
--   by-tenant — the extracted column lets ListByCourse filter in SQL with an
--   index instead of a Go scan.
--
-- ROW LEVEL SECURITY (tenant_isolation): project_groups is admin CRUD, always
--   tenant-scoped on every path (create/list/get all carry X-Tenant-Id) — so,
--   like 0020_exams, RLS is ENABLED here for defence-in-depth.
--   pg.ProjectGroupRepo calls rls.ApplySession (SET LOCAL chora.tenant_id)
--   before every query.
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   set in 9999_grant_app_roles.sql (same mechanism 0009..0020 rely on).
-- =============================================================================

CREATE TABLE IF NOT EXISTS project_groups (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    course_id   UUID        NOT NULL,
    state       TEXT        NOT NULL,
    data        JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_project_groups_tenant
    ON project_groups (tenant_id) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_project_groups_tenant_course
    ON project_groups (tenant_id, course_id) WHERE deleted_at IS NULL;

ALTER TABLE project_groups ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON project_groups
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
