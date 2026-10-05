-- =============================================================================
-- chora-delivery : 0035_offerings_drop_courseid_key.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Date          : 2026-06-28
-- Story         : offering→course one-to-many (course multi-select)
--
-- Purpose:
--   CONTRACT step of the expand/contract pair started in 0034. The Offering
--   aggregate snapshot (JSONB `data`) migrated from a single `CourseID` to a
--   `CourseIDs` (array) field. 0034 was the EXPAND step: it KEPT the legacy
--   `CourseID` key alongside the new `CourseIDs` so OLD pods (still reading
--   data.CourseID) kept working through the deploy window.
--
--   All pods now read/write `CourseIDs[]` (the Offering aggregate has NO
--   singular CourseID field; a re-save already omits the key via json.Marshal).
--   The residual legacy `CourseID` key is therefore dead weight — this
--   migration removes it from every snapshot that still carries it.
--
--   The write-only `course_id` EXTRACT COLUMN is UNCHANGED — it is still
--   written from Offering.PrimaryCourseID() for RLS scoping / keying / listing.
--   ONLY the JSONB `data.CourseID` key is dropped here.
--
--   Note: domain structs carry NO json tags, so the JSONB key is the Go FIELD
--   name `CourseID` (PascalCase), NOT snake_case.
--
-- Idempotent: the WHERE guard skips rows that no longer carry the legacy key.
-- =============================================================================

UPDATE offerings
SET data = data - 'CourseID'
WHERE data ? 'CourseID';
