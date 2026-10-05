-- 008_create_wbl_logs.up.sql
-- WBLLog: work-based learning log entries with supervisor approval workflow.
-- Tenant-scoped with RLS. UUIDv7 primary keys.

CREATE TYPE wbl_log_status AS ENUM ('pending_approval', 'approved', 'rejected');

CREATE TABLE wbl_logs (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID NOT NULL,
    gcid                  UUID NOT NULL,
    supervisor_gcid       UUID NOT NULL,
    activity_description  TEXT NOT NULL,
    topic_node_ids        UUID[] DEFAULT '{}',
    duration_hours        NUMERIC(5,2) NOT NULL CHECK (duration_hours > 0),
    status                wbl_log_status NOT NULL DEFAULT 'pending_approval',
    supervisor_feedback   TEXT,
    submitted_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at            TIMESTAMPTZ
);

-- Indexes
CREATE INDEX idx_wbl_logs_tenant ON wbl_logs(tenant_id);
CREATE INDEX idx_wbl_logs_gcid ON wbl_logs(tenant_id, gcid);
CREATE INDEX idx_wbl_logs_supervisor ON wbl_logs(tenant_id, supervisor_gcid);
CREATE INDEX idx_wbl_logs_status ON wbl_logs(tenant_id, status);

-- Row-Level Security
ALTER TABLE wbl_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE wbl_logs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON wbl_logs
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
