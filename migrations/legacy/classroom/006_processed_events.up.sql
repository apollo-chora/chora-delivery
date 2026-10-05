-- 006_processed_events.up.sql
-- Idempotency guard for event consumers.
-- Prevents duplicate processing of domain events (Pub/Sub at-least-once delivery).
CREATE TABLE IF NOT EXISTS processed_events (
    event_id       UUID         NOT NULL,
    handler_name   VARCHAR(100) NOT NULL,
    processed_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    result_summary JSONB,
    PRIMARY KEY (event_id, handler_name)
);

CREATE INDEX idx_processed_events_handler ON processed_events (handler_name, processed_at);

COMMENT ON TABLE processed_events IS 'Idempotency guard — tracks which events have been processed by which handler';
