-- =============================================================================
-- chora-delivery : 0018_live_poll.down.sql  (reverse of the .up)
-- =============================================================================

DROP INDEX IF EXISTS idx_live_polls_tenant;
DROP TABLE IF EXISTS live_polls;
