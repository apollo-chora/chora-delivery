-- 005_create_training_applications.up.sql
-- TrainingApplication: learner application workflow for training sessions.
-- Status workflow: draft → submitted → under_review → approved/rejected/waitlisted.

CREATE TABLE training_applications (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID NOT NULL,
    gcid                 UUID NOT NULL,    -- Cross-context ref to applicant GCID (no FK)
    training_session_id  UUID NOT NULL REFERENCES training_sessions(id),
    status               application_status NOT NULL DEFAULT 'draft',
    application_text     TEXT,
    submitted_at         TIMESTAMPTZ,
    reviewed_by_gcid     UUID,             -- Cross-context ref to reviewer GCID (no FK)
    reviewed_at          TIMESTAMPTZ,
    rejection_reason     VARCHAR(1000),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_training_applications_tenant_id ON training_applications(tenant_id);
CREATE INDEX idx_training_applications_session ON training_applications(tenant_id, training_session_id);
CREATE INDEX idx_training_applications_gcid ON training_applications(tenant_id, gcid);
CREATE INDEX idx_training_applications_status ON training_applications(tenant_id, status);

-- One application per learner per session
CREATE UNIQUE INDEX idx_training_applications_unique ON training_applications(gcid, training_session_id);

-- Auto-update updated_at
CREATE TRIGGER training_applications_updated_at
    BEFORE UPDATE ON training_applications
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE training_applications ENABLE ROW LEVEL SECURITY;
ALTER TABLE training_applications FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON training_applications
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
