-- =============================================================================
-- chora-delivery : 0024_surveys.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : R+ durability sweep Wave 2 — pg-back the Survey aggregate
-- Date          : 2026-06-01
-- Story         : CHO-1580 (R+ Stage C-lite) — durability debt clearance
--
-- Purpose:
--   Persist the Survey + SurveyResponse aggregates (R+ training-feedback
--   surface, FSM DRAFT->DISTRIBUTED->CLOSED) so they survive a pod restart
--   and are durably listable. Before these tables, surveys + their responses
--   lived only in inmem.SurveyRepo — ephemeral. Wave-2 sibling of the
--   Exams/Wbl/ProjectGroups/SkillsFutures pg-backs (Wave 1).
--
--   Storage = JSONB aggregate snapshot + extracted columns for keying /
--   listing / RLS, identical to 0020's exams (every field on Survey,
--   Question, SurveyResponse + Answer is exported, so the JSONB round-trip
--   is lossless).
--
-- ROW LEVEL SECURITY (tenant_isolation): surveys is admin CRUD, always
--   tenant-scoped on every path (create/list/get/publish/close all carry
--   X-Tenant-Id). SurveyResponse carries no tenant in the domain struct, so
--   its tenant_id column is populated from the RLS session tenant on the ctx
--   (the same value SET LOCAL chora.tenant_id enforces) — the tenant_isolation
--   WITH CHECK on insert guarantees the two agree. So, unlike 0017/0018 whose
--   tenant-less WS by-id resolve forced handler-only isolation, RLS is ENABLED
--   here for defence-in-depth (the 0019_bookings / 0020_exams rationale).
--   pg.SurveyRepo calls rls.ApplySession (SET LOCAL chora.tenant_id) before
--   every query.
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   set in 9999_grant_app_roles.sql (same mechanism 0009..0023 rely on).
-- =============================================================================

CREATE TABLE IF NOT EXISTS surveys (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    state       TEXT        NOT NULL,
    data        JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_surveys_tenant
    ON surveys (tenant_id) WHERE deleted_at IS NULL;

ALTER TABLE surveys ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON surveys
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- survey_responses — one row per (survey_id, gcid). Append-only at the
-- aggregate level; the UNIQUE (survey_id, gcid) constraint is DB-level
-- defence-in-depth for the handler's HasResponse pre-check.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS survey_responses (
    id           UUID PRIMARY KEY,
    survey_id    UUID        NOT NULL,
    tenant_id    UUID        NOT NULL,
    gcid         UUID        NOT NULL,
    data         JSONB       NOT NULL,
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (survey_id, gcid)
);

CREATE INDEX IF NOT EXISTS idx_survey_responses_survey
    ON survey_responses (survey_id);
CREATE INDEX IF NOT EXISTS idx_survey_responses_tenant
    ON survey_responses (tenant_id);

ALTER TABLE survey_responses ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON survey_responses
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
