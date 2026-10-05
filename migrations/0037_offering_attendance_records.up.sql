-- 0037_offering_attendance_records.up.sql — session-scoped attendance Record
-- table (R+ Four-Mode, Attendance tab).
--
-- NAMED offering_attendance_records (NOT attendance_records — that name is
-- already taken in chora_delivery by the class-scoped legacy attendance table
-- with a different schema). One learner's presence mark for one OfferingSession.
-- session_id references offering_sessions.id as a cross-aggregate UUID (no FK,
-- ddd-enforcement #3). Storage = lossless JSONB snapshot in `data` + extracted
-- columns for RLS (tenant_id), the per-session list (session_id) and idempotency.
--
-- The UNIQUE(tenant_id, session_id, gcid) is the natural key: a re-mark upserts
-- (ON CONFLICT DO UPDATE) instead of duplicating, so an instructor can correct a
-- status (present→late) and the register stays one-row-per-learner-per-session.
--
-- Storage + RLS mirror 0036_offering_sessions (NULLIF-safe cast + WITH CHECK).
-- No per-table grants: app_rw / app_ro inherit via 9999_grant_app_roles.sql.

CREATE TABLE IF NOT EXISTS offering_attendance_records (
    id           UUID PRIMARY KEY,
    tenant_id    UUID        NOT NULL,
    session_id   UUID        NOT NULL,
    gcid         TEXT        NOT NULL,
    recorded_at  TIMESTAMPTZ NOT NULL,
    data         JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, session_id, gcid)
);

CREATE INDEX IF NOT EXISTS idx_offering_attendance_records_tenant_session
    ON offering_attendance_records (tenant_id, session_id);

ALTER TABLE offering_attendance_records ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON offering_attendance_records
    USING      (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);
