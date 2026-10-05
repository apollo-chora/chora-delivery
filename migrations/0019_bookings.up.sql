-- =============================================================================
-- chora-delivery : 0019_bookings.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : HANDOFF_RPLUS §6 follow-up — Bookings BE (GET-list + pg-back)
-- Date          : 2026-06-01
-- Story         : CHO-1580 (R+ Stage C-lite) — Bookings BE durability
--
-- Purpose:
--   Persist the Booking aggregate so a class enrolment survives a pod restart
--   and is listable via GET /api/bookings. Before this table, bookings lived
--   only in inmem.BookingRepo — rows were ephemeral and there was no GET-list,
--   so the R+ FE (CHO-1620 T3) had to session-track created bookings. This is
--   the [[feedback-no-stubs-real-wiring]] + [[feedback-resilience-priority]]
--   close, the delivery-domain sibling of 0001's course_enrollments.
--
--   Storage = flat columns (the Booking aggregate is a simple value record —
--   id/class/course/learner/status/timestamps), NOT a JSONB snapshot. This
--   diverges from 0017/0018's live_quizzes/live_polls (which carry rich nested
--   classroom-realtime state) and matches the column-shaped course_enrollments
--   the pg.BookingRepo SELECTs mirror.
--
-- NO FOREIGN KEYS (intentional): the booking-create path validates the parent
--   class in-app via the in-memory ClassRepo (deps.Classes) — classes are NOT
--   pg-backed in this path, so a class_id/course_id FK would violate on every
--   insert. Cross-aggregate references stay UUID-without-FK per
--   .claude/rules/ddd-enforcement.md §"Aggregate Invariants" (3).
--
-- NO updated_at trigger (intentional, same as 0018_live_polls): the domain
--   owns updated_at — Booking.TransitionStatus stamps it and the
--   pg.BookingRepo UPSERT writes EXCLUDED.updated_at, so a DB-side
--   BEFORE UPDATE trigger would clobber the domain's authoritative timestamp.
--
-- ROW LEVEL SECURITY (tenant_isolation): bookings is genuinely tenant-scoped on
--   every access path (create / status-PATCH / list all carry X-Tenant-Id), so
--   — unlike 0017/0018 whose tenant-less WS by-id resolve forced handler-only
--   isolation — RLS is enabled here for defence-in-depth. pg.BookingRepo calls
--   rls.ApplySession (SET LOCAL chora.tenant_id) before every query; the policy
--   below makes Postgres enforce the same scope the handler checks.
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   set in 9999_grant_app_roles.sql (same mechanism 0009..0018 rely on).
-- =============================================================================

CREATE TABLE IF NOT EXISTS bookings (
    id            UUID        PRIMARY KEY,
    tenant_id     UUID        NOT NULL,
    class_id      UUID        NOT NULL,
    course_id     UUID        NOT NULL,
    learner_gcid  UUID        NOT NULL,
    status        TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_bookings_tenant
    ON bookings (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bookings_class
    ON bookings (class_id) WHERE deleted_at IS NULL;

ALTER TABLE bookings ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bookings
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
