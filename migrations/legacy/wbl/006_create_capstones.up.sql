-- 006_create_capstones.up.sql
-- CapstoneProject and CapstoneSubmission tables.
-- Tenant-scoped with RLS. Soft-delete on projects via deleted_at.

CREATE TABLE capstone_projects (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    title           VARCHAR(255) NOT NULL,
    description     TEXT,
    status          capstone_status NOT NULL DEFAULT 'draft',
    placement_id    UUID,                    -- Optional link to a placement (no FK)
    required_skills TEXT[] DEFAULT '{}',
    created_by_gcid UUID NOT NULL,           -- Cross-context ref to GCID
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ              -- Soft delete
);

-- Indexes
CREATE INDEX idx_capstones_tenant ON capstone_projects(tenant_id);
CREATE INDEX idx_capstones_status ON capstone_projects(tenant_id, status) WHERE deleted_at IS NULL;
CREATE INDEX idx_capstones_placement ON capstone_projects(placement_id) WHERE deleted_at IS NULL AND placement_id IS NOT NULL;

-- Auto-update updated_at
CREATE TRIGGER capstones_updated_at
    BEFORE UPDATE ON capstone_projects
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE capstone_projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE capstone_projects FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON capstone_projects
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- Capstone submissions (append-only).
CREATE TABLE capstone_submissions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    capstone_id     UUID NOT NULL,           -- Cross-context ref (no FK)
    submitter_gcid  UUID NOT NULL,           -- Cross-context ref to GCID
    submission_url  TEXT NOT NULL,
    notes           TEXT,
    submitted_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_submissions_tenant ON capstone_submissions(tenant_id);
CREATE INDEX idx_submissions_capstone ON capstone_submissions(tenant_id, capstone_id);

-- Row-Level Security
ALTER TABLE capstone_submissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE capstone_submissions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON capstone_submissions
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- IndustryPartner table.
CREATE TABLE industry_partners (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL,
    name           VARCHAR(255) NOT NULL,
    description    TEXT,
    contact_email  VARCHAR(320),
    website        VARCHAR(500),
    status         partner_status NOT NULL DEFAULT 'active',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ              -- Soft delete
);

-- Indexes
CREATE INDEX idx_partners_tenant ON industry_partners(tenant_id);
CREATE INDEX idx_partners_status ON industry_partners(tenant_id, status) WHERE deleted_at IS NULL;

-- Auto-update updated_at
CREATE TRIGGER partners_updated_at
    BEFORE UPDATE ON industry_partners
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE industry_partners ENABLE ROW LEVEL SECURITY;
ALTER TABLE industry_partners FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON industry_partners
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
