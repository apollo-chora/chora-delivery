-- =============================================================================
-- chora-delivery : 0051_rooms.up.sql
--
-- CHO-2191 SP1 — promote the campusops.Room aggregate to a durable,
-- tenant-scoped Postgres store. Before this, Room lived only in the in-memory
-- campusops.Registry (lost on every pod restart). This table is the FOUNDATION
-- for the ratified room_id-keyed double-book / over-capacity gate (SP2 builds
-- the gate; SP1 only promotes Room + exposes POST/GET /api/v1/rooms).
--
-- Unlike offering_sessions (mig 0036, JSONB snapshot), Room persists as REAL,
-- queryable columns — the SP2 gate must constrain / SELECT on capacity + a
-- stable room_id. campus_id + branch_id are NULLABLE: a scheduling room needs no
-- campus (safe widening — there are ZERO rooms in prod).
--
-- Storage + RLS mirror 0036_offering_sessions (NULLIF-safe cast + WITH CHECK —
-- the mig-0019 pooled-conn lesson). Self-contained grants (0045/0049-class form,
-- MUST target chora_delivery_app_rw / chora_delivery_app_ro): 9999_grant_app_
-- roles.sql already ran against the live DB, so a table created afterwards needs
-- its own explicit grants — ALTER DEFAULT PRIVILEGES alone has previously caused
-- 42501 on targeted migrations (reusable_gotcha_targeted_migration_skips_9999_
-- grants_and_tracker_lineage_drift).
--
-- Date : 2026-07-16
-- =============================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS rooms (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    campus_id   UUID,
    branch_id   UUID,
    name        TEXT        NOT NULL,
    capacity    INT         NOT NULL CHECK (capacity > 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_rooms_tenant
    ON rooms (tenant_id) WHERE deleted_at IS NULL;

ALTER TABLE rooms ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON rooms
    USING      (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

-- ----------------------------------------------------------------------------
-- Self-contained grants (see banner). A global app_rw role does not exist and
-- would abort + roll back the migration.
-- ----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON rooms TO chora_delivery_app_rw;
GRANT SELECT                         ON rooms TO chora_delivery_app_ro;

COMMIT;
