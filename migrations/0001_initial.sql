-- =============================================================================
-- chora-delivery : 0001_initial.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : agent-a5e52e89b73ede1d2 (db-migrations-11-services)
-- Date          : 2026-05-08
-- Architecture  : Architecture Review locked 2026-05-07 (Tier 1 D1) — R+ Rhythm+
--
-- Aggregates owned by this database:
--   - Course (instructor-owned, atom_ids[] cross-DB ref to chora_creation)
--   - Course enrollments (learner role assignment per course)
--   - Class (single delivery instance with capacity invariant)
--   - Class rostering (booking → time-slot)
--   - Attendance records (QR + manual; idempotent on (tenant, class, gcid))
--   - Certifications (append-only credentials with SHA-256 integrity hash)
-- =============================================================================

BEGIN;

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE OR REPLACE FUNCTION delivery_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- -----------------------------------------------------------------------------
-- ENUMs
-- -----------------------------------------------------------------------------
CREATE TYPE booking_status         AS ENUM ('pending', 'confirmed', 'attended', 'no-show');
CREATE TYPE attendance_status      AS ENUM ('present', 'absent', 'late', 'excused');
CREATE TYPE attendance_source      AS ENUM ('qr-scan', 'manual');
CREATE TYPE course_enrollment_role AS ENUM ('learner', 'auditor', 'co_instructor');

-- -----------------------------------------------------------------------------
-- courses — primary delivery aggregate
-- -----------------------------------------------------------------------------
CREATE TABLE courses (
    course_id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID         NOT NULL,
    instructor_gcid   UUID         NOT NULL,
    title             VARCHAR(256) NOT NULL,
    description       TEXT         NOT NULL DEFAULT '',
    atom_ids          UUID[]       NOT NULL DEFAULT '{}',  -- cross-DB ref
    public            BOOLEAN      NOT NULL DEFAULT FALSE,
    price_sgd_cents   BIGINT       NOT NULL DEFAULT 0 CHECK (price_sgd_cents >= 0),
    sf_eligible       BOOLEAN      NOT NULL DEFAULT FALSE, -- SkillsFuture Singapore
    max_capacity      INTEGER      NOT NULL DEFAULT 0 CHECK (max_capacity >= 0),
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ  NULL
);

