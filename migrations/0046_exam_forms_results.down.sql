-- =============================================================================
-- chora-delivery : 0046_exam_forms_results.down.sql
--   (reverses 0046_exam_forms_results.up.sql)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON exam_results;
DROP INDEX IF EXISTS idx_exam_results_tenant_candidate;
DROP INDEX IF EXISTS idx_exam_results_tenant_form;
DROP TABLE IF EXISTS exam_results;

DROP POLICY IF EXISTS tenant_isolation ON exam_forms;
DROP INDEX IF EXISTS idx_exam_forms_tenant_exam;
DROP TABLE IF EXISTS exam_forms;
