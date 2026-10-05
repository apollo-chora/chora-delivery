-- 0036_offering_sessions.up.sql — OfferingSession aggregate table
-- (R+ Four-Mode, Schedule & Rooms tab).
--
-- A scheduled delivery session (title + room + time) belonging to a delivery
-- Offering. offering_id is a cross-aggregate UUID reference (no FK, per
-- ddd-enforcement invariant #3 — same DB but aggregates are isolated). The
-- whole OfferingSession is stored as a lossless JSONB snapshot in `data`; the
-- extracted columns exist only for RLS scoping (tenant_id), the schedule list
-- filter (offering_id) and ordering (starts_at).
--
-- Storage + RLS mirror 0031_offerings (NULLIF-safe cast + WITH CHECK — the
-- mig-0019 pooled-conn lesson). No per-table grants: app_rw / app_ro inherit
-- via ALTER DEFAULT PRIVILEGES in 9999_grant_app_roles.sql (sorts last).

CREATE TABLE IF NOT EXISTS offering_sessions (
    id           UUID PRIMARY KEY,
    tenant_id    UUID        NOT NULL,
    offering_id  UUID        NOT NULL,
    starts_at    TIMESTAMPTZ NOT NULL,
    data         JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_offering_sessions_tenant_offering
    ON offering_sessions (tenant_id, offering_id) WHERE deleted_at IS NULL;

ALTER TABLE offering_sessions ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON offering_sessions
    USING      (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);
