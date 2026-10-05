-- 002_create_training_sessions.up.sql
-- TrainingSession: aggregate root for training administration.
-- Tenant-scoped with RLS. Soft-delete via deleted_at.

CREATE TABLE training_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    title           VARCHAR(255) NOT NULL,
    description     TEXT,
    status          session_status NOT NULL DEFAULT 'draft',
    delivery_mode   delivery_mode NOT NULL DEFAULT 'virtual',
    locked_path_id  UUID,                -- Cross-context ref to LockedPath (no FK)
    instructor_gcid UUID,                -- Cross-context ref to GCID (no FK)
    max_capacity    INTEGER,
    enrolled_count  INTEGER NOT NULL DEFAULT 0,
    enrollment_open BOOLEAN NOT NULL DEFAULT false,
    starts_at       TIMESTAMPTZ,
    ends_at         TIMESTAMPTZ,
    location        VARCHAR(500),
    class_section_id UUID,               -- Nullable: Campus Operations add-on
    metadata        JSONB DEFAULT '{}',
    created_by_gcid UUID NOT NULL,       -- Cross-context ref to GCID (no FK)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ          -- Soft delete (DDD rule #5)
);

-- Indexes
CREATE INDEX idx_training_sessions_tenant_id ON training_sessions(tenant_id);
CREATE INDEX idx_training_sessions_status ON training_sessions(tenant_id, status) WHERE deleted_at IS NULL;
CREATE INDEX idx_training_sessions_instructor ON training_sessions(tenant_id, instructor_gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_training_sessions_starts_at ON training_sessions(tenant_id, starts_at) WHERE deleted_at IS NULL;
CREATE INDEX idx_training_sessions_locked_path ON training_sessions(locked_path_id) WHERE deleted_at IS NULL;

-- Auto-update updated_at
CREATE TRIGGER training_sessions_updated_at
    BEFORE UPDATE ON training_sessions
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE training_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE training_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON training_sessions
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
