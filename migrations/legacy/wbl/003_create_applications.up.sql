-- 003_create_applications.up.sql
-- InternshipApplication: learner's application for an internship.
-- Tenant-scoped with RLS.

CREATE TABLE internship_applications (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL,
    internship_id    UUID NOT NULL,            -- Cross-context ref (no FK, validated in domain)
    applicant_gcid   UUID NOT NULL,            -- Cross-context ref to GCID
    status           wbl_application_status NOT NULL DEFAULT 'submitted',
    cover_letter     TEXT,
    reviewed_by_gcid UUID,
    rejection_reason TEXT,
    submitted_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_applications_tenant ON internship_applications(tenant_id);
CREATE INDEX idx_applications_internship ON internship_applications(tenant_id, internship_id);
CREATE INDEX idx_applications_applicant ON internship_applications(tenant_id, internship_id, applicant_gcid);
CREATE INDEX idx_applications_status ON internship_applications(tenant_id, status);

-- Auto-update updated_at
CREATE TRIGGER applications_updated_at
    BEFORE UPDATE ON internship_applications
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE internship_applications ENABLE ROW LEVEL SECURITY;
ALTER TABLE internship_applications FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON internship_applications
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
