-- =============================================================================
-- chora-delivery : 0028_test_sets_source_job_id.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : agent — Lane 1c W4 batch→test-set compose (CHO-1703)
-- Date          : 2026-06-10
-- Architecture  : Architecture Review locked 2026-05-07 + ADR-180 D10
--                 (event-driven creation→delivery test-set assembly) +
--                 ddd-enforcement.md #3 (cross-DB queries FORBIDDEN —
--                 cross-domain UUID reference without FK).
--
-- Purpose:
--   Adds `source_job_id` to `test_sets` — the UUIDv7 of the chora-creation
--   batch question-generation job whose
--   `chora.creation.question_batch.accepted.v1` event assembled this test
--   set (delivery-test-sets.yaml v1.1.0). NULL for hand-authored test sets.
--
--   The UNIQUE partial index is the subscriber's durable idempotency
--   backstop: Pub/Sub is at-least-once, so duplicate event delivery — or a
--   concurrent redelivery racing two pods past the read-side existence
--   check — collapses onto this index instead of minting a second test set.
--
-- Cross-DB query rule preserved:
--   - source_job_id references chora_creation.question_generation_jobs by
--     UUID ONLY — no FK, no dblink. The value arrives exclusively via the
--     Pub/Sub event payload.
--
-- RLS: the existing `tenant_isolation` policy on test_sets (0009) covers
-- the new column — no policy change. The index is intentionally global
-- (not tenant-composite): job_id is a UUIDv7 minted by chora-creation,
-- so non-null values are globally unique by construction and the contract
-- ("UNIQUE among non-null") is tenant-agnostic.
-- =============================================================================

BEGIN;

ALTER TABLE test_sets
    ADD COLUMN source_job_id UUID;

COMMENT ON COLUMN test_sets.source_job_id IS
    'UUIDv7 of the chora-creation batch question_generation_jobs row whose chora.creation.question_batch.accepted.v1 event assembled this test set (Lane 1c / ADR-180 D10). NULL for hand-authored test sets. Cross-domain UUID reference without FK per ddd-enforcement #3. UNIQUE among non-null values — the event subscriber''s idempotency key.';

-- Subscriber idempotency backstop — duplicate event delivery (at-least-once
-- Pub/Sub) can never mint a second test set for the same batch job.
CREATE UNIQUE INDEX idx_test_sets_source_job_id
    ON test_sets (source_job_id)
    WHERE source_job_id IS NOT NULL;

COMMIT;

-- =============================================================================
-- VERIFICATION (run manually after apply):
--
--   SET ROLE chora_delivery_app_rw;
--   BEGIN;
--     SET LOCAL chora.tenant_id = '11111111-1111-7111-8111-111111111111';
--     INSERT INTO test_sets (test_set_id, tenant_id, author_gcid, title, source_job_id)
--     VALUES (
--         gen_random_uuid(),
--         '11111111-1111-7111-8111-111111111111',
--         '00000000-0000-7000-8000-000000001999',
--         'Batch-assembled test set',
--         '01985e7f-0000-7000-8000-00000000aaaa'
--     );
--     -- Duplicate source_job_id must fail with 23505 on
--     -- idx_test_sets_source_job_id:
--     INSERT INTO test_sets (test_set_id, tenant_id, author_gcid, title, source_job_id)
--     VALUES (
--         gen_random_uuid(),
--         '11111111-1111-7111-8111-111111111111',
--         '00000000-0000-7000-8000-000000001999',
--         'Duplicate must fail',
--         '01985e7f-0000-7000-8000-00000000aaaa'
--     );
--   ROLLBACK;
--   RESET ROLE;
-- =============================================================================
