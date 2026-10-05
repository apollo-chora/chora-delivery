-- =============================================================================
-- chora-delivery : 0035_offerings_drop_courseid_key.down.sql
--
-- Reverse of 0035 (the CONTRACT step): restore the legacy `CourseID` JSONB key
-- from the first element of `CourseIDs` — re-establishing the expand-window
-- coexistence that 0034 created, so older pods reading data.CourseID work
-- again. Mirrors 0034's down idiom (CourseIDs[0] is the PRIMARY course).
--
-- The write-only `course_id` extract column is untouched (0035 never altered
-- it). Lossy only in the sense that the restored key holds just the primary
-- course — which is exactly what a single `CourseID` meant pre-0034.
--
-- Idempotent: only touches rows that carry `CourseIDs` and do NOT already carry
-- the legacy `CourseID` key.
-- =============================================================================

UPDATE offerings
SET data = data || jsonb_build_object('CourseID', data -> 'CourseIDs' -> 0)
WHERE data ? 'CourseIDs'
  AND NOT (data ? 'CourseID');
