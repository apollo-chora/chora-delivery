-- 001_create_extensions_and_enums.up.sql
-- Extensions and ENUM types for chora-examadmin.

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Exam contract lifecycle status.
CREATE TYPE exam_contract_status AS ENUM (
    'draft',
    'active',
    'expired',
    'terminated'
);

-- Exam sitting lifecycle status.
CREATE TYPE exam_sitting_status AS ENUM (
    'scheduled',
    'in_progress',
    'completed',
    'cancelled'
);

-- Candidate registration status.
CREATE TYPE exam_registration_status AS ENUM (
    'registered',
    'confirmed',
    'checked_in',
    'no_show',
    'withdrawn'
);

-- Proctor session status.
CREATE TYPE exam_proctor_session_status AS ENUM (
    'active',
    'completed'
);

-- Exam incident type.
CREATE TYPE exam_incident_type AS ENUM (
    'misconduct',
    'technical_failure',
    'medical_emergency',
    'fire_alarm',
    'other'
);

-- Exam incident severity.
CREATE TYPE exam_incident_severity AS ENUM (
    'low',
    'medium',
    'high',
    'critical'
);

-- Exam appeal status.
CREATE TYPE exam_appeal_status AS ENUM (
    'submitted',
    'under_review',
    'upheld',
    'rejected'
);

-- Shared trigger function: auto-update updated_at on row modification.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
