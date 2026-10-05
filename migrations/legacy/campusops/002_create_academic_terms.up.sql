-- 002_create_academic_terms.up.sql
-- AcademicTerm: semester/trimester/quarter/custom term periods.
-- Tenant-scoped with RLS. Soft-delete via deleted_at.

CREATE TABLE academic_terms (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    name        VARCHAR(255) NOT NULL,
    term_type   term_type NOT NULL,
    starts_at   TIMESTAMPTZ NOT NULL,
    ends_at     TIMESTAMPTZ NOT NULL,
    is_active   BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

-- Indexes
CREATE INDEX idx_academic_terms_tenant_id ON academic_terms(tenant_id);
CREATE INDEX idx_academic_terms_active ON academic_terms(tenant_id, is_active) WHERE deleted_at IS NULL;
CREATE INDEX idx_academic_terms_dates ON academic_terms(tenant_id, starts_at, ends_at) WHERE deleted_at IS NULL;

-- Auto-update updated_at
CREATE TRIGGER academic_terms_updated_at
    BEFORE UPDATE ON academic_terms
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE academic_terms ENABLE ROW LEVEL SECURITY;
ALTER TABLE academic_terms FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON academic_terms
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
