-- 001_create_extensions_and_enums.up.sql
-- Extensions and ENUM types for chora-classroom.

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Quiz session lifecycle status.
-- waiting: quiz created, accepting participants
-- active: instructor started the quiz, questions being delivered
-- paused: quiz temporarily paused
-- ended: quiz session completed
CREATE TYPE quiz_status AS ENUM (
    'waiting',
    'active',
    'paused',
    'ended'
);

-- Poll type.
-- single_choice: one selection per voter
-- multiple_choice: multiple selections allowed
-- word_cloud: free text aggregated as word cloud
CREATE TYPE poll_type AS ENUM (
    'single_choice',
    'multiple_choice',
    'word_cloud'
);

-- Poll lifecycle status.
CREATE TYPE poll_status AS ENUM (
    'open',
    'closed'
);

-- JamBoard entry type.
CREATE TYPE entry_type AS ENUM (
    'sticky_note',
    'drawing',
    'link'
);

-- JamBoard lifecycle status.
CREATE TYPE board_status AS ENUM (
    'open',
    'closed'
);

-- Shared trigger function: auto-update updated_at on row modification.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
