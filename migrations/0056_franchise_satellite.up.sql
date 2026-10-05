-- =============================================================================
-- chora-delivery : 0056_franchise_satellite.up.sql
--
-- Domain        : Content Delivery (5 core), Exam bounded context adjacency
-- Database      : chora_delivery  (ADR-192 D4: NO 14th DB, the map co-locates)
-- Date          : 2026-07-17
-- ADR           : ADR-192 D1 (cross-tenant exam rollup, ACCEPTED 2026-06-25)
-- Story         : CHO-2230 (W5 FRANCHISE bypass-free slice)
--
-- Purpose:
--   franchise_satellite is the AUTHORITATIVE, REVOCABLE owner-to-satellite
--   mapping: one live row states "owner_tenant_id may aggregate
--   satellite_tenant_id" for the exam rollup. Writes are restricted to the
--   platform-owner authority at the domain-service level (ADR-192 O1); the
--   admin path is ordinary tenant-scoped CRUD.
--
--   The table name and the flat (owner_tenant_id, satellite_tenant_id)
--   columns are ADR-192 D1 verbatim: the FUTURE exam_owner_rollup policy
--   subqueries
--
--     SELECT satellite_tenant_id FROM franchise_satellite
--     WHERE owner_tenant_id = NULLIF(current_setting('chora.exam_rollup_owner', true), '')::uuid
--
--   so the keys must be real columns, not JSONB.
--
-- WHAT THIS MIGRATION DELIBERATELY DOES NOT DO:
--   It does NOT create the exam_owner_rollup policy, does NOT touch the
--   chora.exam_rollup_owner GUC, and does NOT widen any read anywhere. That
--   policy IS the sanctioned 3rd RLS-bypass surface and is W5-gated behind
--   its own manual-steering STOP. This table alone widens nothing: RLS below
--   scopes rows to the OWNER tenant exactly like every other delivery table.
--
-- ROW LEVEL SECURITY (tenant_isolation): the OWNER tenant is the row's
--   tenant scope (owner_tenant_id doubles as the tenant_id column; a mapping
--   belongs to the owner who may read the satellites, never to the satellite).
--   NULLIF-guarded GUC cast + explicit WITH CHECK per the 0055/0047 form
--   (a pooled connection reverts the GUC to '' and ''::uuid raises 22P02).
--   RLS is ENABLED (not FORCEd), the deliberate chora_delivery convention
--   (0038_credentials rationale: the migrate role must stay exempt for
--   cross-tenant backfills; app_rw/app_ro are NOBYPASSRLS non-owners and are
--   fully bound by ENABLE).
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   set in 9999_grant_app_roles.sql (this migration adds NO grant statements;
--   a TARGETED hand-apply of this file alone leaves app roles with zero
--   privileges, the 9999-skip trap: apply via the tracked runner).
--
-- Soft-delete: deleted_at (never hard-delete). Revocation = stamping
--   deleted_at; the partial UNIQUE index scopes uniqueness to LIVE rows so a
--   revoked pair can be re-granted later without colliding with its tombstone.
-- =============================================================================

CREATE TABLE IF NOT EXISTS franchise_satellite (
    id                  UUID PRIMARY KEY,                 -- UUIDv7 minted by the app
    owner_tenant_id     UUID        NOT NULL,             -- the franchisor/owner (RLS scope)
    satellite_tenant_id UUID        NOT NULL,             -- the mapped satellite tenant
    created_by_gcid     UUID        NOT NULL,             -- accountable authority (ADR-192 O1)
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ,                      -- revocation tombstone

    CONSTRAINT chk_franchise_satellite_not_self CHECK (owner_tenant_id <> satellite_tenant_id)
);

-- Uniqueness over LIVE rows only: revoke then re-grant must work, and the
-- rollup policy's subquery must never see two live rows for one pair.
CREATE UNIQUE INDEX IF NOT EXISTS uq_franchise_satellite_live
    ON franchise_satellite (owner_tenant_id, satellite_tenant_id)
    WHERE deleted_at IS NULL;

ALTER TABLE franchise_satellite ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON franchise_satellite
    FOR ALL
    USING      (owner_tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (owner_tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);
