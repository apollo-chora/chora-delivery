-- =============================================================================
-- chora-delivery : 0019_bookings.down.sql  (reverses 0019_bookings.up.sql)
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON bookings;
DROP INDEX IF EXISTS idx_bookings_class;
DROP INDEX IF EXISTS idx_bookings_tenant;
DROP TABLE IF EXISTS bookings;
