-- =============================================================================
-- chora-delivery : 0031_offerings.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : R+ four-delivery-mode refactor W1
-- Date          : 2026-06-25
-- ADR           : ADR-190 (delivery_type policy over ONE Content Delivery
--                 context; the offering/Cohort instance carries delivery_type)
-- Story         : CHO-1849
--
-- Purpose:
--   Persist the Offering aggregate — the delivery INSTANCE of a reusable
--   Course, carrying delivery_type {graduate|short|async} + the lifecycle FSM
--   DRAFT->LAUNCHED->RUNNING->CONCLUDED->ARCHIVED. Promotes the prior
--   (unpersisted) Cohort skeleton. Exam is its own bounded context (ADR-190)
--   and is NOT a delivery_type here.
--
--   Storage = JSONB aggregate snapshot + extracted columns for keying / RLS
--   scoping / listing — the 0020_exams pattern. delivery_type + course_id are
--   extracted alongside the FSM state so the R+ universal finder (W2) can list
--   + facet by delivery_type without unpacking the JSONB blob.
--
-- ROW LEVEL SECURITY (tenant_isolation): offerings is admin CRUD, always
--   tenant-scoped on every path (create/list/get all carry X-Tenant-Id). RLS is
--   ENABLED for defence-in-depth; pg.OfferingRepo calls rls.ApplySession
--   (SET LOCAL chora.tenant_id) before every query. The USING/WITH CHECK pair
--   uses the NULLIF-safe cast (mig-0019 lesson: a pooled conn can leave the GUC
--   at '' and ''::uuid throws 22P02) and blocks writing rows into a foreign
--   tenant (the 0029_course_content_items write-path form).
--
-- Grants: app_rw / app_ro inherit via the persistent ALTER DEFAULT PRIVILEGES
--   in 9999_grant_app_roles.sql — no per-table grants here.
-- =============================================================================

CREATE TABLE IF NOT EXISTS offerings (
    id            UUID PRIMARY KEY,
    tenant_id     UUID        NOT NULL,
    course_id     UUID        NOT NULL,
    delivery_type TEXT        NOT NULL,
    state         TEXT        NOT NULL,
    data          JSONB       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);

-- Active-listing index, tenant-scoped (the finder's default query).
CREATE INDEX IF NOT EXISTS idx_offerings_tenant
    ON offerings (tenant_id) WHERE deleted_at IS NULL;

-- Faceted finder index: tenant + delivery_type (W2 universal finder badges).
CREATE INDEX IF NOT EXISTS idx_offerings_tenant_delivery_type
    ON offerings (tenant_id, delivery_type) WHERE deleted_at IS NULL;

ALTER TABLE offerings ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON offerings
    USING (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);
