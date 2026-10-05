-- =============================================================================
-- chora-delivery : 0014_cj2_course_state_fsm.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : agent — chora-delivery E2E-BE-CJ2 (Customer Journey #2)
-- Date          : 2026-05-16
-- Architecture  : CJ#2 directive row at
--                 `docs/m13/e2e-fe-coord-directive-2026-05-16.md` §3 +
--                 spec at `docs/TODO-DEVELOPMENT.md` "Customer Journey #2".
--
-- Aggregates touched: courses (existing — migration 0001_initial.sql).
--
-- Purpose:
--   Adds 8 new columns + 1 index + 1 RLS policy to courses table to support
--   CJ#2 state-FSM authoring + release flow:
--
--     state              — lifecycle state (DRAFT / AWAITING_REVIEW /
--                          PUBLISHED / ARCHIVED). Default DRAFT.
--     author_gcid        — UUID of the authoring instructor (separate from
--                          existing instructor_gcid which becomes the
--                          delivery-time owner; author is the
--                          identity that called POST /api/v1/courses).
--     test_set_ids       — UUID[] cross-aggregate references into
--                          chora_delivery.test_sets (same-DB soft FK per
--                          ddd-enforcement #3).
--     instructor_gcids   — UUID[] training-admin-set roster (populated at
--                          /release time).
--     scheduled_open_at  — TIMESTAMPTZ when enrolment opens (optional).
--     review_notes       — TEXT free-form feedback set on /reject.
--     learning_objectives — TEXT[] author-supplied learning outcomes.
--     prerequisites      — TEXT[] author-supplied prerequisites.
--     published_at       — TIMESTAMPTZ when /release transitioned to
--                          PUBLISHED.
--
-- Resilience-priority directive (feedback_resilience_priority):
--   - `IF NOT EXISTS` for every column + index + policy so the migration
--     is idempotent under dead-pod resume.
--   - Single transactional unit; if the migrate pod dies mid-apply,
--     replacement re-runs cleanly.
--   - No data loss — existing course rows default `state='DRAFT'`,
--     `author_gcid := instructor_gcid` (back-fill keeps legacy rows
--     consistent), `test_set_ids := '{}'`, `learning_objectives := '{}'`,
--     `prerequisites := '{}'`.
--
-- RLS:
--   - Existing `tenant_isolation` policy from 0001_initial.sql stays in
--     effect (filters by `chora.tenant_id`).
--   - This migration does NOT add a state-aware visibility policy — that
--     gate is enforced at the application layer (handler RBAC + WHERE
--     clauses in CourseRepo.ListByState). The reason: per-row visibility
--     decisions depend on caller role + author_gcid match, which require
--     the caller's GCID + role set as session vars; the handler is the
--     canonical authority for that gate per the integrative-UI pattern
--     (instructor + training-admin compose roles in the same surface).
--
-- HARD RULE per ddd-enforcement.md: cross-database queries forbidden.
-- This migration only touches chora_delivery.courses.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- 1) Add the 9 new columns (idempotent via IF NOT EXISTS)
-- -----------------------------------------------------------------------------

ALTER TABLE courses
    ADD COLUMN IF NOT EXISTS state TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (state IN ('DRAFT', 'AWAITING_REVIEW', 'PUBLISHED', 'ARCHIVED'));

ALTER TABLE courses
    ADD COLUMN IF NOT EXISTS author_gcid UUID;

ALTER TABLE courses
    ADD COLUMN IF NOT EXISTS test_set_ids UUID[] NOT NULL DEFAULT '{}';

ALTER TABLE courses
    ADD COLUMN IF NOT EXISTS instructor_gcids UUID[];

ALTER TABLE courses
    ADD COLUMN IF NOT EXISTS scheduled_open_at TIMESTAMPTZ;

ALTER TABLE courses
    ADD COLUMN IF NOT EXISTS review_notes TEXT;

ALTER TABLE courses
    ADD COLUMN IF NOT EXISTS learning_objectives TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE courses
    ADD COLUMN IF NOT EXISTS prerequisites TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE courses
    ADD COLUMN IF NOT EXISTS published_at TIMESTAMPTZ;

-- -----------------------------------------------------------------------------
-- 2) Back-fill author_gcid from instructor_gcid for any pre-existing rows
--    so the NOT-NULL-equivalent invariant holds in application code.
-- -----------------------------------------------------------------------------

UPDATE courses
   SET author_gcid = instructor_gcid
 WHERE author_gcid IS NULL
   AND instructor_gcid IS NOT NULL;

-- -----------------------------------------------------------------------------
-- 3) Index — (tenant_id, state) supports the R+ admin queue
--    GET /api/v1/courses?state=AWAITING_REVIEW + the /a/catalog filter.
-- -----------------------------------------------------------------------------

CREATE INDEX IF NOT EXISTS courses_state_tenant_idx
    ON courses (tenant_id, state)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS courses_author_state_idx
    ON courses (author_gcid, state)
    WHERE deleted_at IS NULL;

COMMIT;

-- =============================================================================
-- VERIFICATION (run manually after apply):
--
--   -- New columns exist
--   SELECT column_name, data_type FROM information_schema.columns
--    WHERE table_name = 'courses'
--      AND column_name IN ('state', 'author_gcid', 'test_set_ids',
--                          'instructor_gcids', 'scheduled_open_at',
--                          'review_notes', 'learning_objectives',
--                          'prerequisites', 'published_at')
--    ORDER BY column_name;
--   -- Expected: 9 rows.
--
--   -- State CHECK is in place
--   SELECT pg_get_constraintdef(c.oid)
--     FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid
--    WHERE t.relname = 'courses' AND c.contype = 'c';
--
--   -- Index visible
--   SELECT indexname FROM pg_indexes WHERE tablename = 'courses'
--    AND indexname LIKE 'courses_%state%';
--   -- Expected: courses_state_tenant_idx + courses_author_state_idx.
-- =============================================================================
