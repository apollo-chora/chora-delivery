-- 0042_course_visibility_state_gate.down.sql — reverse the visibility column +
-- restore the pre-state-gate view. The state backfill (DRAFT→PUBLISHED for the
-- public seed courses) is NOT reversed — the prior per-row state is not recorded
-- and PUBLISHED is the correct state for those public courses anyway.

-- Restore the view WITHOUT the state gate (public + not-deleted only).
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

GRANT SELECT ON public_courses_catalog TO chora_delivery_app_rw, chora_delivery_app_ro;

ALTER TABLE courses DROP COLUMN IF EXISTS visibility;
