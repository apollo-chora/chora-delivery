-- 006_create_proctor_sessions.up.sql
-- ProctorSession: proctoring session for an exam sitting.
-- Also includes ProctorAssignment and ExamInvigilator.
-- Tenant-scoped with RLS.

CREATE TABLE proctor_sessions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    sitting_id   UUID NOT NULL,               -- Cross-context ref to ExamSitting
    proctor_gcid UUID NOT NULL,               -- Cross-context ref to GCID (no FK)
    status       exam_proctor_session_status NOT NULL DEFAULT 'active',
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at     TIMESTAMPTZ,
    notes        TEXT
);

CREATE INDEX idx_proctor_sessions_sitting ON proctor_sessions(sitting_id);
CREATE INDEX idx_proctor_sessions_tenant ON proctor_sessions(tenant_id);
CREATE INDEX idx_proctor_sessions_active ON proctor_sessions(sitting_id, tenant_id) WHERE status = 'active';

ALTER TABLE proctor_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE proctor_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON proctor_sessions
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ProctorAssignment: proctor assigned to a sitting.
CREATE TABLE proctor_assignments (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    sitting_id   UUID NOT NULL,
    proctor_gcid UUID NOT NULL,
    assigned_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_proctor_assign_sitting ON proctor_assignments(sitting_id);
CREATE INDEX idx_proctor_assign_tenant ON proctor_assignments(tenant_id);

ALTER TABLE proctor_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE proctor_assignments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON proctor_assignments
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ExamInvigilator: invigilator assigned to a sitting.
CREATE TABLE exam_invigilators (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL,
    sitting_id        UUID NOT NULL,
    invigilator_gcid  UUID NOT NULL,
    role              VARCHAR(100) NOT NULL DEFAULT 'invigilator',
    assigned_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_exam_invig_sitting ON exam_invigilators(sitting_id);
CREATE INDEX idx_exam_invig_tenant ON exam_invigilators(tenant_id);

ALTER TABLE exam_invigilators ENABLE ROW LEVEL SECURITY;
ALTER TABLE exam_invigilators FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_invigilators
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
