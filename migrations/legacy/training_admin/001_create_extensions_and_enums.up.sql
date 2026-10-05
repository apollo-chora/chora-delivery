-- 001_create_extensions_and_enums.up.sql
-- Extensions and ENUM types for chora-training-admin.

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Training session lifecycle status.
-- draft: initial state, can be modified freely
-- scheduled: enrollment is open, session awaiting start
-- in_progress: session is actively running
-- completed: session has ended normally
-- cancelled: session was cancelled before completion
CREATE TYPE session_status AS ENUM (
    'draft',
    'scheduled',
    'in_progress',
    'completed',
    'cancelled'
);

-- How the training is delivered.
CREATE TYPE delivery_mode AS ENUM (
    'physical',
    'virtual',
    'hybrid'
);

-- Attendance marking for a learner.
CREATE TYPE attendance_status AS ENUM (
    'present',
    'late',
    'absent',
    'excused'
);

-- How attendance was recorded.
CREATE TYPE check_in_method AS ENUM (
    'manual',
    'qr_scan',
    'nfc',
    'geo_checkin'
);

-- Training application workflow status.
-- draft → submitted → under_review → approved/rejected/waitlisted
CREATE TYPE application_status AS ENUM (
    'draft',
    'submitted',
    'under_review',
    'approved',
    'rejected',
    'waitlisted'
);

-- Type of trainee request.
CREATE TYPE trainee_request_type AS ENUM (
    'deferral',
    'withdrawal',
    'makeup',
    'transfer'
);

-- Trainee request workflow status.
CREATE TYPE request_status AS ENUM (
    'submitted',
    'under_review',
    'approved',
    'rejected'
);

-- Certificate program lifecycle.
CREATE TYPE program_status AS ENUM (
    'draft',
    'active',
    'archived'
);

-- Program enrollment status.
CREATE TYPE enrollment_status AS ENUM (
    'enrolled',
    'in_progress',
    'completed',
    'withdrawn'
);

-- Schedule recurrence type.
CREATE TYPE recurrence_type AS ENUM (
    'one_off',
    'weekly',
    'biweekly'
);

-- Day of week for schedules.
CREATE TYPE day_of_week AS ENUM (
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
