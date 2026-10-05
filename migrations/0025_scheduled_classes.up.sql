-- =============================================================================
-- chora-delivery : 0025_scheduled_classes.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : R+ durability sweep Wave 2 follow-up — pg-back ScheduledClass
-- Date          : 2026-06-01
-- Story         : CHO-1626 (R+ Stage C-lite) — scheduling create-route + durability
--
-- Purpose:
--   Persist the ScheduledClass aggregate (a delivery instance scheduled against
--   a Course: course_id, instructor_gcid, room, starts_at/ends_at, capacity) so
--   it survives a pod restart and the R+ week-view calendar shows real data.
--
--   Wave 2 deferred pg-backing because ScheduledClass had NO write path
--   (nothing constructed one — the week-view was always empty). This follow-up
--   adds the create / reschedule / cancel routes, so the aggregate is now
--   writable + worth persisting.
--
--   Storage = JSONB aggregate snapshot + extracted columns:
--     - tenant_id           : RLS scoping
--     - iso_year + iso_week  : the week-view filter (exact parity with the
--                              in-memory InISOWeek check — StartsAt.ISOWeek())
--     - starts_at            : ordering / future range queries
--   (every ScheduledClass data field is exported; only the unexported
--   sync.Mutex is skipped, so the JSONB round-trip is lossless.)
--
-- ROW LEVEL SECURITY (tenant_isolation): scheduling is admin CRUD, always
--   tenant-scoped on every path — RLS enabled for defence-in-depth (the
--   0019_bookings / 0020_exams / 0024_surveys rationale). pg.SchedulingRepo
--   calls rls.ApplySession (SET LOCAL chora.tenant_id) before every query.
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   set in 9999_grant_app_roles.sql (same mechanism 0009..0024 rely on).
-- =============================================================================

CREATE TABLE IF NOT EXISTS scheduled_classes (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    iso_year    INT         NOT NULL,
    iso_week    INT         NOT NULL,
    starts_at   TIMESTAMPTZ NOT NULL,
    data        JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_scheduled_classes_tenant_week
    ON scheduled_classes (tenant_id, iso_year, iso_week) WHERE deleted_at IS NULL;

ALTER TABLE scheduled_classes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON scheduled_classes
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
