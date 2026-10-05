-- =============================================================================
-- chora-delivery : 0044_courses_public_catalog_read.down.sql
--
-- Restore the mig-0043 tenant_isolation policy — drops the no-tenant
-- public-discovery branch, returning `courses` to strict tenant-scoped reads.
-- After this, cross-tenant public discovery (public_courses_catalog with no
-- tenant GUC) returns 0 again.
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
