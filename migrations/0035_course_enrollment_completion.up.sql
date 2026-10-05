-- =============================================================================
-- chora-delivery : 0035_course_enrollment_completion.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : Enrollment-completion lifecycle (chora.delivery.enrollment.completed.v1)
-- Date          : 2026-06-28
--
-- Purpose:
--   Give course_enrollments (0001_initial) a completion lifecycle so the
--   per-learner fact chora.delivery.enrollment.completed.v1 can flow. Adds:
--     - status        : active | completed | cancelled (reconciles deleted_at)
--     - completed_at   : when Complete() ran (NULL until completed)
--     - passed         : met the passing requirements (NULL until completed;
--                        nullable so "completed-without-passing" is distinct
--                        from "not yet completed")
--
--   Backfill: every pre-existing row gets the 'active' default; rows already
--   soft-deleted (deleted_at IS NOT NULL) are reconciled to 'cancelled' so the
--   status column and deleted_at never disagree (matches Enrollment.SoftDelete).
--
-- ROW LEVEL SECURITY: unchanged — the tenant_isolation policy from 0001 is
--   FOR ALL, so it already covers the completion UPDATE. No new policy / grant
--   (app_rw / app_ro inherit). Completion writes run under rls.ApplySession
--   like every other course_enrollments query.
--
-- Idempotent (ADD COLUMN IF NOT EXISTS + guarded CHECK + bounded backfill UPDATE)
-- per the migration-runner fail-fast lesson (project_migration_runner_failfast_wedge):
-- a re-run re-asserts the same state and never errors.
-- =============================================================================

ALTER TABLE course_enrollments ADD COLUMN IF NOT EXISTS status       TEXT NOT NULL DEFAULT 'active';
ALTER TABLE course_enrollments ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;
ALTER TABLE course_enrollments ADD COLUMN IF NOT EXISTS passed       BOOLEAN;

-- DB-layer integrity for the lifecycle enum (guarded so the migration is
-- idempotent — ADD CONSTRAINT has no IF NOT EXISTS form).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'course_enrollments_status_check'
    ) THEN
        ALTER TABLE course_enrollments
            ADD CONSTRAINT course_enrollments_status_check
            CHECK (status IN ('active', 'completed', 'cancelled'));
    END IF;
END $$;

-- Backfill: reconcile already-soft-deleted rows to 'cancelled'. Bounded +
-- idempotent (only flips rows still carrying the 'active' default).
UPDATE course_enrollments
   SET status = 'cancelled'
 WHERE deleted_at IS NOT NULL
   AND status = 'active';
