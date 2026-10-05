-- 005_create_candidates.up.sql
-- CandidateRegistration: a candidate's registration for an exam sitting.
-- Tenant-scoped with RLS. Append-only (no updated_at, no soft delete).

CREATE TABLE candidate_registrations (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL,
    sitting_id          UUID NOT NULL,          -- Cross-context ref to ExamSitting
    candidate_gcid      UUID NOT NULL,          -- Cross-context ref to GCID (no FK)
    status              exam_registration_status NOT NULL DEFAULT 'registered',
    accommodation_notes TEXT,
    registered_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_candidate_reg_sitting ON candidate_registrations(sitting_id);
CREATE INDEX idx_candidate_reg_tenant ON candidate_registrations(tenant_id);
CREATE INDEX idx_candidate_reg_candidate ON candidate_registrations(candidate_gcid);
CREATE UNIQUE INDEX idx_candidate_reg_unique ON candidate_registrations(sitting_id, candidate_gcid, tenant_id);

ALTER TABLE candidate_registrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE candidate_registrations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON candidate_registrations
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
