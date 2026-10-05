-- =============================================================================
-- chora-delivery : 0023_skillsfutures_claims.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : R+ durability sweep — pg-back the SkillsFuturesClaim aggregate
-- Date          : 2026-06-01
-- Story         : CHO-1580 (R+ Stage C-lite) — durability debt clearance
--
-- Purpose:
--   Persist the SkillsFuturesClaim aggregate (SSG funding request, FSM
--   PENDING->APPROVED->DISBURSED / PENDING->REJECTED) so it survives a pod
--   restart and is durably listable. Before this table, claims lived only in
--   inmem.SkillsFuturesRepo — ephemeral, unacceptable for a SkillsFutures
--   Singapore (SSG) government funding queue retained for audit. Part of the
--   R+ durability sweep (Exams/Wbl/ProjectGroups/SkillsFutures).
--
--   Storage = JSONB aggregate snapshot + extracted columns for keying/listing,
--   identical to 0020's exams (the claim record is a multi-field FSM aggregate
--   — a flat-column mapping would be brittle; every field is exported so the
--   JSONB round-trip is lossless).
--
-- ROW LEVEL SECURITY (tenant_isolation): skillsfutures_claims is admin-reviewed
--   CRUD, always tenant-scoped on every path (create/list/get all carry
--   X-Tenant-Id) — so RLS is ENABLED here for defence-in-depth (the
--   0019_bookings / 0020_exams rationale). pg.SkillsFuturesRepo calls
--   rls.ApplySession (SET LOCAL chora.tenant_id) before every query.
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   set in 9999_grant_app_roles.sql (same mechanism 0009..0020 rely on).
-- =============================================================================

CREATE TABLE IF NOT EXISTS skillsfutures_claims (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    state       TEXT        NOT NULL,
    data        JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_skillsfutures_claims_tenant
    ON skillsfutures_claims (tenant_id) WHERE deleted_at IS NULL;

ALTER TABLE skillsfutures_claims ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON skillsfutures_claims
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
