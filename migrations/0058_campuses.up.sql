-- =============================================================================
-- chora-delivery : 0058_campuses.up.sql
--
-- CHO-2293 - promote the campusops.Campus aggregate to a durable, tenant-scoped
-- Postgres store. Before this, Campus lived only in the in-memory
-- campusops.Registry: cmd/server wired NewRegistry() unconditionally (no pool
-- gate, no pg alternative) and then seeded a hardcoded demo row, so every campus
-- a real tenant created died on pod restart while the fake row resurrected on
-- every boot.
--
-- The binding was also absent from the ADR-236 D5 durability guard's slice, so
-- the boot log read `total=18 durable=18 in_memory=0 violations=0` while an
-- in-memory store was demonstrably serving production writes: a verdict true for
-- the 18 ports it checked and blind to the 19th. The wiring change that
-- accompanies this migration registers campus as the 19th binding.
--
-- Storage + RLS mirror 0051_rooms (REAL queryable columns, not a JSONB
-- snapshot; NULLIF-safe cast + WITH CHECK, the mig-0019 pooled-conn lesson).
-- Self-contained grants (0045/0049/0051-class form, MUST target
-- chora_delivery_app_rw / chora_delivery_app_ro): 9999_grant_app_roles.sql
-- already ran against the live DB, so a table created afterwards needs its own
-- explicit grants. ALTER DEFAULT PRIVILEGES alone has previously caused 42501 on
-- targeted migrations (reusable_gotcha_targeted_migration_skips_9999_grants_
-- and_tracker_lineage_drift).
--
-- Scope: Campus only. Room already has its own durable lane (rooms, mig 0051).
-- Branch has no HTTP surface and no writer, so it deliberately gets NO table
-- here: a relation nothing ever writes is exactly the shape that hides a 42501
-- forever. Reconciling the two room concepts is CHO-2294.
--
-- Date : 2026-07-18
-- =============================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS campuses (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    name        TEXT        NOT NULL,
    address_l1  TEXT        NOT NULL DEFAULT '',
    address_l2  TEXT        NOT NULL DEFAULT '',
    city        TEXT        NOT NULL DEFAULT '',
    country     TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ,
    CONSTRAINT campuses_country_alpha2 CHECK (country ~ '^[A-Z]{2}$')
);

CREATE INDEX IF NOT EXISTS idx_campuses_tenant
    ON campuses (tenant_id) WHERE deleted_at IS NULL;

ALTER TABLE campuses ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON campuses
    USING      (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

-- ----------------------------------------------------------------------------
-- Self-contained grants (see banner). A global app_rw role does not exist and
-- would abort + roll back the migration.
-- ----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON campuses TO chora_delivery_app_rw;
GRANT SELECT                         ON campuses TO chora_delivery_app_ro;

COMMIT;
