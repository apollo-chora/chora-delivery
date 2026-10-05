-- 005_create_class_profiles.up.sql
-- ClassProfile — engagement analytics per training session.
-- training_session_id is a cross-context UUID reference (no FK per DDD rule #3).

CREATE TABLE class_profiles (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID        NOT NULL,
    training_session_id  UUID        NOT NULL,
    total_participants   INTEGER     NOT NULL DEFAULT 0,
    quiz_count           INTEGER     NOT NULL DEFAULT 0,
    poll_count           INTEGER     NOT NULL DEFAULT 0,
    jamboard_count       INTEGER     NOT NULL DEFAULT 0,
    avg_engagement_score REAL        NOT NULL DEFAULT 0.0,
    top_participants     JSONB       DEFAULT '[]'::jsonb,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One profile per training session per tenant.
CREATE UNIQUE INDEX uq_class_profiles_session_tenant
    ON class_profiles (training_session_id, tenant_id);

CREATE INDEX idx_class_profiles_tenant ON class_profiles (tenant_id);

CREATE TRIGGER trg_class_profiles_updated_at
    BEFORE UPDATE ON class_profiles
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- RLS (DP-02)
ALTER TABLE class_profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE class_profiles FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON class_profiles
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
