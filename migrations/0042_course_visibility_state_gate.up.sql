-- =============================================================================
-- chora-delivery : 0042_course_visibility_state_gate.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Relates to    : Sub-phase B security hardening (ADR-226 follow-on) — B2.1
--                 view-gate + B2.3 persist real visibility.
--
-- Three changes, all on the `courses` table / public_courses_catalog view:
--
-- 1. BACKFILL (data) — legitimise the intentionally-public seed/demo/E2E courses
--    that were made public=true via the catalogue seed path (NewPublicCourse) but
--    never transitioned to state=PUBLISHED (they defaulted to DRAFT). Confirmed set
--    (2026-07-08): 5 Mr. Chen tenant-22222222 demo-catalogue courses (incl.
--    33333333 "Certified Scrum Product Owner Prep", the canonical public-catalogue
--    course the 0006 view's own tests reference) + 1 E2E test course. These ARE
--    meant to be publicly discoverable, so PUBLISHED is the correct state. Running
--    this BEFORE the view-gate keeps them visible.
--
-- 2. B2.3 — persist real `visibility`. Until now the table had only the `public`
--    bool, so VisibilityPrivate collapsed to `public=false` and read back as
--    tenant_only (catalogue.scanTenantCourseRow coerced from public). Add a
--    first-class `visibility` column (private|tenant_only|public) so private is
--    persisted. Backfill from the existing `public` denorm — private is
--    unrecoverable for existing rows (never persisted), so they seed tenant_only;
--    GOING FORWARD the catalogue Save writes the real scope. `public` stays as the
--    denorm the cross-tenant view filters on (public == visibility='public').
--
-- 3. B2.1 view-gate — the cross-tenant `public_courses_catalog` view now ALSO
--    requires state='PUBLISHED' (defence-in-depth over the `public` flag). The
--    public flag and the CJ#2 state FSM were decoupled (the catalogue SetVisibility
--    path UPSERTs `public` independent of state), so a DRAFT course flipped public
--    could leak cross-tenant. With the backfill above, no legitimate public course
--    is hidden; a future draft-flip can never leak through the view.
-- =============================================================================

-- 1. Backfill: intentionally-public courses → PUBLISHED (idempotent).
UPDATE courses
SET state = 'PUBLISHED', updated_at = now()
WHERE public = true AND deleted_at IS NULL AND state <> 'PUBLISHED';

-- 2. B2.3: first-class visibility column + backfill from the public denorm.
ALTER TABLE courses
    ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'tenant_only'
        CHECK (visibility IN ('private', 'tenant_only', 'public'));

UPDATE courses
SET visibility = CASE WHEN public THEN 'public' ELSE 'tenant_only' END
WHERE visibility IS DISTINCT FROM (CASE WHEN public THEN 'public' ELSE 'tenant_only' END);

-- 3. B2.1 view-gate: cross-tenant discovery requires public=true AND PUBLISHED.
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
  AND state = 'PUBLISHED'
  AND deleted_at IS NULL;

GRANT SELECT ON public_courses_catalog TO chora_delivery_app_rw, chora_delivery_app_ro;
