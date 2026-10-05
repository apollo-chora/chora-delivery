-- 004_create_placements_and_supervisors.up.sql
-- Placement: active work placement linking learner to internship.
-- PlacementSupervisor is stored inline (supervisor_gcid on placement).
-- Tenant-scoped with RLS. Soft-delete via deleted_at.

CREATE TABLE placements (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL,
    internship_id     UUID NOT NULL,          -- Cross-context ref (no FK)
    learner_gcid      UUID NOT NULL,          -- Cross-context ref to GCID
    supervisor_gcid   UUID,                   -- Cross-context ref to GCID (nullable)
    status            placement_status NOT NULL DEFAULT 'active',
    starts_at         TIMESTAMPTZ,
    ends_at           TIMESTAMPTZ,
    total_hours_logged NUMERIC(8,2) NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ             -- Soft delete
);

-- Indexes
CREATE INDEX idx_placements_tenant ON placements(tenant_id);
CREATE INDEX idx_placements_internship ON placements(tenant_id, internship_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_placements_learner ON placements(tenant_id, learner_gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_placements_status ON placements(tenant_id, status) WHERE deleted_at IS NULL;
CREATE INDEX idx_placements_supervisor ON placements(tenant_id, supervisor_gcid) WHERE deleted_at IS NULL AND supervisor_gcid IS NOT NULL;

-- Auto-update updated_at
CREATE TRIGGER placements_updated_at
    BEFORE UPDATE ON placements
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE placements ENABLE ROW LEVEL SECURITY;
ALTER TABLE placements FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON placements
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
