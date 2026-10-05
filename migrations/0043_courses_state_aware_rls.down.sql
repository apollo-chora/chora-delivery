-- 0043_courses_state_aware_rls.down.sql — restore the plain tenant-isolation
-- policy on `courses` (pre-B2.4). Matches 0001_initial.sql.
DROP POLICY IF EXISTS tenant_isolation ON courses;
CREATE POLICY tenant_isolation ON courses
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
