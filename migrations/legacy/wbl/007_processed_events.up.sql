-- 007_processed_events.up.sql
-- Idempotency store for event processing.

CREATE TABLE processed_events (
    event_id     UUID PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- TTL index for cleanup (events older than 7 days can be purged)
CREATE INDEX idx_processed_events_processed_at ON processed_events(processed_at);
