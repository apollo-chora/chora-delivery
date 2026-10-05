-- 007_create_incidents_results_appeals.up.sql
-- ExamIncident, ExamResult, ExamAppeal, ExamFee, ExamAccommodation.
-- Tenant-scoped with RLS.

-- ExamIncident: incident during an exam sitting (append-only).
CREATE TABLE exam_incidents (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL,
    sitting_id       UUID NOT NULL,
    reported_by_gcid UUID NOT NULL,           -- Cross-context ref to GCID (no FK)
    incident_type    exam_incident_type NOT NULL,
    severity         exam_incident_severity NOT NULL,
    description      TEXT NOT NULL,
    candidate_gcid   UUID,                    -- Optional: specific candidate involved
    reported_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_exam_incidents_sitting ON exam_incidents(sitting_id);
CREATE INDEX idx_exam_incidents_tenant ON exam_incidents(tenant_id);
CREATE INDEX idx_exam_incidents_severity ON exam_incidents(tenant_id, severity);

ALTER TABLE exam_incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE exam_incidents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_incidents
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ExamResult: candidate exam result (append-only).
CREATE TABLE exam_results (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    sitting_id      UUID NOT NULL,
    candidate_gcid  UUID NOT NULL,            -- Cross-context ref to GCID (no FK)
    score           DOUBLE PRECISION NOT NULL,
    max_score       DOUBLE PRECISION NOT NULL CHECK (max_score > 0),
    grade           VARCHAR(20),
    passed          BOOLEAN NOT NULL,
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_exam_results_sitting ON exam_results(sitting_id);
CREATE INDEX idx_exam_results_tenant ON exam_results(tenant_id);
CREATE INDEX idx_exam_results_candidate ON exam_results(candidate_gcid, tenant_id);

ALTER TABLE exam_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE exam_results FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_results
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ExamAppeal: appeal against an exam result.
CREATE TABLE exam_appeals (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    result_id       UUID NOT NULL,            -- Cross-context ref to ExamResult
    candidate_gcid  UUID NOT NULL,            -- Cross-context ref to GCID (no FK)
    reason          TEXT NOT NULL,
    status          exam_appeal_status NOT NULL DEFAULT 'submitted',
    submitted_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at     TIMESTAMPTZ,
    review_notes    TEXT
);

CREATE INDEX idx_exam_appeals_result ON exam_appeals(result_id);
CREATE INDEX idx_exam_appeals_tenant ON exam_appeals(tenant_id);
CREATE INDEX idx_exam_appeals_status ON exam_appeals(tenant_id, status);

ALTER TABLE exam_appeals ENABLE ROW LEVEL SECURITY;
ALTER TABLE exam_appeals FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_appeals
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ExamFee: fee associated with a sitting.
CREATE TABLE exam_fees (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    sitting_id  UUID NOT NULL,
    fee_type    VARCHAR(100) NOT NULL,
    amount      DOUBLE PRECISION NOT NULL CHECK (amount >= 0),
    currency    VARCHAR(3) NOT NULL DEFAULT 'USD',
    description TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_exam_fees_sitting ON exam_fees(sitting_id);
CREATE INDEX idx_exam_fees_tenant ON exam_fees(tenant_id);

ALTER TABLE exam_fees ENABLE ROW LEVEL SECURITY;
ALTER TABLE exam_fees FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_fees
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ExamAccommodation: special accommodations for candidates.
CREATE TABLE exam_accommodations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    sitting_id      UUID NOT NULL,
    candidate_gcid  UUID NOT NULL,
    type            VARCHAR(100) NOT NULL,
    details         TEXT NOT NULL,
    approved_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_exam_accom_sitting ON exam_accommodations(sitting_id);
CREATE INDEX idx_exam_accom_tenant ON exam_accommodations(tenant_id);
CREATE INDEX idx_exam_accom_candidate ON exam_accommodations(candidate_gcid);

ALTER TABLE exam_accommodations ENABLE ROW LEVEL SECURITY;
ALTER TABLE exam_accommodations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_accommodations
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
