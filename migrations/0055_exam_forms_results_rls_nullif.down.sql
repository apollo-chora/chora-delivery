-- =============================================================================
-- chora-delivery : 0055_exam_forms_results_rls_nullif.down.sql
--
-- Restore the mig-0046 tenant_isolation policies on exam_forms + exam_results,
-- verbatim: FOR ALL USING with the UNGUARDED cast and no explicit WITH CHECK.
--
-- ⚠ This REARMS the 22P02: after this runs, a query on a pooled connection whose
-- chora.tenant_id GUC has reverted to '' throws "invalid input syntax for type
-- uuid" instead of matching no rows. Down is here for lineage symmetry, not
-- because reverting is safe.
--
-- The restored form is byte-for-byte the 0046 original (lines 66-67 and 95-96),
-- so this down is a true inverse: a re-run of 0055 is idempotent on top of it.
-- =============================================================================

DROP POLICY IF EXISTS tenant_isolation ON exam_forms;

CREATE POLICY tenant_isolation ON exam_forms
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

DROP POLICY IF EXISTS tenant_isolation ON exam_results;

CREATE POLICY tenant_isolation ON exam_results
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
