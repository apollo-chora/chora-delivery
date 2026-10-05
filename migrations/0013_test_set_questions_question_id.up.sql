-- =============================================================================
-- chora-delivery : 0013_test_set_questions_question_id.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : agent — chora-delivery LEG3-D R3 Option B
-- Date          : 2026-05-16
-- Architecture  : Architecture Review locked 2026-05-07 +
--                 ddd-enforcement.md #3 (cross-DB FORBIDDEN) +
--                 ADR-155 D5 (deterministic snapshot) +
--                 docs/m13/wave3-leg3d-round3-blocker-2026-05-16.md
--                 + user "go B" decision 2026-05-16.
--
-- Purpose:
--   Adds `question_id` column to `test_set_questions` so chora-delivery
--   stores TWO distinct cross-domain references on each inclusion row:
--
--     - `question_atom_id` — UUIDv7 of the LearningAtom in chora_creation
--                            (e.g., `00000000-0000-7000-8000-00000000a0a2`).
--                            The canonical authoring identifier.
--     - `question_id`      — UUIDv7 of the Question embedded inside the
--                            atom's payload (e.g.,
--                            `mcq_payload.question_id` =
--                            `019e2ba6-7353-73f5-b517-dd627e76d450`).
--                            The identifier consumed by
--                            chora.services.creation.v1.Creation/
--                            SnapshotQuestionByID at PublishWithSnapshot
--                            time.
--
--   Pre-LEG3-D R3 the two were conflated: the handler stored only
--   `question_atom_id` and the snapshotter caller passed that value to
--   chora-creation, which returned NotFound (atom UUID != question UUID).
--   The publish path 502'd.
--
--   Per user decision 2026-05-16 (Option B in the blocker doc) the FE
--   picker now extracts `mcq_payload.question_id` from the loaded atom
--   payload and POSTs it explicitly alongside `question_atom_id`.
--   chora-delivery stores both verbatim; the snapshotter caller uses
--   `question_id` (not `question_atom_id`) at publish time.
--
-- Cross-DB query rule preserved:
--   - chora-delivery NEVER queries chora_creation at add-question time
--   - The cross-domain link is the stored UUID (no FK)
--   - SnapshotQuestionByID is the sanctioned sync gRPC at publish time
--
-- Backfill (one-shot, inline) — fail-loud preference per
-- `feedback_no_stubs_real_wiring` says NOT NULL from the start. Existing
-- DRAFT rows were created with `question_atom_id` standing in for
-- `question_id` (the legacy conflated model). Backfill copies the atom_id
-- over so the NOT NULL constraint holds; instructors who want a
-- semantically correct snapshot can re-create the inclusion via the
-- picker UI (which now resolves the real embedded question_id) before
-- republishing. PUBLISHED rows already carry a populated
-- `payload_snapshot` so grading is unaffected.
--
-- Resilience-priority directive: NOT NULL after backfill so a future
-- handler can rely on the column being present.
-- =============================================================================

BEGIN;

-- Step 1 — Add the column nullable so backfill can run.
ALTER TABLE test_set_questions
    ADD COLUMN question_id UUID;

COMMENT ON COLUMN test_set_questions.question_id IS
    'LEG3-D R3 Option B — embedded Question UUID inside the atom''s payload (mcq_payload.question_id / oe_payload.question_id). Distinct from question_atom_id. Consumed by chora.services.creation.v1.Creation/SnapshotQuestionByID at TestSet.PublishWithSnapshot time. Pre-LEG3-D-R3 the two were conflated; backfill copies question_atom_id over for legacy DRAFT rows. New rows MUST receive a distinct value from the picker UI.';

-- Step 2 — Backfill legacy rows. Old DRAFT rows treated atom_id as the
-- question_id; the conflation is harmless at the data layer (the column
-- carries a UUID either way) but PUBLISHED rows already have the real
-- payload locked in `payload_snapshot`. Re-publish would update both.
UPDATE test_set_questions
   SET question_id = question_atom_id
 WHERE question_id IS NULL;

-- Step 3 — Lock in the NOT NULL constraint.
ALTER TABLE test_set_questions
    ALTER COLUMN question_id SET NOT NULL;

-- Step 4 — Index on (question_id) for the eventual reverse lookup of
-- "which test_set_questions reference this Question UUID". Partial on
-- non-deleted rows matches the existing `idx_test_set_questions_atom`
-- pattern.
CREATE INDEX IF NOT EXISTS idx_test_set_questions_question_id
    ON test_set_questions (question_id)
    WHERE deleted_at IS NULL;

COMMIT;

-- =============================================================================
-- VERIFICATION (run manually after apply):
--
--   SET ROLE chora_delivery_app_rw;
--   BEGIN;
--     SET LOCAL chora.tenant_id = '11111111-1111-7111-8111-111111111111';
--     -- Every row now carries a non-null question_id:
--     SELECT count(*) FROM test_set_questions WHERE question_id IS NULL;  -- 0
--     -- Legacy rows: question_id == question_atom_id (backfill).
--     SELECT count(*)
--       FROM test_set_questions
--      WHERE question_id = question_atom_id;  -- equal to pre-migration row-count
--   ROLLBACK;
--   RESET ROLE;
-- =============================================================================
