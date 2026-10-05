-- 0028_test_sets_source_job_id.down.sql — Lane 1c W4 (CHO-1703 / ADR-180).
--
-- Dropping the column implicitly drops idx_test_sets_source_job_id; the
-- explicit DROP INDEX first keeps the down migration self-documenting and
-- idempotent under partial-failure re-runs.

BEGIN;

DROP INDEX IF EXISTS idx_test_sets_source_job_id;

ALTER TABLE test_sets
    DROP COLUMN IF EXISTS source_job_id;

COMMIT;
