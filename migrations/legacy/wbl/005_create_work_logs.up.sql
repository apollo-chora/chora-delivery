-- 005_create_work_logs.up.sql
-- WorkLogEntry: individual work log entries within a placement.
-- Append-only pattern. Tenant-scoped with RLS.
-- Also includes SkillEndorsement table.

CREATE TABLE work_log_entries (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL,
    placement_id        UUID NOT NULL,        -- Cross-context ref (no FK)
    date                DATE NOT NULL,
    hours               NUMERIC(5,2) NOT NULL CHECK (hours > 0),
    description         TEXT NOT NULL,
    skills_applied      TEXT[] DEFAULT '{}',
    supervisor_approved BOOLEAN NOT NULL DEFAULT false,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_work_logs_tenant ON work_log_entries(tenant_id);
CREATE INDEX idx_work_logs_placement ON work_log_entries(tenant_id, placement_id);
CREATE INDEX idx_work_logs_date ON work_log_entries(placement_id, date);

-- Row-Level Security
ALTER TABLE work_log_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_log_entries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON work_log_entries
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- SkillEndorsement: supervisor endorsement of a learner's skill.
CREATE TABLE skill_endorsements (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL,
    placement_id   UUID NOT NULL,             -- Cross-context ref (no FK)
    endorser_gcid  UUID NOT NULL,             -- Cross-context ref to GCID
    skill_name     VARCHAR(255) NOT NULL,
    level          endorsement_level NOT NULL,
    comments       TEXT,
    endorsed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_endorsements_tenant ON skill_endorsements(tenant_id);
CREATE INDEX idx_endorsements_placement ON skill_endorsements(tenant_id, placement_id);
CREATE INDEX idx_endorsements_endorser ON skill_endorsements(tenant_id, endorser_gcid);

-- Row-Level Security
ALTER TABLE skill_endorsements ENABLE ROW LEVEL SECURITY;
ALTER TABLE skill_endorsements FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON skill_endorsements
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
