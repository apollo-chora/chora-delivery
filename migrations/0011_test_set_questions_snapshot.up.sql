-- =============================================================================
-- chora-delivery : 0011_test_set_questions_snapshot.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : agent — chora-delivery Fix-F (Lane A snapshot debt close)
-- Date          : 2026-05-16
-- Architecture  : Architecture Review locked 2026-05-07 + ADR-155 D2
--                 (deterministic MCQ grading) + ddd-enforcement.md #3
--                 (cross-DB queries FORBIDDEN — snapshot pattern).
--
-- Purpose:
--   Adds `payload_snapshot` + `snapshot_at` to `test_set_questions` so
--   chora-delivery can grade MCQ submissions deterministically without
--   querying chora_creation at runtime.
--
--   At TestSet.Publish() time the chora-delivery service makes a
--   service-to-service gRPC call to chora-creation's Question service
--   (chora.services.creation.v1.Creation/SnapshotQuestionByID — see
--   chora-contracts/proto/services/creation/v1/creation.proto) to fetch
--   the canonical MCQ payload (options + correct_options + scoring config),
--   then stores it inline as JSONB on each test_set_questions row. The
--   sync gRPC is acceptable here because:
--
--     - Authoring-time one-shot — NOT a per-request runtime call
--     - Atomic with the DRAFT → PUBLISHED state transition
--     - Deterministic (no LLM); per `feedback_agentic_pubsub_only` the
--       Pub/Sub mandate applies only to LLM-bearing operations.
--
--   Grading at /submit reads payload_snapshot to determine correctness
--   (see internal/domain/delivery/submission.go GradeMCQAnswer).
--
-- Cross-DB query rule preserved:
--   - chora-delivery NEVER queries chora_creation.questions at runtime
--   - The snapshot is taken via gRPC (no Postgres dblink, no FK)
--
-- Resilience-priority directive (feedback_resilience_priority +
-- feedback_no_stubs_real_wiring):
--   - payload_snapshot is NULL for DRAFT rows; SET to non-null at publish
--   - When NULL at grade time, GradeMCQAnswer fails loud (zero-credit per-
--     answer + structured error event) rather than silently green-pathing
--   - snapshot_at is the authoring-side timestamp at gRPC fetch (not the
--     wall clock at the chora-delivery insert)
--
-- Per Fix-E reservation: 0012 is reserved for the grading_inbox dedup
-- migration (in flight in parallel). DO NOT renumber.
-- =============================================================================

BEGIN;

ALTER TABLE test_set_questions
    ADD COLUMN payload_snapshot JSONB,
    ADD COLUMN snapshot_at      TIMESTAMPTZ;

COMMENT ON COLUMN test_set_questions.payload_snapshot IS
    'Snapshot of chora_creation.questions.{mcq_payload|oe_payload} at test-set publish time. Captures correct_options (MCQ) + stem + options + scoring config + model_answer (OE) for deterministic grading per ADR-155. Per ddd-enforcement #3 cross-DB queries forbidden; this is the snapshot pattern.';

COMMENT ON COLUMN test_set_questions.snapshot_at IS
    'When the payload_snapshot was captured (authoring-side timestamp from chora-creation, not chora-delivery wall clock). NULL until parent test-set is PUBLISHED.';

-- Partial index — only PUBLISHED rows carry snapshots, so the index trims
-- dead weight from DRAFT rows.
CREATE INDEX IF NOT EXISTS idx_test_set_questions_with_snapshot
    ON test_set_questions (test_set_id)
    WHERE payload_snapshot IS NOT NULL AND deleted_at IS NULL;

COMMIT;

-- =============================================================================
-- VERIFICATION (run manually after apply):
--
--   SET ROLE chora_delivery_app_rw;
--   BEGIN;
--     SET LOCAL chora.tenant_id = '11111111-1111-7111-8111-111111111111';
--     -- After Publish(), payload_snapshot is non-null
--     SELECT count(*) FROM test_set_questions
--      WHERE payload_snapshot IS NOT NULL;
--   ROLLBACK;
--   RESET ROLE;
-- =============================================================================
