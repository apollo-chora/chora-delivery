-- 003_create_attendance.up.sql
-- Attendance: append-only records (never updated or deleted per DDD).
-- Records attendance for a single learner at a single session meeting.

CREATE TABLE attendance (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID NOT NULL,
    training_session_id  UUID NOT NULL REFERENCES training_sessions(id),
    learner_gcid         UUID NOT NULL,    -- Cross-context ref to GCID (no FK)
    status               attendance_status NOT NULL,
    check_in_method      check_in_method NOT NULL DEFAULT 'manual',
    session_date         DATE,
    marked_by_gcid       UUID,             -- Nullable: null for self check-in
    notes                VARCHAR(500),
    recorded_at          TIMESTAMPTZ NOT NULL DEFAULT now()
    -- No updated_at, no deleted_at: append-only (DDD rule #4 for AtomRevision pattern)
);

-- Indexes
CREATE INDEX idx_attendance_tenant_id ON attendance(tenant_id);
CREATE INDEX idx_attendance_session ON attendance(tenant_id, training_session_id);
CREATE INDEX idx_attendance_learner ON attendance(tenant_id, learner_gcid);
CREATE INDEX idx_attendance_session_date ON attendance(training_session_id, session_date);
CREATE UNIQUE INDEX idx_attendance_unique_per_meeting ON attendance(training_session_id, learner_gcid, session_date);

-- Row-Level Security
ALTER TABLE attendance ENABLE ROW LEVEL SECURITY;
ALTER TABLE attendance FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON attendance
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
