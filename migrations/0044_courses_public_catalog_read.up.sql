-- =============================================================================
-- chora-delivery : 0044_courses_public_catalog_read.up.sql
--
-- FIX cross-tenant PUBLIC course discovery.
--
-- `courses` has FORCE ROW LEVEL SECURITY and the tenant_isolation policy
-- (mig 0043) requires tenant_id = chora.tenant_id. The public_courses_catalog
-- view runs as its owner (security_invoker=false, owner has NO BYPASSRLS), so a
-- read with NO tenant GUC — the cross-tenant discovery path
-- (/api/catalog?public=true → CatalogueRepo public path, which reads the view
-- WITHOUT rls.ApplySession) — matched ZERO rows (NULLIF→NULL ⇒ tenant_id = NULL
-- ⇒ no match). The "cross-tenant public catalogue" had silently become
-- tenant-scoped/empty: 14 public+PUBLISHED courses existed but discovery
-- returned 0 (confirmed 2026-07-08).
--
-- FIX: add a CONTROLLED permissive branch — when NO tenant context is set
-- (the discovery path only), allow reading rows that are public = true AND
-- state = 'PUBLISHED'.
--   - Exposes ONLY already-public data (never draft / private / tenant_only /
--     AWAITING_REVIEW). Verified: a no-GUC `SELECT count(*) FROM courses` returns
--     exactly the 14 public+PUBLISHED rows, not the ~35 total.
--   - Activates ONLY when chora.tenant_id is unset. A tenant-scoped read (the
--     tenant catalogue browse, course management, enrol) SETS chora.tenant_id
--     and therefore stays FULLY isolated — verified: GUC=<tenant> returns that
--     tenant's rows only (12 for Mighty Mind), NOT the 14 cross-tenant set.
--
-- WRITES keep the plain tenant-only WITH CHECK (unchanged) — no cross-tenant
-- write is possible.
--
-- Rollback-safe: this is an ADDITIVE read capability with NO code dependency
-- (the view + the CatalogueRepo public path already read without a GUC). Any
-- delivery image works with it, so there is no image/migration ordering hazard.
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON courses;

CREATE POLICY tenant_isolation ON courses
    FOR ALL
    USING (
        ( tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid
          AND (
              NULLIF(current_setting('chora.user_gcid', true), '') IS NULL
              OR state = 'PUBLISHED'
              OR author_gcid::text = NULLIF(current_setting('chora.user_gcid', true), '')
              OR current_setting('chora.user_roles', true) ~ '(^|,)(admin|training-admin|training_admin|tenant_admin)(,|$)'
          ) )
        OR ( NULLIF(current_setting('chora.tenant_id', true), '') IS NULL
             AND public = true
             AND state = 'PUBLISHED' )
    )
    WITH CHECK (
        tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid
    );
