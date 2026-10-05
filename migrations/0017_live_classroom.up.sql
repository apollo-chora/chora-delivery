-- =============================================================================
-- chora-delivery : 0017_live_classroom.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : ADR-168 R+ classroom-realtime — gap 1 (true multi-pod)
-- Date          : 2026-05-29
-- Architecture  : ADR-168 (docs/architecture/adrs/adr-168-classroom-realtime-
--                 redis-backplane.md) + docs/SMOKE_RPLUS_REALTIME_ADR168_2026-05-29.md
--
-- Purpose:
--   Persist the LiveQuiz + LiveQuizSession aggregates so a participant's
--   WebSocket can resolve the session on ANY pod (the in-memory repos are
--   per-pod → cross-pod WS 404s; the Redis backplane/tally/ZSET are already
--   cross-pod-correct — this closes the session-resolution gap).
--
--   Storage = JSONB aggregate snapshot + extracted columns for keying/listing.
--   Both aggregates are fully exported (no hidden state) so the JSON round-trip
--   is lossless. The pg repos json.Marshal the aggregate into `data` and
--   json.Unmarshal it back on Get/List.
--
-- ⚠ NO ROW LEVEL SECURITY (intentional, NOT an oversight):
--   the live-quiz WS handler resolves a session by ID ALONE — the WS connect
--   bypasses tenantRequired so the 101 upgrade succeeds (ADR-166 §D5), and that
--   id-keyed resolve IS the cross-pod path this migration enables. An RLS
--   tenant policy keyed on current_setting('chora.tenant_id') would return zero
--   rows for that tenant-less Get and break the feature. Tenant isolation stays
--   handler-enforced (handlers 404 on tenant mismatch — `s.TenantID != tenantID`),
--   exactly as the in-memory repos these replace. Revisit if/when the WS connect
--   is made tenant-aware (would let RLS be added without breaking resolve).
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   set in 9999_grant_app_roles.sql (same mechanism 0009..0016 rely on).
-- =============================================================================

CREATE TABLE IF NOT EXISTS live_quizzes (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    state       TEXT        NOT NULL,
    data        JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_live_quizzes_tenant
    ON live_quizzes (tenant_id) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS live_quiz_sessions (
    id            UUID PRIMARY KEY,
    tenant_id     UUID        NOT NULL,
    live_quiz_id  UUID        NOT NULL,
    state         TEXT        NOT NULL,
    data          JSONB       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_live_quiz_sessions_tenant
    ON live_quiz_sessions (tenant_id) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_live_quiz_sessions_quiz
    ON live_quiz_sessions (live_quiz_id) WHERE deleted_at IS NULL;
