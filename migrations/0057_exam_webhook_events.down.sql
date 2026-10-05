-- =============================================================================
-- chora-delivery : 0057_exam_webhook_events.down.sql
--   (reverses 0057_exam_webhook_events.up.sql; ADR-193 Reversibility)
-- =============================================================================

DROP INDEX IF EXISTS idx_exam_webhook_events_unprocessed;
DROP TABLE IF EXISTS exam_webhook_events;
