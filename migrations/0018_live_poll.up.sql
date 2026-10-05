-- =============================================================================
-- chora-delivery : 0018_live_poll.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : ADR-168 R+ classroom-realtime — gap 1b (true multi-pod, polls)
-- Date          : 2026-05-29
-- Architecture  : ADR-168 (docs/architecture/adrs/adr-168-classroom-realtime-
--                 redis-backplane.md) + docs/SMOKE_RPLUS_REALTIME_ADR168_2026-05-29.md
--
-- Purpose:
--   Persist the LivePoll aggregate so a participant's vote / WebSocket can
--   resolve the poll on ANY pod (the in-memory repo is per-pod → cross-pod
--   vote/WS 404s; the Redis backplane/tally are already cross-pod-correct —
--   this closes the poll-resolution gap, the sibling of 0017's quiz/session).
--
--   Storage = JSONB aggregate snapshot + extracted columns for keying/listing,
--   identical to 0017's live_quizzes / live_quiz_sessions.
--
--   LOSSLESS SNAPSHOT NOTE (why LivePoll was deferred from gap-1, now closed):
--     unlike LiveQuiz/LiveQuizSession (all fields exported → trivially lossless),
--     LivePoll carries an UNEXPORTED `voters` set (first-vote-wins idempotency)
--     that a default json.Marshal would DROP. The fix lives ON the aggregate:
--     LivePoll.MarshalJSON/UnmarshalJSON (domain/classroom/live_poll.go) emit +
--     rehydrate `voters` as a JSON array inside this `data` snapshot, so the
--     round-trip is lossless and an already-voted learner is still rejected
--     after cross-pod rehydration. NO separate `voters` child table — polls
--     resolve by id (voters need not be independently queryable) and a child
--     table would diverge from the established Session/Quiz JSONB-snapshot
--     pattern + add normalization debt.
--
-- ⚠ NO ROW LEVEL SECURITY (intentional, NOT an oversight — same rationale as
--   0017_live_classroom.up.sql):
--   the live-poll WS handler resolves a poll by ID ALONE — the WS connect
--   bypasses tenantRequired so the 101 upgrade succeeds (ADR-166 §D5), and that
--   id-keyed resolve IS the cross-pod path this migration enables. An RLS
--   tenant policy keyed on current_setting('chora.tenant_id') would return zero
--   rows for that tenant-less Get and break the feature. Tenant isolation stays
--   handler-enforced (handlers 404 on tenant mismatch — `p.TenantID != tenantID`),
--   exactly as the in-memory repo this replaces. Revisit if/when the WS connect
--   is made tenant-aware (would let RLS be added without breaking resolve).
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   set in 9999_grant_app_roles.sql (same mechanism 0009..0017 rely on).
-- =============================================================================

CREATE TABLE IF NOT EXISTS live_polls (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    state       TEXT        NOT NULL,
    data        JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_live_polls_tenant
    ON live_polls (tenant_id) WHERE deleted_at IS NULL;
