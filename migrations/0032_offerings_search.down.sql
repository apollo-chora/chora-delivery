-- =============================================================================
-- chora-delivery : 0032_offerings_search.down.sql  (reverses 0032)
-- =============================================================================

DROP INDEX IF EXISTS idx_offerings_tenant_state;
DROP INDEX IF EXISTS idx_offerings_tenant_label_id;
DROP INDEX IF EXISTS idx_offerings_tenant_updated_id;
DROP INDEX IF EXISTS idx_offerings_tenant_created_id;
ALTER TABLE offerings DROP COLUMN IF EXISTS label;
