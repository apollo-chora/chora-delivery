-- 002_create_internships.up.sql
-- Internship: posting for work-based learning opportunities.
-- Tenant-scoped with RLS. Soft-delete via deleted_at.

CREATE TABLE internships (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL,
    title            VARCHAR(255) NOT NULL,
    description      TEXT,
    status           internship_status NOT NULL DEFAULT 'draft',
    partner_id       UUID NOT NULL,           -- Cross-context ref to IndustryPartner
    location         VARCHAR(500),
    max_positions    INTEGER NOT NULL DEFAULT 1,
    filled_positions INTEGER NOT NULL DEFAULT 0,
    starts_at        TIMESTAMPTZ,
    ends_at          TIMESTAMPTZ,
    required_skills  TEXT[] DEFAULT '{}',
    created_by_gcid  UUID NOT NULL,           -- Cross-context ref to GCID (no FK)
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ              -- Soft delete (DDD rule #5)
);

-- Indexes
CREATE INDEX idx_internships_tenant_id ON internships(tenant_id);
CREATE INDEX idx_internships_status ON internships(tenant_id, status) WHERE deleted_at IS NULL;
CREATE INDEX idx_internships_partner ON internships(tenant_id, partner_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_internships_starts_at ON internships(tenant_id, starts_at) WHERE deleted_at IS NULL;

-- Auto-update updated_at
CREATE TRIGGER internships_updated_at
    BEFORE UPDATE ON internships
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE internships ENABLE ROW LEVEL SECURITY;
ALTER TABLE internships FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON internships
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
