-- 006_create_bookings.up.sql
-- FacilityBooking: room/venue reservation with overlap prevention.
-- TermCalendarEvent: notable events within academic terms.
-- Tenant-scoped with RLS. Soft-delete via deleted_at.

CREATE TABLE facility_bookings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    room_id         UUID NOT NULL REFERENCES rooms(id),
    booked_by_gcid  UUID NOT NULL,          -- Cross-context ref to GCID (no FK)
    title           VARCHAR(255) NOT NULL,
    starts_at       TIMESTAMPTZ NOT NULL,
    ends_at         TIMESTAMPTZ NOT NULL,
    status          booking_status NOT NULL DEFAULT 'pending',
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_facility_bookings_tenant ON facility_bookings(tenant_id);
CREATE INDEX idx_facility_bookings_room ON facility_bookings(room_id, starts_at, ends_at) WHERE deleted_at IS NULL AND status != 'cancelled';
CREATE INDEX idx_facility_bookings_dates ON facility_bookings(tenant_id, starts_at) WHERE deleted_at IS NULL;

CREATE TRIGGER facility_bookings_updated_at
    BEFORE UPDATE ON facility_bookings
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE facility_bookings ENABLE ROW LEVEL SECURITY;
ALTER TABLE facility_bookings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON facility_bookings
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- Exclusion constraint to prevent overlapping confirmed bookings
-- (PostgreSQL range overlap check)
CREATE EXTENSION IF NOT EXISTS btree_gist;
ALTER TABLE facility_bookings
    ADD CONSTRAINT no_overlap_bookings
    EXCLUDE USING gist (
        room_id WITH =,
        tstzrange(starts_at, ends_at) WITH &&
    ) WHERE (status != 'cancelled' AND deleted_at IS NULL);

-- Term calendar events
CREATE TABLE term_calendar_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    term_id     UUID NOT NULL REFERENCES academic_terms(id),
    title       VARCHAR(255) NOT NULL,
    event_type  VARCHAR(50) NOT NULL,       -- holiday, exam_period, registration, etc.
    starts_at   TIMESTAMPTZ NOT NULL,
    ends_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_term_calendar_events_term ON term_calendar_events(term_id);
CREATE INDEX idx_term_calendar_events_tenant ON term_calendar_events(tenant_id);

ALTER TABLE term_calendar_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE term_calendar_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON term_calendar_events
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
