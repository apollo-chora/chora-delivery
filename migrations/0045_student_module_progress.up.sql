-- =============================================================================
-- chora-delivery : 0045_student_module_progress.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : WS-A W7 course-structure layer (StudentModuleProgress follow-up)
-- Story         : CHO-2074
-- ADR           : ADR-190 (ONE Content Delivery context; Course structure)
--
-- Purpose:
--   Persist the StudentModuleProgress aggregate — the per-learner (GCID),
--   per-Module completion projection that folds content-item completion events
--   (e.g. chora.consumption.atom_session.completed.v1) into progress toward a
--   Module's completion Requirement (all_items / n_of_m / specific_items, 0039).
--
--   A row is its OWN aggregate root, keyed by (tenant_id, gcid, module_id). It
--   holds CROSS-AGGREGATE references — module_id → course_modules(id) and
--   course_id → courses(id) — BY UUID with NO FK, per
--   .claude/rules/ddd-enforcement.md Invariant #3 (child entities only via their
--   root; cross-aggregate refs are UUIDs without FK, validated via events). This
--   is the mirror image of 0039's course_module_items.module_id, which IS an FK
--   because that edge is WITHIN the module aggregate; here the module is a
--   DIFFERENT aggregate, so no FK. Intra-chora_delivery only — cross-DB FORBIDDEN.
--   completed_content_item_ids is an append-only JSONB array of the module's
--   course_content item ids the learner has completed.
--
-- ROW LEVEL SECURITY (tenant_isolation): the projection is tenant-scoped and
--   read by BOTH the learner (own rows) and the instructor (the whole cohort),
--   so RLS filters by tenant only — the per-GCID gate is enforced at the handler
--   (a learner sees own; instructor/admin sees all), mirroring the offering
--   roster surface. The USING/WITH CHECK pair uses the NULLIF-safe cast (mig-0019
--   lesson: a pooled conn can leave the GUC at '' and ''::uuid throws 22P02) and
--   blocks writing a row into a foreign tenant — the 0039 write-path form.
--   pg.StudentModuleProgressRepo calls rls.ApplySession (SET LOCAL
--   chora.tenant_id) before every query; the app role is NOBYPASSRLS.
--
-- Uniqueness: at most one ACTIVE projection per (tenant, gcid, module) — a
--   partial UNIQUE index excluding soft-deleted rows, so a concurrent
--   double-create fails loud instead of forking a learner's progress.
--
-- Grants: explicit per-DB app-role grants (0039 form). These MUST target
--   chora_delivery_app_rw / chora_delivery_app_ro — a global app_rw role does
--   not exist and would abort + roll back the migration.
-- =============================================================================

CREATE TABLE IF NOT EXISTS student_module_progress (
    id                         UUID PRIMARY KEY,
    tenant_id                  UUID        NOT NULL,
    gcid                       UUID        NOT NULL,
    module_id                  UUID        NOT NULL,
    course_id                  UUID        NOT NULL,
    completed_content_item_ids JSONB       NOT NULL DEFAULT '[]'::jsonb,
    is_complete                BOOLEAN     NOT NULL DEFAULT false,
    completed_at               TIMESTAMPTZ,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at                 TIMESTAMPTZ
);

-- One ACTIVE projection per learner-module (concurrent double-create fails loud).
CREATE UNIQUE INDEX IF NOT EXISTS uq_student_module_progress_learner_module
    ON student_module_progress (tenant_id, gcid, module_id)
    WHERE deleted_at IS NULL;

-- Instructor cohort roll-up: all learners' progress for one module.
CREATE INDEX IF NOT EXISTS idx_student_module_progress_module
    ON student_module_progress (tenant_id, module_id)
    WHERE deleted_at IS NULL;

-- Learner course view: all module progress for one learner in a course.
CREATE INDEX IF NOT EXISTS idx_student_module_progress_learner_course
    ON student_module_progress (tenant_id, gcid, course_id)
    WHERE deleted_at IS NULL;

ALTER TABLE student_module_progress ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON student_module_progress
    USING (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

-- ----------------------------------------------------------------------------
-- Per-DB app-role grants (MUST be chora_delivery_app_rw / _app_ro).
-- ----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON student_module_progress TO chora_delivery_app_rw;
GRANT SELECT                         ON student_module_progress TO chora_delivery_app_ro;
