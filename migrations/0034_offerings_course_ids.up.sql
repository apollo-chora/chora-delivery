-- =============================================================================
-- chora-delivery : 0034_offerings_course_ids.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Date          : 2026-06-28
-- Story         : offering→course one-to-many (course multi-select)
--
-- Purpose:
--   Offering→course becomes ONE-TO-MANY: an offering bundles ≥1 course. The
--   aggregate snapshot (JSONB `data`) gains a `CourseIDs` (array) field. This
--   migration backfills existing rows: data.CourseIDs = [data.CourseID].
--
--   EXPAND step (expand/contract): the legacy `CourseID` key is KEPT alongside
--   the new `CourseIDs` so OLD pods (still reading data.CourseID) keep working
--   through the deploy window. New pods read/write `CourseIDs`; a re-save drops
--   the stale `CourseID` from that row. A follow-up CONTRACT migration removes
--   the residual `CourseID` once all pods are new.
--
--   The write-only `course_id` extract column is UNCHANGED (it now holds the
--   PRIMARY/first course; new code writes Offering.PrimaryCourseID()). Reads go
--   through the JSONB `data`. RLS (tenant_isolation) is unaffected (tenant_id).
--
-- Idempotent: the WHERE guard skips rows already backfilled.
-- =============================================================================

UPDATE offerings
SET data = data || jsonb_build_object('CourseIDs', jsonb_build_array(data -> 'CourseID'))
WHERE data ? 'CourseID'
  AND NOT (data ? 'CourseIDs');
