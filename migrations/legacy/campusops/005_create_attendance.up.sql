-- 005_create_attendance.up.sql
-- AttendanceRecord: append-only attendance records for class sections.
-- Tenant-scoped with RLS.

CREATE TABLE attendance_records (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    section_id      UUID NOT NULL REFERENCES class_sections(id),
    learner_gcid    UUID NOT NULL,         -- Cross-context ref to GCID (no FK)
    status          campus_attendance_status NOT NULL,
    check_in_method campus_check_in_method NOT NULL DEFAULT 'manual',
    session_date    DATE NOT NULL,
    marked_by_gcid  UUID NOT NULL,         -- Cross-context ref to GCID (no FK)
    notes           TEXT,
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_attendance_records_tenant ON attendance_records(tenant_id);
CREATE INDEX idx_attendance_records_section ON attendance_records(section_id, session_date);
CREATE INDEX idx_attendance_records_learner ON attendance_records(section_id, learner_gcid);

ALTER TABLE attendance_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE attendance_records FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON attendance_records
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
