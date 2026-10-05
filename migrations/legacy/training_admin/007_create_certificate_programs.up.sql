-- 007_create_certificate_programs.up.sql
-- CertificateProgram: bundled learning paths leading to certification.
-- ProgramEnrollment: learner progress through a certificate program.

CREATE TABLE certificate_programs (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                UUID NOT NULL,
    title                    VARCHAR(255) NOT NULL,
    description              TEXT,
    status                   program_status NOT NULL DEFAULT 'draft',
    path_ids                 UUID[] NOT NULL,  -- Cross-context refs to LockedPaths (no FK)
    total_paths              INTEGER NOT NULL,
    estimated_duration_hours REAL,
    certificate_template     VARCHAR(255),
    created_by_gcid          UUID NOT NULL,    -- Cross-context ref to GCID (no FK)
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at               TIMESTAMPTZ       -- Soft delete
);

-- Indexes
CREATE INDEX idx_certificate_programs_tenant_id ON certificate_programs(tenant_id);
CREATE INDEX idx_certificate_programs_status ON certificate_programs(tenant_id, status) WHERE deleted_at IS NULL;

-- Auto-update updated_at
CREATE TRIGGER certificate_programs_updated_at
    BEFORE UPDATE ON certificate_programs
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE certificate_programs ENABLE ROW LEVEL SECURITY;
ALTER TABLE certificate_programs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON certificate_programs
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ─────────────────────────────────────────────────────────────

CREATE TABLE program_enrollments (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID NOT NULL,
    gcid                 UUID NOT NULL,    -- Cross-context ref to learner GCID (no FK)
    program_id           UUID NOT NULL REFERENCES certificate_programs(id),
    status               enrollment_status NOT NULL DEFAULT 'enrolled',
    completed_path_ids   UUID[] DEFAULT '{}',
    total_paths          INTEGER NOT NULL,
    progress_pct         REAL NOT NULL DEFAULT 0.0,
    enrolled_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at         TIMESTAMPTZ,
    certificate_issued_at TIMESTAMPTZ,
    certificate_id       VARCHAR(100),     -- Formatted: CHORA-{tenant}-{YYYY}-{seq}
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_program_enrollments_tenant_id ON program_enrollments(tenant_id);
CREATE INDEX idx_program_enrollments_program ON program_enrollments(tenant_id, program_id);
CREATE INDEX idx_program_enrollments_gcid ON program_enrollments(tenant_id, gcid);
CREATE INDEX idx_program_enrollments_status ON program_enrollments(tenant_id, status);

-- One enrollment per learner per program
CREATE UNIQUE INDEX idx_program_enrollments_unique ON program_enrollments(gcid, program_id);

-- Auto-update updated_at
CREATE TRIGGER program_enrollments_updated_at
    BEFORE UPDATE ON program_enrollments
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE program_enrollments ENABLE ROW LEVEL SECURITY;
ALTER TABLE program_enrollments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON program_enrollments
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
