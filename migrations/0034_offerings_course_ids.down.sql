-- =============================================================================
-- chora-delivery : 0034_offerings_course_ids.down.sql
--
-- Reverse of 0034: collapse the offering aggregate's CourseIDs[] back to a
-- single CourseID (the first/primary). Lossy for offerings that bundled >1
-- course (only the primary survives) — expected for a down-migration.
-- Idempotent: skips rows already single-course.
-- =============================================================================

UPDATE offerings
SET data = (data - 'CourseIDs')
           || CASE
                WHEN data ? 'CourseID' THEN '{}'::jsonb
                ELSE jsonb_build_object('CourseID', data -> 'CourseIDs' -> 0)
              END
WHERE data ? 'CourseIDs';
