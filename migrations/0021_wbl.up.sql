-- =============================================================================
-- chora-delivery : 0021_wbl.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : R+ durability sweep — pg-back the Wbl Placement aggregate
-- Date          : 2026-06-01
-- Story         : CHO-1580 (R+ Stage C-lite) — durability debt clearance
--
-- Purpose:
--   Persist the Wbl Placement aggregate (work-based-learning placement, FSM
--   SCHEDULED->IN_PROGRESS->COMPLETED, with SCHEDULED/IN_PROGRESS->WITHDRAWN as
--   the terminal soft-delete state) so it survives a pod restart and is durably
--   listable. Before this table, placements lived only in inmem.WblRepo —
--   ephemeral. The sibling of 0020_exams; part of the R+ durability sweep
--   (Exams/Wbl/ProjectGroups/SkillsFutures).
--
--   Storage = JSONB aggregate snapshot + extracted columns for keying/listing,
--   identical to 0020's exams (the Placement record is a multi-field FSM
--   aggregate — a flat-column mapping would be brittle; every field is exported
--   so the JSONB round-trip is lossless).
--
--   Soft-delete: WBL has NO deleted_at flag — soft delete is the WITHDRAWN
--   state transition (per the domain doc). The deleted_at column is kept here
--   for table-shape parity with the rest of the sweep but is unused by the
--   adapter; listing scopes by state, not deleted_at.
--
-- ROW LEVEL SECURITY (tenant_isolation): wbl_placements is admin CRUD, always
--   tenant-scoped on every path (create/list/get/patch/delete all carry
--   X-Tenant-Id) — so, like 0019_bookings / 0020_exams, RLS is ENABLED for
--   defence-in-depth. pg.WblRepo calls rls.ApplySession (SET LOCAL
--   chora.tenant_id) before every query.
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   set in 9999_grant_app_roles.sql (same mechanism 0009..0020 rely on).
-- =============================================================================

CREATE TABLE IF NOT EXISTS wbl_placements (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    state       TEXT        NOT NULL,
    data        JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_wbl_placements_tenant
    ON wbl_placements (tenant_id) WHERE deleted_at IS NULL;

ALTER TABLE wbl_placements ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON wbl_placements
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
