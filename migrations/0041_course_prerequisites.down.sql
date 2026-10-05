-- 0041_course_prerequisites.down.sql — drop the Course Prerequisite DAG table
-- (ADR-226). DROP TABLE removes its indexes + RLS policy with it.
DROP TABLE IF EXISTS course_prerequisites;
