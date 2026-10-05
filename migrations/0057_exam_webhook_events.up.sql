-- =============================================================================
-- chora-delivery : 0057_exam_webhook_events.up.sql
--
-- Domain        : Content Delivery (5 core), Exam bounded context
-- Database      : chora_delivery
-- Date          : 2026-07-17
-- ADR           : ADR-193 D1 (franchise/satellite HMAC webhook federation,
--                 ACCEPTED 2026-06-25; O4a addendum 2026-07-16)
-- Story         : CHO-2230 (W5 FRANCHISE bypass-free slice)
--
-- Purpose:
--   exam_webhook_events is the durable dedup + resume gate for INBOUND
--   satellite exam-result deliveries (the chora-delivery receiver at
--   POST /api/v1/webhooks/exam-results). Structurally mirrors
--   chora_payments.stripe_webhook_events (ADR-164/ADR-188 receiver pattern):
--
--     - event_id (the satellite envelope UUIDv7) is the PRIMARY KEY
--     - idempotency_key (producer dedup key) is UNIQUE: the receive path
--       gates on it, so a replayed delivery returns the ORIGINAL outcome
--     - processed_at IS NULL = received-not-processed: the receiver
--       RE-DISPATCHES such rows on retry (never strand a result)
--     - result_id is the RESUME MARKER: set the moment the durable
--       exam_results row exists, BEFORE the released-event publish, so a
--       retried delivery resumes at publish instead of grading twice (one
--       idempotency_key can never mint two exam_results rows)
--
-- RLS: DELIBERATELY DISABLED (ADR-193 D1: "the dedup table is RLS-disabled
--   (operational)"). A satellite delivery arrives with NO Chora session; the
--   target tenant lives INSIDE the HMAC-verified payload and is resolved
--   post-dedup. Tenant isolation is enforced where the business rows land:
--   the exam_results write is fully RLS-scoped. This is an operational
--   receipt ledger (like stripe_webhook_events and the outbox), not a
--   tenant-business table; tenant_id is carried for attribution + forensics.
--
-- Grants: app_rw / app_ro inherit via 9999_grant_app_roles.sql default
--   privileges (no grant statements here; apply via the tracked runner).
--
-- Lifecycle: receipts are append + update-in-place bookkeeping; no
--   soft-delete column because rows are never deleted at all (the receipt IS
--   the audit trail of the delivery).
-- =============================================================================

CREATE TABLE IF NOT EXISTS exam_webhook_events (
    event_id         UUID         PRIMARY KEY,            -- satellite envelope UUIDv7
    idempotency_key  TEXT         NOT NULL UNIQUE,        -- producer dedup key (the gate)
    tenant_id        UUID         NOT NULL,               -- target tenant from the SIGNED payload
    received_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    processed_at     TIMESTAMPTZ,                         -- NULL = received-not-processed
    processing_error TEXT         NOT NULL DEFAULT '',    -- last failure reason (forensics)
    result_id        UUID                                  -- durable exam_results row (resume marker)
);

-- Ops scan: stranded (unprocessed) receipts, oldest first.
CREATE INDEX IF NOT EXISTS idx_exam_webhook_events_unprocessed
    ON exam_webhook_events (received_at)
    WHERE processed_at IS NULL;
