-- 003_create_venues_and_rooms.up.sql
-- Venue: physical campus location containing rooms.
-- Room: room within a venue with capacity and equipment.
-- Both tenant-scoped with RLS. Soft-delete via deleted_at.

CREATE TABLE venues (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    name        VARCHAR(255) NOT NULL,
    address     TEXT,
    campus      VARCHAR(255),
    timezone    VARCHAR(50),
    is_active   BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX idx_venues_tenant_id ON venues(tenant_id);
CREATE INDEX idx_venues_active ON venues(tenant_id, is_active) WHERE deleted_at IS NULL;

CREATE TRIGGER venues_updated_at
    BEFORE UPDATE ON venues
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE venues ENABLE ROW LEVEL SECURITY;
ALTER TABLE venues FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON venues
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- Rooms
CREATE TABLE rooms (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    venue_id    UUID NOT NULL REFERENCES venues(id) ON DELETE CASCADE,
    name        VARCHAR(255) NOT NULL,
    room_code   VARCHAR(50) NOT NULL,
    capacity    INTEGER NOT NULL DEFAULT 0,
    room_type   room_type NOT NULL DEFAULT 'classroom',
    is_active   BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX idx_rooms_tenant_id ON rooms(tenant_id);
CREATE INDEX idx_rooms_venue_id ON rooms(venue_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_rooms_code_venue ON rooms(venue_id, room_code) WHERE deleted_at IS NULL;

CREATE TRIGGER rooms_updated_at
    BEFORE UPDATE ON rooms
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE rooms ENABLE ROW LEVEL SECURITY;
ALTER TABLE rooms FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON rooms
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- Room equipment
CREATE TABLE room_equipment (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id     UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    name        VARCHAR(255) NOT NULL,
    quantity    INTEGER NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_room_equipment_room_id ON room_equipment(room_id);
