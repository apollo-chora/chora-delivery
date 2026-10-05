-- 001_create_extensions_and_enums.up.sql
-- Extensions and ENUM types for chora-wbl.

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Internship posting lifecycle.
CREATE TYPE internship_status AS ENUM (
    'draft',
    'open',
    'closed',
    'archived'
);

-- Application workflow status.
CREATE TYPE wbl_application_status AS ENUM (
    'submitted',
    'under_review',
    'approved',
    'rejected'
);

-- Placement lifecycle.
CREATE TYPE placement_status AS ENUM (
    'active',
    'completed',
    'withdrawn'
);

-- Capstone project lifecycle.
CREATE TYPE capstone_status AS ENUM (
    'draft',
    'active',
    'completed',
    'archived'
);

-- Skill endorsement level.
CREATE TYPE endorsement_level AS ENUM (
    'beginner',
    'intermediate',
    'advanced',
    'expert'
);

-- Industry partner status.
CREATE TYPE partner_status AS ENUM (
    'active',
    'inactive'
);

-- Shared trigger function: auto-update updated_at on row modification.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
