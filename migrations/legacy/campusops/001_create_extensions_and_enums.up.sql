-- 001_create_extensions_and_enums.up.sql
-- Extensions and ENUM types for chora-campusops.

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Academic term type.
CREATE TYPE term_type AS ENUM (
    'semester',
    'trimester',
    'quarter',
    'summer',
    'custom'
);

-- Room type classification.
CREATE TYPE room_type AS ENUM (
    'classroom',
    'lab',
    'auditorium',
    'conference',
    'studio',
    'other'
);

-- Class section lifecycle status.
CREATE TYPE section_status AS ENUM (
    'active',
    'cancelled',
    'completed'
);

-- Attendance marking for a learner (shared with training-admin domain).
CREATE TYPE campus_attendance_status AS ENUM (
    'present',
    'late',
    'absent',
    'excused'
);

-- How attendance was recorded (shared with training-admin domain).
CREATE TYPE campus_check_in_method AS ENUM (
    'manual',
    'qr_scan',
    'nfc',
    'geo_checkin'
);

-- Facility booking lifecycle status.
CREATE TYPE booking_status AS ENUM (
    'pending',
    'confirmed',
    'cancelled'
);

-- Day of week for time slots.
CREATE TYPE campus_day_of_week AS ENUM (
    'mon',
    'tue',
    'wed',
    'thu',
    'fri',
    'sat',
    'sun'
);

-- Shared trigger function: auto-update updated_at on row modification.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
