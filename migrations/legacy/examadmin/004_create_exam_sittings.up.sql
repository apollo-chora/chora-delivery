-- 004_create_exam_sittings.up.sql
-- ExamSitting: a specific exam event at a venue on a date.
-- Tenant-scoped with RLS. Soft-delete via deleted_at.

CREATE TABLE exam_sittings (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL,
    contract_id       UUID NOT NULL,          -- Cross-context ref to ExamContract (no FK for cross-aggregate)
    venue_id          UUID NOT NULL,          -- Cross-context ref to ExamVenue
    room_id           UUID,                   -- Optional ref to ExamRoom
    exam_code         VARCHAR(100) NOT NULL,
    title             VARCHAR(255) NOT NULL,
    status            exam_sitting_status NOT NULL DEFAULT 'scheduled',
    scheduled_start   TIMESTAMPTZ NOT NULL,
    scheduled_end     TIMESTAMPTZ NOT NULL,
    max_candidates    INTEGER NOT NULL CHECK (max_candidates > 0),
    registered_count  INTEGER NOT NULL DEFAULT 0,
    created_by_gcid   UUID NOT NULL,          -- Cross-context ref to GCID (no FK)
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ
);

-- Indexes
CREATE INDEX idx_exam_sittings_tenant ON exam_sittings(tenant_id);
CREATE INDEX idx_exam_sittings_contract ON exam_sittings(contract_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_exam_sittings_venue ON exam_sittings(venue_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_exam_sittings_status ON exam_sittings(tenant_id, status) WHERE deleted_at IS NULL;
CREATE INDEX idx_exam_sittings_schedule ON exam_sittings(tenant_id, scheduled_start) WHERE deleted_at IS NULL;

CREATE TRIGGER exam_sittings_updated_at
    BEFORE UPDATE ON exam_sittings
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE exam_sittings ENABLE ROW LEVEL SECURITY;
ALTER TABLE exam_sittings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_sittings
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
