-- 008_processed_events.up.sql
-- Idempotency table for consumed events (deduplication).

CREATE TABLE processed_events (
    event_id     UUID PRIMARY KEY,
    event_type   VARCHAR(255) NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_processed_events_type ON processed_events(event_type);
