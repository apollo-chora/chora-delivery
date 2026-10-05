-- =============================================================================
-- chora-delivery : 0016_cj2_published_public_backfill.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : agent — chora-delivery E2E-BE-CJ2-CATALOG-PROJECTION
-- Date          : 2026-05-24
-- Architecture  : Aligned with CJ#2 spec at
--                 `docs/m13/e2e-fe-coord-directive-2026-05-16.md` §3 +
--                 view definition `0006_public_catalog_view.sql`.
--
-- Purpose:
--   Backfill `public = TRUE` for CJ#2 courses whose state has already
--   reached `PUBLISHED` but were persisted with `public = FALSE` by the
--   pre-fix `SaveCJ2` adapter (`services/chora-delivery/internal/adapter/
--   repo/pg/course_cj2.go`, prior to E2E-BE-CJ2-CATALOG-PROJECTION fix).
--
--   The `public_courses_catalog` view filters
--   `WHERE public = TRUE AND deleted_at IS NULL`, so PUBLISHED CJ#2
--   courses written before this migration are invisible to
--   `/api/catalog?public=true`. The companion `SaveCJ2` change binds
--   `public = (state == PUBLISHED)` going forward; this migration
--   converges the existing rows.
--
-- Aggregates touched: courses (existing — migrations 0001 + 0014).
--
-- Resilience-priority directive (feedback_resilience_priority):
--   - Idempotent (`WHERE public IS DISTINCT FROM TRUE`) so dead-pod resume
--     re-runs converge.
--   - Single transactional unit + non-destructive (no DELETEs, no
--     constraint changes); a partial-apply roll-back loses nothing.
--   - No data loss — `public` is the only column changed; every other
--     column on the row stays untouched.
--
-- HARD RULE per ddd-enforcement.md: cross-database queries forbidden.
-- This migration only touches chora_delivery.courses.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- 1) Backfill public=TRUE for already-PUBLISHED CJ#2 courses.
--
--    `state` was added by 0014_cj2_course_state_fsm.up.sql; pre-CJ#2 rows
--    DEFAULT to 'DRAFT' so the WHERE clause naturally skips them.
--    `IS DISTINCT FROM TRUE` makes the UPDATE idempotent — a re-apply
--    after the SaveCJ2 fix is shipped is a no-op.
-- -----------------------------------------------------------------------------

UPDATE courses
   SET public     = TRUE,
       updated_at = updated_at  -- preserve original (no synthetic touch)
 WHERE state      = 'PUBLISHED'
   AND deleted_at IS NULL
   AND public IS DISTINCT FROM TRUE;

COMMIT;

-- =============================================================================
-- VERIFICATION (run manually after apply):
--
--   -- 1. Every PUBLISHED CJ#2 course is now public=true.
--   SELECT count(*) AS still_hidden
--     FROM courses
--    WHERE state = 'PUBLISHED'
--      AND deleted_at IS NULL
--      AND public IS NOT TRUE;
--   -- Expected: 0
--
--   -- 2. Catalog view now sees the Phyllis CJ#2 smoke course.
--   SELECT course_id, title
--     FROM public_courses_catalog
--    WHERE course_id = '019e58ea-5ae0-7b74-9f1d-73a92622ab90';
--   -- Expected: 1 row ('Phyllis CJ#2 Smoke — Software Engineering Foundations')
--
--   -- 3. Idempotent rerun (should affect 0 rows).
--   BEGIN;
--     UPDATE courses
--        SET public = TRUE
--      WHERE state  = 'PUBLISHED'
--        AND deleted_at IS NULL
--        AND public IS DISTINCT FROM TRUE;
--     -- Expected: UPDATE 0
--   ROLLBACK;
-- =============================================================================
