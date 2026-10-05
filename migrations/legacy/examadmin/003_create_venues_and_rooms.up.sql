-- 003_create_venues_and_rooms.up.sql
-- ExamVenue and ExamRoom: physical locations for exams.
-- Tenant-scoped with RLS. Soft-delete via deleted_at.

CREATE TABLE exam_venues (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    name        VARCHAR(255) NOT NULL,
    address     TEXT NOT NULL,
    capacity    INTEGER NOT NULL CHECK (capacity > 0),
    is_active   BOOLEAN NOT NULL DEFAULT true,
    metadata    JSONB DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX idx_exam_venues_tenant_id ON exam_venues(tenant_id);
CREATE INDEX idx_exam_venues_active ON exam_venues(tenant_id, is_active) WHERE deleted_at IS NULL;

CREATE TRIGGER exam_venues_updated_at
    BEFORE UPDATE ON exam_venues
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE exam_venues ENABLE ROW LEVEL SECURITY;
ALTER TABLE exam_venues FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_venues
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ExamRoom: child entity of ExamVenue.
CREATE TABLE exam_rooms (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    venue_id    UUID NOT NULL REFERENCES exam_venues(id),
    tenant_id   UUID NOT NULL,
    name        VARCHAR(255) NOT NULL,
    capacity    INTEGER NOT NULL CHECK (capacity > 0),
    is_active   BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX idx_exam_rooms_venue ON exam_rooms(venue_id);
CREATE INDEX idx_exam_rooms_tenant ON exam_rooms(tenant_id);

CREATE TRIGGER exam_rooms_updated_at
    BEFORE UPDATE ON exam_rooms
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE exam_rooms ENABLE ROW LEVEL SECURITY;
ALTER TABLE exam_rooms FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON exam_rooms
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