CREATE INDEX idx_courses_tenant      ON courses (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_courses_instructor  ON courses (instructor_gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_courses_public      ON courses (public) WHERE deleted_at IS NULL;
CREATE INDEX idx_courses_atoms_gin   ON courses USING GIN (atom_ids);

CREATE TRIGGER trg_courses_updated_at
    BEFORE UPDATE ON courses
    FOR EACH ROW EXECUTE FUNCTION delivery_set_updated_at();

ALTER TABLE courses ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON courses
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- course_enrollments — learner registration per course
-- -----------------------------------------------------------------------------
CREATE TABLE course_enrollments (
    enrollment_id     UUID                   PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID                   NOT NULL,
    course_id         UUID                   NOT NULL REFERENCES courses(course_id) ON DELETE RESTRICT,
    gcid              UUID                   NOT NULL,
    role              course_enrollment_role NOT NULL DEFAULT 'learner',
    enrolled_at       TIMESTAMPTZ            NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ            NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ            NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ            NULL,
    UNIQUE (course_id, gcid)
);

CREATE INDEX idx_course_enroll_tenant ON course_enrollments (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_course_enroll_gcid   ON course_enrollments (gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_course_enroll_course ON course_enrollments (course_id) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_course_enrollments_updated_at
    BEFORE UPDATE ON course_enrollments
    FOR EACH ROW EXECUTE FUNCTION delivery_set_updated_at();

ALTER TABLE course_enrollments ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON course_enrollments
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- classes — single delivery instance of a course
-- -----------------------------------------------------------------------------
CREATE TABLE classes (
    class_id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID         NOT NULL,
    course_id         UUID         NOT NULL REFERENCES courses(course_id) ON DELETE RESTRICT,
    instructor_gcid   UUID         NOT NULL,
    room              VARCHAR(128) NOT NULL DEFAULT '',
    start_at          TIMESTAMPTZ  NOT NULL,
    end_at            TIMESTAMPTZ  NOT NULL,
    capacity          INTEGER      NOT NULL DEFAULT 0 CHECK (capacity >= 0),
    bookings_count    INTEGER      NOT NULL DEFAULT 0 CHECK (bookings_count >= 0),
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ  NULL,
    CHECK (end_at > start_at),
    CHECK (bookings_count <= capacity)
);

CREATE INDEX idx_classes_tenant     ON classes (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_classes_course     ON classes (course_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_classes_instructor ON classes (instructor_gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_classes_start      ON classes (start_at) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_classes_updated_at
    BEFORE UPDATE ON classes
    FOR EACH ROW EXECUTE FUNCTION delivery_set_updated_at();

ALTER TABLE classes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON classes
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- class_rostering — Booking → Class assignment with state machine
-- -----------------------------------------------------------------------------
CREATE TABLE class_rostering (
    booking_id        UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID            NOT NULL,
    class_id          UUID            NOT NULL REFERENCES classes(class_id) ON DELETE RESTRICT,
    course_id         UUID            NOT NULL,
    gcid              UUID            NOT NULL,
    status            booking_status  NOT NULL DEFAULT 'pending',
    slot_start        TIMESTAMPTZ     NULL,
    slot_end          TIMESTAMPTZ     NULL,
    created_at        TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ     NULL,
    UNIQUE (class_id, gcid)
);

CREATE INDEX idx_class_rostering_tenant ON class_rostering (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_class_rostering_class  ON class_rostering (class_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_class_rostering_gcid   ON class_rostering (gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_class_rostering_status ON class_rostering (status) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_class_rostering_updated_at
    BEFORE UPDATE ON class_rostering
    FOR EACH ROW EXECUTE FUNCTION delivery_set_updated_at();

ALTER TABLE class_rostering ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON class_rostering
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- attendance_records — idempotent on (tenant_id, class_id, gcid)
--
-- Records both QR-scan + manual override flows. QR-scan source carries
-- qr_token_used so abuse reports can correlate to issued tokens.
-- -----------------------------------------------------------------------------
CREATE TABLE attendance_records (
    attendance_id     UUID                PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID                NOT NULL,
    class_id          UUID                NOT NULL REFERENCES classes(class_id) ON DELETE RESTRICT,
    gcid              UUID                NOT NULL,
    source            attendance_source   NOT NULL,
    status            attendance_status   NOT NULL DEFAULT 'present',
    qr_token_used     TEXT                NULL,
    recorded_at       TIMESTAMPTZ         NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ         NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ         NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, class_id, gcid)
);

CREATE INDEX idx_attendance_class       ON attendance_records (class_id);
CREATE INDEX idx_attendance_gcid        ON attendance_records (gcid);
CREATE INDEX idx_attendance_recorded_at ON attendance_records (recorded_at);

CREATE TRIGGER trg_attendance_updated_at
    BEFORE UPDATE ON attendance_records
    FOR EACH ROW EXECUTE FUNCTION delivery_set_updated_at();

ALTER TABLE attendance_records ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON attendance_records
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- certifications — APPEND-ONLY credentials with SHA-256 integrity hash
-- -----------------------------------------------------------------------------
CREATE TABLE certifications (
    certification_id  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID         NOT NULL,
    course_id         UUID         NOT NULL,
    gcid              UUID         NOT NULL,
    accomplishments   TEXT[]       NOT NULL DEFAULT '{}',
    score             SMALLINT     NULL CHECK (score IS NULL OR (score BETWEEN 0 AND 100)),
    signature_hash    CHAR(64)     NOT NULL,         -- SHA-256 hex (deterministic)
    issued_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (course_id, gcid)
);

CREATE INDEX idx_certifications_tenant ON certifications (tenant_id);
CREATE INDEX idx_certifications_gcid   ON certifications (gcid);
CREATE INDEX idx_certifications_course ON certifications (course_id);

CREATE OR REPLACE FUNCTION enforce_certifications_append_only()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'certifications is append-only: % rejected', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_certifications_no_update
    BEFORE UPDATE ON certifications
    FOR EACH ROW EXECUTE FUNCTION enforce_certifications_append_only();

CREATE TRIGGER trg_certifications_no_delete
    BEFORE DELETE ON certifications
    FOR EACH ROW EXECUTE FUNCTION enforce_certifications_append_only();

ALTER TABLE certifications ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON certifications
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;
