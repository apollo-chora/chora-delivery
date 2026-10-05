-- 004_create_sections_and_timeslots.up.sql
-- ClassSection: links training sessions to campus scheduling.
-- SectionTimeSlot: recurring schedule entries for class sections.
-- Tenant-scoped with RLS. Soft-delete via deleted_at.

CREATE TABLE class_sections (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID NOT NULL,
    section_code          VARCHAR(50) NOT NULL,
    training_session_id   UUID NOT NULL,        -- Cross-context ref to TrainingSession (no FK)
    academic_term_id      UUID NOT NULL REFERENCES academic_terms(id),
    room_id               UUID REFERENCES rooms(id),
    instructor_gcid       UUID NOT NULL,        -- Cross-context ref to GCID (no FK)
    max_capacity          INTEGER NOT NULL DEFAULT 0,
    enrolled_count        INTEGER NOT NULL DEFAULT 0,
    status                section_status NOT NULL DEFAULT 'active',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at            TIMESTAMPTZ
);

CREATE INDEX idx_class_sections_tenant_id ON class_sections(tenant_id);
CREATE INDEX idx_class_sections_term ON class_sections(academic_term_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_class_sections_training ON class_sections(training_session_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_class_sections_instructor ON class_sections(tenant_id, instructor_gcid) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_class_sections_code_term ON class_sections(tenant_id, section_code, academic_term_id) WHERE deleted_at IS NULL;

CREATE TRIGGER class_sections_updated_at
    BEFORE UPDATE ON class_sections
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE class_sections ENABLE ROW LEVEL SECURITY;
ALTER TABLE class_sections FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON class_sections
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- Section time slots
CREATE TABLE section_time_slots (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    section_id      UUID NOT NULL REFERENCES class_sections(id) ON DELETE CASCADE,
    day_of_week     campus_day_of_week NOT NULL,
    start_time      TIME NOT NULL,
    end_time        TIME NOT NULL,
    room_id         UUID REFERENCES rooms(id),
    effective_from  DATE NOT NULL,
    effective_until DATE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_section_time_slots_section ON section_time_slots(section_id);
CREATE INDEX idx_section_time_slots_day ON section_time_slots(section_id, day_of_week);
