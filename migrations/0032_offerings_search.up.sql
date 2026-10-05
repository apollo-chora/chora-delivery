-- =============================================================================
-- chora-delivery : 0032_offerings_search.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : R+ four-delivery-mode refactor W2.A
-- Date          : 2026-06-25
-- ADR           : ADR-190 (delivery_type policy) + the universal-finder spec
--                 docs/design/ux_universal_search_collection.md
-- Story         : CHO-1850
--
-- Purpose:
--   Back the R+ universal finder's GET /api/v1/search/offerings — keyset
--   (cursor) pagination + server multi-sort + query-minus-self facet counts +
--   free-text label search. Extends 0031_offerings.
--
--   1. Extract a `label` column from the JSONB snapshot so the finder can
--      sort / keyset / free-text-search on label without unpacking the blob
--      (the 0031 extraction pattern — delivery_type/state already extracted).
--      Backfilled from data->>'Label' for the W1 rows; the upsert
--      (pg.SQLUpsertOffering) now writes it on every Save.
--   2. Keyset-sort indexes for the three sortable columns, tenant-scoped +
--      partial on the active set (deleted_at IS NULL), composite with id as the
--      tiebreak so ORDER BY (<sort>, id) is index-ordered.
--   3. A tenant+state facet index (the tenant+delivery_type facet index already
--      exists from 0031).
--
-- Free-text label search uses ILIKE (substring, case-insensitive) — pg_trgm is
-- NOT enabled in chora_delivery and the per-tenant offering set is small, so a
-- trigram GIN index is deferred (add when an offering corpus warrants it).
--
-- RLS: unchanged — the tenant_isolation policy from 0031 still applies; the
-- finder runs under rls.ApplySession like every other offering query.
-- Idempotent (IF NOT EXISTS / guarded backfill) per the migration-runner
-- fail-fast lesson (project_migration_runner_failfast_wedge_2026_06_22).
-- =============================================================================

ALTER TABLE offerings ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT '';

-- Backfill the W1 rows from the JSONB snapshot (Go field name = 'Label').
UPDATE offerings SET label = COALESCE(data->>'Label', '')
WHERE label = '' AND data ? 'Label';

-- Keyset-sort indexes (tenant-scoped, active-only, id tiebreak).
CREATE INDEX IF NOT EXISTS idx_offerings_tenant_created_id
    ON offerings (tenant_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_offerings_tenant_updated_id
    ON offerings (tenant_id, updated_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_offerings_tenant_label_id
    ON offerings (tenant_id, label, id) WHERE deleted_at IS NULL;

-- State facet index (delivery_type facet index already exists from 0031).
CREATE INDEX IF NOT EXISTS idx_offerings_tenant_state
    ON offerings (tenant_id, state) WHERE deleted_at IS NULL;
