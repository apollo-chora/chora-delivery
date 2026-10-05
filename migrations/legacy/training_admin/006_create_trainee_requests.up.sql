-- 006_create_trainee_requests.up.sql
-- TraineeRequest: deferral, withdrawal, makeup, and transfer requests.

CREATE TABLE trainee_requests (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID NOT NULL,
    gcid                 UUID NOT NULL,    -- Cross-context ref to requestor GCID (no FK)
    training_session_id  UUID NOT NULL REFERENCES training_sessions(id),
    request_type         trainee_request_type NOT NULL,
    status               request_status NOT NULL DEFAULT 'submitted',
    reason               TEXT NOT NULL,
    supporting_documents TEXT[] DEFAULT '{}',  -- GCS signed URLs
    target_session_id    UUID,             -- For transfer requests: target session
    reviewed_by_gcid     UUID,             -- Cross-context ref to reviewer GCID (no FK)
    reviewed_at          TIMESTAMPTZ,
    resolution_notes     VARCHAR(1000),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_trainee_requests_tenant_id ON trainee_requests(tenant_id);
CREATE INDEX idx_trainee_requests_session ON trainee_requests(tenant_id, training_session_id);
CREATE INDEX idx_trainee_requests_gcid ON trainee_requests(tenant_id, gcid);
CREATE INDEX idx_trainee_requests_type ON trainee_requests(tenant_id, request_type);
CREATE INDEX idx_trainee_requests_status ON trainee_requests(tenant_id, status);

-- Auto-update updated_at
CREATE TRIGGER trainee_requests_updated_at
    BEFORE UPDATE ON trainee_requests
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE trainee_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE trainee_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON trainee_requests
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
