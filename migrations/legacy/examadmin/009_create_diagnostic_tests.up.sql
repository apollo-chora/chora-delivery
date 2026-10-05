-- 009_create_diagnostic_tests.up.sql
-- DiagnosticTest, StudyPlan, WeaknessDrill — exam prep entities.
-- Tenant-scoped with RLS. UUIDv7 primary keys.

CREATE TYPE study_plan_status AS ENUM ('active', 'completed', 'abandoned');
CREATE TYPE drill_status AS ENUM ('pending', 'in_progress', 'completed');

-- DiagnosticTest: a diagnostic assessment taken before an exam.
CREATE TABLE diagnostic_tests (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL,
    gcid              UUID NOT NULL,
    exam_id           UUID NOT NULL,
    atom_results      JSONB NOT NULL DEFAULT '[]',
    overall_score_pct NUMERIC(5,2) NOT NULL DEFAULT 0,
    weak_topic_ids    UUID[] DEFAULT '{}',
    strong_topic_ids  UUID[] DEFAULT '{}',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_diagnostic_tests_tenant ON diagnostic_tests(tenant_id);
CREATE INDEX idx_diagnostic_tests_gcid ON diagnostic_tests(tenant_id, gcid);
CREATE INDEX idx_diagnostic_tests_exam ON diagnostic_tests(tenant_id, exam_id);

ALTER TABLE diagnostic_tests ENABLE ROW LEVEL SECURITY;
ALTER TABLE diagnostic_tests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON diagnostic_tests
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- StudyPlan: a generated study plan based on diagnostic results.
CREATE TABLE study_plans (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL,
    gcid                UUID NOT NULL,
    diagnostic_test_id  UUID NOT NULL,
    recommended_atoms   JSONB NOT NULL DEFAULT '[]',
    estimated_hours     NUMERIC(6,2) NOT NULL DEFAULT 0,
    status              study_plan_status NOT NULL DEFAULT 'active',
    progress_pct        NUMERIC(5,2) NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_study_plans_tenant ON study_plans(tenant_id);
CREATE INDEX idx_study_plans_gcid ON study_plans(tenant_id, gcid);
CREATE INDEX idx_study_plans_diagnostic ON study_plans(diagnostic_test_id);

ALTER TABLE study_plans ENABLE ROW LEVEL SECURITY;
ALTER TABLE study_plans FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON study_plans
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- WeaknessDrill: a targeted drill session for weak topic areas.
CREATE TABLE weakness_drills (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL,
    gcid                UUID NOT NULL,
    diagnostic_test_id  UUID NOT NULL,
    weak_topic_node_ids UUID[] DEFAULT '{}',
    drill_atom_ids      UUID[] DEFAULT '{}',
    status              drill_status NOT NULL DEFAULT 'pending',
    results             JSONB,
    score_pct           NUMERIC(5,2) NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ
);

CREATE INDEX idx_weakness_drills_tenant ON weakness_drills(tenant_id);
CREATE INDEX idx_weakness_drills_gcid ON weakness_drills(tenant_id, gcid);
CREATE INDEX idx_weakness_drills_diagnostic ON weakness_drills(diagnostic_test_id);

ALTER TABLE weakness_drills ENABLE ROW LEVEL SECURITY;
ALTER TABLE weakness_drills FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON weakness_drills
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
