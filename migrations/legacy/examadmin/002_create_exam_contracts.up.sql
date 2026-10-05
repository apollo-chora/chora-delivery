-- 002_create_exam_contracts.up.sql
-- ExamContract: aggregate root for exam board agreements.
-- Tenant-scoped with RLS. Soft-delete via deleted_at.

CREATE TABLE exam_contracts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL,
    exam_board_name     VARCHAR(255) NOT NULL,
    contract_reference  VARCHAR(255) NOT NULL,
    status              exam_contract_status NOT NULL DEFAULT 'draft',
    valid_from          TIMESTAMPTZ NOT NULL,
    valid_until         TIMESTAMPTZ NOT NULL,
    terms               JSONB DEFAULT '{}',
    created_by_gcid     UUID NOT NULL,       -- Cross-context ref to GCID (no FK)
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ          -- Soft delete (DDD rule #5)
);

-- Indexes
CREATE INDEX idx_exam_contracts_tenant_id ON exam_contracts(tenant_id);
CREATE INDEX idx_exam_contracts_status ON exam_contracts(tenant_id, status) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_exam_contracts_reference ON exam_contracts(tenant_id, contract_reference) WHERE deleted_at IS NULL;

-- Auto-update updated_at
CREATE TRIGGER exam_contracts_updated_at
    BEFORE UPDATE ON exam_contracts
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE exam_contracts ENABLE ROW LEVEL SECURITY;
ALTER TABLE exam_contracts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_contracts
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
