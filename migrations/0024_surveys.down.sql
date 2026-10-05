-- =============================================================================
-- chora-delivery : 0024_surveys.down.sql  (reverses 0024_surveys.up.sql)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON survey_responses;
DROP INDEX IF EXISTS idx_survey_responses_tenant;
DROP INDEX IF EXISTS idx_survey_responses_survey;
DROP TABLE IF EXISTS survey_responses;

DROP POLICY IF EXISTS tenant_isolation ON surveys;
DROP INDEX IF EXISTS idx_surveys_tenant;
DROP TABLE IF EXISTS surveys;
