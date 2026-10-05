-- =============================================================================
-- chora-delivery : 0055_exam_forms_results_rls_nullif.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Date          : 2026-07-16
-- ADR           : ADR-192 (cross-tenant exam rollup RLS surface), the
--                 "NULLIF-safe cast on every GUC read" requirement.
--
-- FIX the unguarded GUC cast on the exam_forms + exam_results tenant_isolation
-- policies (mig 0046 lines 67 + 96):
--
--     USING (tenant_id = current_setting('chora.tenant_id', true)::uuid)
--
-- current_setting(..., true) returns NULL when the GUC was never SET, and NULL
-- casts harmlessly. But a POOLED connection does not leave the GUC unset: it
-- reverts it to the EMPTY STRING, and ''::uuid raises 22P02
-- (invalid input syntax for type uuid). So the policy throws rather than
-- filters, on a connection whose only sin is having been reused. That is the
-- mig-0019/0045 lesson, already cited by this migration's own sibling
-- 0047_exam_candidates.up.sql:34-37, which shipped the guarded form.
--
-- exam_results is where a PASS/FAIL outcome lives, and the certification lane
-- reads it from a Pub/Sub subscriber, where a 22P02 becomes a NACK and a
-- redelivery loop rather than a visible 400.
--
-- The fix is the sibling's exact form: NULLIF(..., '') collapses BOTH the unset
-- (NULL) and pooled-empty ('') GUC to NULL before the cast, so the predicate
-- evaluates to NULL and matches no rows instead of throwing.
--
-- HONEST SCOPE, what this does NOT do:
--   - It does not make a missing rls.ApplySession safe. It converts a 22P02
--     crash into a deterministic ZERO-ROW denial. Zero-rows-on-a-dead-context is
--     its own trap class, and the guard against it is the app-side contract
--     (pg repos call rls.ApplySession before every query) plus the bare-context
--     integration tests, NOT this policy.
--   - It does not repair or re-scope any existing row. Policy-only, no DML.
--   - It changes NO isolation boundary: app_rw / app_ro are NOBYPASSRLS
--     non-owners and were already fully bound by ENABLE. This closes a
--     crash-on-pooled-conn hole, not a leak.
--
-- WITH CHECK is stated EXPLICITLY. It is not a behaviour change: a FOR ALL
-- policy with USING and no WITH CHECK already reuses USING for the write check.
-- Spelling it out matches 0047 / 0038 / 0045 and keeps the write intent from
-- depending on a Postgres default that a future edit could silently drop.
--
-- FORCE ROW LEVEL SECURITY is deliberately NOT added here: see the .down.sql
-- header and the commit body. ADR-192 wants it as defence-in-depth for the OWNER
-- role, but 0038_credentials.up.sql:19-24 records the opposite as a DELIBERATE
-- chora_delivery convention ("RLS is ENABLED (not FORCEd) ... the migrate role
-- (table owner, BYPASSRLS) must stay exempt to run cross-tenant backfills"), and
-- 0044 records a dated live incident (2026-07-08) where FORCE + NULLIF silently
-- returned 0 rows on `courses`. Resolving that conflict needs the deployed
-- pg_roles.rolbypassrls value, which this change cannot verify. Shipping the
-- unambiguous half now; FORCE is raised as a separate recommendation.
--
-- Idempotent: DROP POLICY IF EXISTS then CREATE. Re-runnable.
-- Rollback-safe: policy-only, no DDL on columns, no data touched. Any delivery
-- image works with it (the app already sets the GUC), so there is no
-- image/migration ordering hazard.
-- =============================================================================

-- ----------------------------------------------------------------------------
-- exam_forms: replaces the mig-0046 unguarded-cast policy.
-- ----------------------------------------------------------------------------
DROP POLICY IF EXISTS tenant_isolation ON exam_forms;

CREATE POLICY tenant_isolation ON exam_forms
    FOR ALL
    USING      (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

-- ----------------------------------------------------------------------------
-- exam_results: replaces the mig-0046 unguarded-cast policy.
-- ----------------------------------------------------------------------------
DROP POLICY IF EXISTS tenant_isolation ON exam_results;

CREATE POLICY tenant_isolation ON exam_results
    FOR ALL
    USING      (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);
