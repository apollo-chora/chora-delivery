-- =============================================================================
-- chora-delivery : 0006_public_catalog_view.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : agent A-Public-Catalog-View (Phyllis Step 5 unlocker)
-- Date          : 2026-05-11
-- Architecture  : Architecture Review locked 2026-05-07 (Tier 1 D1 — R+ Rhythm+)
-- Story         : CHO Content Delivery | Phyllis Step 5 | public_courses_catalog
--
-- Purpose:
--   Phyllis Step 5 (PublicDiscovery) needs cross-tenant visibility of public
--   courses. A direct query on `courses` is RLS-scoped to
--   `current_setting('chora.tenant_id')`; a learner in tenant A cannot see
--   Mr. Chen's CSPO course (`33333333-...`, public=true) under tenant B.
--   Lifting RLS at the table level (or having app roles BYPASSRLS) would
--   break the multi-tenant model.
--
--   Solution: a view that pre-filters the public-only slice and bypasses
--   RLS via the OWNER rule. Postgres views run with the privileges of the
--   view owner (security-definer semantics) when `security_invoker = false`
--   (default in PG 18). The view owner is `chora_delivery_migrate`, the
--   same role that owns `courses`. Since `courses.relforcerowsecurity = f`,
--   the migrate role's reads are not subject to the `tenant_isolation`
--   policy, so the view returns rows from every tenant — but only those
--   matching the WHERE clause `public = true AND deleted_at IS NULL`.
--
--   App roles (`chora_delivery_app_rw`, `chora_delivery_app_ro`) are
--   granted SELECT on the view but NOT BYPASSRLS on the underlying table.
--   They cannot escape RLS via any other path.
--
-- Resilience properties (per `feedback_resilience_priority`):
--
--   - **No bypass for non-public data**: the WHERE clause is the gate. A
--     row with `public=false` is unreachable through this view regardless
--     of caller tenant context. Validated by the negative test in
--     `tests/phyllis-e2e/phyllis_test.go` Step 5.
--   - **Soft-delete-aware**: `deleted_at IS NULL` always; soft-deleted
--     courses do not appear, even if they were public.
--   - **Idempotency**: `CREATE OR REPLACE VIEW` so re-applying via
--     `apply-cloudsql-migrations.sh` is a no-op.
--   - **Multi-tenant safety**: the view is intentionally tenant-agnostic
--     for public-only rows. Clients (BFF, frontend) MUST display the
--     `tenant_id` field so learners know which tenant publishes the
--     course, and may filter further by tenant if desired. RLS still
--     applies to direct `courses` queries — the view is a NARROW exception.
--   - **Dead-pod resume**: pure DDL — re-runs converge.
--
-- Cross-DB rule: this view is INTRA-chora_delivery. No JOINs to other
-- databases. Cross-domain references (atom_ids, instructor_gcid) are NOT
-- materialised through this view — the BFF resolves them via separate
-- per-DB queries / events.
--
-- Column audit (what's exposed vs excluded):
--
--   Exposed (safe for cross-tenant disclosure):
--     course_id           — public identifier (the catalog handle)
--     tenant_id           — owning-tenant identifier; clients display it
--                           so learners see who publishes the course
--     instructor_gcid     — public attribution; Mr. Chen owns his GCID
--                           globally (GCID is opaque UUIDv7, no tenant ctx)
--     title               — public marketing copy
--     description         — public marketing copy
--     price_sgd_cents     — public price; catalog display
--     sf_eligible         — SkillsFuture eligibility flag; public
--                           marketing
--     max_capacity        — informs enrollment pressure; public
--     created_at          — sortable for recency
--     updated_at          — sortable for staleness
--
--   Excluded (NOT in this view):
--     atom_ids            — cross-DB array of internal atom UUIDs;
--                           structural detail not needed for discovery
--     public              — redundant (always true via WHERE clause)
--     deleted_at          — redundant (always NULL via WHERE clause)
--
--   Excluded by absence (not in `courses` schema, but flagged for any
--   future column audit when the schema grows):
--     - internal pricing components / promo codes / billing-internal
--     - draft notes / unpublished editorial state
--     - ANY field marked PII for a non-instructor party
--
-- =============================================================================

BEGIN;

-- ----------------------------------------------------------------------------
-- The view itself.
--
-- security_invoker = false (default in PG 18, set explicitly for clarity):
--   the view runs with the privileges of the OWNER (migrate role), not the
--   invoking app role. The migrate role is the table owner and bypasses
--   RLS because `relforcerowsecurity = f` on courses. App roles invoking
--   this view inherit the migrate role's bypass strictly for the public
--   slice the WHERE clause permits.
-- ----------------------------------------------------------------------------

CREATE OR REPLACE VIEW public_courses_catalog
WITH (security_invoker = false) AS
SELECT
    course_id,
    tenant_id,
    instructor_gcid,
    title,
    description,
    price_sgd_cents,
    sf_eligible,
    max_capacity,
    created_at,
    updated_at
FROM courses
WHERE public = true
  AND deleted_at IS NULL;

COMMENT ON VIEW public_courses_catalog IS
    'Cross-tenant public course discovery view (Phyllis Step 5). Tenant-'
    'agnostic by design; pre-filters to public=true AND deleted_at IS NULL '
    'so non-public rows can NEVER leak through. View owner is '
    'chora_delivery_migrate (table owner of courses; bypasses RLS via '
    'OWNER rule + relforcerowsecurity=false). security_invoker=false '
    'makes app roles inherit the bypass ONLY for this narrow public slice. '
    'Safe to grant SELECT to app_ro/app_rw across tenants.';

-- ----------------------------------------------------------------------------
-- Grants — explicit; do not rely on default privileges alone.
--
-- 9999_grant_app_roles.sql sets default privileges for FUTURE objects, but
-- it grants only on TABLES + SEQUENCES + FUNCTIONS, not on views. Views
-- in PG inherit table-class privileges (a view is a relkind='v' relation),
-- so the existing default privileges DO actually cover this view; we
-- still GRANT explicitly to make the privilege intent legible in the
-- migration history.
-- ----------------------------------------------------------------------------

GRANT SELECT ON public_courses_catalog
    TO chora_delivery_app_rw, chora_delivery_app_ro;

COMMIT;

-- =============================================================================
-- VERIFICATION (run manually or via tests/phyllis-e2e):
--
--   -- 1. View exists + has 1+ rows under any tenant context.
--   SET ROLE chora_delivery_app_ro;
--   BEGIN;
--     SET LOCAL chora.tenant_id = '11111111-1111-7111-8111-111111111111';  -- MTM
--     SELECT count(*) FROM public_courses_catalog
--      WHERE course_id = '33333333-3333-7333-8333-333333333333';
--     -- Expected: 1   (Mr. Chen's CSPO course visible to MTM context)
--   ROLLBACK;
--
--   -- 2. Negative — non-public course (MTM private) MUST NOT appear.
--   BEGIN;
--     SET LOCAL chora.tenant_id = '22222222-2222-7222-8222-222222222222';  -- Chen's
--     SELECT count(*) FROM public_courses_catalog
--      WHERE course_id = '44444444-4444-7444-8444-444444444444';
--     -- Expected: 0   (MTM Math is public=false; never leaks)
--   ROLLBACK;
--
--   -- 3. Negative — soft-deleted public course MUST NOT appear.
--   BEGIN;
--     UPDATE courses SET deleted_at = now()
--      WHERE course_id = '33333333-3333-7333-8333-333333333333';
--     SELECT count(*) FROM public_courses_catalog
--      WHERE course_id = '33333333-3333-7333-8333-333333333333';
--     -- Expected: 0
--   ROLLBACK;   -- restores deleted_at
--
--   RESET ROLE;
-- =============================================================================
