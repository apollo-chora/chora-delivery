-- =============================================================================
-- chora-delivery : 0047_exam_candidates.down.sql  (reverses 0047_exam_candidates.up.sql)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON exam_candidates;
DROP INDEX IF EXISTS idx_exam_candidates_exam;
DROP INDEX IF EXISTS uq_exam_candidates_exam_gcid;
DROP TABLE IF EXISTS exam_candidates;
