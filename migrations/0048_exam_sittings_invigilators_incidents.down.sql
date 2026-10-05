-- =============================================================================
-- chora-delivery : 0048_exam_sittings_invigilators_incidents.down.sql
--   (reverses 0048_exam_sittings_invigilators_incidents.up.sql)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON incident_reports;
DROP INDEX IF EXISTS idx_incident_reports_sitting;
DROP TABLE IF EXISTS incident_reports;

DROP POLICY IF EXISTS tenant_isolation ON exam_invigilators;
DROP INDEX IF EXISTS uq_exam_invigilators_sitting_gcid;
DROP INDEX IF EXISTS uq_exam_invigilators_one_chief;
DROP INDEX IF EXISTS idx_exam_invigilators_sitting;
DROP TABLE IF EXISTS exam_invigilators;

DROP POLICY IF EXISTS tenant_isolation ON exam_sittings;
DROP INDEX IF EXISTS idx_exam_sittings_exam;
DROP TABLE IF EXISTS exam_sittings;
