-- =============================================================================
-- chora-delivery : 0043_courses_state_aware_rls.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Relates to    : Sub-phase B security hardening — B2.4 state-aware RLS on courses.
--
-- ⚠️ 2-PHASE DEPLOY — APPLY THIS **AFTER** the role-GUC code is deployed.
--   rls.ApplySession only emits `SET LOCAL chora.user_roles` once the new
--   chora-delivery binary is live. If this policy lands BEFORE that code, the
--   training-admin review queue (handleCJ2CourseList reads OTHER authors'
--   AWAITING_REVIEW with gcid set) would see an empty chora.user_roles and be
--   filtered to PUBLISHED+own — i.e. the review queue breaks. Deploy code → then
--   apply this migration.
--
-- WHAT IT DOES: replaces the plain tenant_isolation policy on `courses` with a
--   state-aware one that adds defence-in-depth over the app-layer visibility
--   checks (a DRAFT course must never reach a learner even if a WHERE clause
--   omits the state filter — e.g. the tenant-scoped catalogue browse has no app
--   state filter today). A row is READABLE when the caller's tenant matches AND
--   any of:
--     - NO user context (chora.user_gcid unset) → unrestricted. FAIL-OPEN so the
--       tenant-only / system read paths (offering-publish catalogue Get, event
--       projections, migrations) are UNCHANGED — zero breakage.
--     - state = 'PUBLISHED'                       → everyone sees published.
--     - author_gcid = chora.user_gcid             → an author sees OWN (any state).
--     - chora.user_roles contains admin/training-admin/tenant_admin → staff see all
--       (keeps the review queue + release flow working).
--
--   WRITES keep the plain tenant-only WITH CHECK (NOT state-gated) so SaveCJ2 can
--   still create/update DRAFT courses. NULLIF-safe casts (mig-0019 lesson: a
--   pooled conn can leave a GUC '' and ''::uuid throws 22P02).
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON courses;

CREATE POLICY tenant_isolation ON courses
    FOR ALL
    USING (
        tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid
        AND (
            NULLIF(current_setting('chora.user_gcid', true), '') IS NULL
            OR state = 'PUBLISHED'
            OR author_gcid::text = NULLIF(current_setting('chora.user_gcid', true), '')
            OR current_setting('chora.user_roles', true) ~ '(^|,)(admin|training-admin|training_admin|tenant_admin)(,|$)'
        )
    )
    WITH CHECK (
        tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid
    );
