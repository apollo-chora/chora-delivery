-- 0008_outbox_wipe_json_pending.up.sql
--
-- Outbox payload encoding migration (codebase-wide JSON→binary-protobuf fix
-- #33, chora-delivery slice).
--
-- Background
-- ----------
-- Pre-fix outbox rows on chora_delivery.outbox_events held JSON-marshalled
-- payload bytes that the binary Schema Registry (BINARY encoding) rejects at
-- publish time with "Invalid binary proto message". The dispatcher retries
-- until MaxAttempts and the row dead-letters; the BINARY-attached topic
-- continues to receive failures forever for each retry.
--
-- Fix
-- ---
-- internal/adapter/events/protomarshal now emits canonical binary protobuf
-- bytes for the 10 BINARY-encoded chora.delivery.* topics that have a
-- Schema Registry schema attached on chora-489812:
--
--   * chora.delivery.course.created.v1
--   * chora.delivery.booking.confirmed.v1
--   * chora.delivery.certification.issued.v1
--   * chora.delivery.application.submitted.v1
--   * chora.delivery.application.offer_made.v1
--   * chora.delivery.application.accepted.v1
--   * chora.delivery.application.withdrawn.v1
--   * chora.delivery.application.rejected.v1
--   * chora.delivery.application.paid.v1
--   * chora.delivery.application.enrolled.v1
--
-- New rows written after the fix carry binary bytes and publish cleanly.
-- This migration drains pre-fix JSON-payload pending rows so the dispatcher
-- stops retrying them; rows that NEVER successfully published are safe to
-- mark 'failed' — no downstream subscriber ever saw them.
--
-- Topics WITHOUT a Schema Registry schema attached are NOT touched — JSON
-- payloads publish cleanly on unattached topics, no replay is needed:
--   * chora.delivery.course.updated.v1
--   * chora.delivery.course.published.v1
--   * chora.delivery.enrollment.created.v1
--   * chora.delivery.enrollment.cancelled.v1
--   * chora.delivery.application.under_review.v1
--
-- Replay strategy
-- ---------------
-- Mark-failed rather than translate-and-retry: the producer-side handlers
-- (HTTP /v1/courses /v1/enrollments /v1/bookings + Stripe webhook + Singpass
-- applications) are idempotent on envelope.idempotency_key — re-triggering
-- the user / admin flow emits a fresh correctly-encoded outbox row. The
-- application aggregate's append-only state history also lets the operator
-- replay (PublishApplicationStateChanged) for any stuck application.
--
-- Translating JSON-decoded fields back into the typed proto would be more
-- error-prone than re-emission.
--
-- Idempotent: re-running is a no-op (the WHERE clause matches no rows after
-- the first pass).
UPDATE outbox_events
SET status          = 'failed',
    last_error      = 'codebase-wide outbox protobuf encoding fix #33 — pre-fix JSON-payload row drained',
    last_attempt_at = now()
WHERE status = 'pending'
  AND topic IN (
    'chora.delivery.course.created.v1',
    'chora.delivery.booking.confirmed.v1',
    'chora.delivery.certification.issued.v1',
    'chora.delivery.application.submitted.v1',
    'chora.delivery.application.offer_made.v1',
    'chora.delivery.application.accepted.v1',
    'chora.delivery.application.withdrawn.v1',
    'chora.delivery.application.rejected.v1',
    'chora.delivery.application.paid.v1',
    'chora.delivery.application.enrolled.v1'
  );
