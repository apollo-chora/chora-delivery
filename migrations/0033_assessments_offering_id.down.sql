-- =============================================================================
-- chora-delivery : 0033_assessments_offering_id.down.sql
--                  (reverses 0033_assessments_offering_id.up.sql)
-- =============================================================================

DROP INDEX IF EXISTS idx_assessments_tenant_offering;
ALTER TABLE assessments DROP COLUMN IF EXISTS offering_id;
