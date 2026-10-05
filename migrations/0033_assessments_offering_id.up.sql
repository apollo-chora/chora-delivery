-- =============================================================================
-- chora-delivery : 0033_assessments_offering_id.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : R+ four-delivery-mode refactor W3.A
-- Date          : 2026-06-28
-- ADR           : ADR-190 (delivery_type policy over ONE Content Delivery
--                 context; the Offering carries delivery_type)
--
-- Purpose:
--   Make an Offering's assessments addressable (W3.A). Scope the existing LIVE
--   Assessment aggregate (0010_assessments_submissions) to an Offering by
--   adding a nullable offering_id column + a tenant-scoped partial index that
--   backs GET /api/v1/offerings/{id}/assessments (AssessmentRepo.ListByOffering,
--   created_at DESC, deleted_at IS NULL).
--
--   offering_id is a same-DB soft FK to chora_delivery.offerings (0031) — UUID
--   without a Go-level FK constraint per ddd-enforcement #3. It is OPTIONAL /
--   nullable (like class_id from 0010): a freestanding assessment leaves it
--   NULL; an offering-scoped one carries the offering id. Distinct from
--   class_id (NOT an overload).
--
-- ROW LEVEL SECURITY: unchanged — the assessments_tenant_isolation policy from
--   0010 still applies; ListByOffering runs under rls.ApplySession like every
--   other assessment query. No new policy, no new grant (app_rw/app_ro inherit).
--
-- Idempotent (ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS) per the
-- migration-runner fail-fast lesson (project_migration_runner_failfast_wedge).
-- =============================================================================

ALTER TABLE assessments ADD COLUMN IF NOT EXISTS offering_id UUID;

-- Offering-scoped listing index, tenant-scoped + partial on the active set.
CREATE INDEX IF NOT EXISTS idx_assessments_tenant_offering
    ON assessments (tenant_id, offering_id) WHERE deleted_at IS NULL;
