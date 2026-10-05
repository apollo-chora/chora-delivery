-- 004_create_schedules.up.sql
-- Schedule: time slot entries for training sessions.

CREATE TABLE schedules (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    training_session_id  UUID NOT NULL REFERENCES training_sessions(id),
    day_of_week          day_of_week NOT NULL,
    start_time           TIME NOT NULL,
    end_time             TIME NOT NULL,
    recurrence           recurrence_type NOT NULL DEFAULT 'one_off',
    effective_from       DATE NOT NULL,
    effective_until       DATE,
    location             VARCHAR(500),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT check_time_order CHECK (end_time > start_time),
    CONSTRAINT check_effective_range CHECK (effective_until IS NULL OR effective_until >= effective_from)
);

-- Indexes
CREATE INDEX idx_schedules_session ON schedules(training_session_id);
CREATE INDEX idx_schedules_day ON schedules(training_session_id, day_of_week);

-- No RLS needed: schedules are accessed through their parent session (tenant isolation via session FK)
-- But for defense-in-depth, we can't add tenant_id since Schedule doesn't have one.
-- Access is controlled by JOIN to training_sessions which has RLS.
