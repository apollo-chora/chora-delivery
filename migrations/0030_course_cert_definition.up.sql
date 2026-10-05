-- 0030_course_cert_definition — the certificate a Course awards on completion
-- (CHO-1795, L3). Additive nullable/defaulted columns on courses; the
-- CertDefinition value object on the Course aggregate persists here and is
-- CONSUMED by the issuance chain.
--
-- Additive + safe for the currently-deployed code: the existing SaveCJ2 INSERT
-- lists explicit columns, so the new defaulted columns are inert until the
-- cert-aware code is activated (COURSE_CERT_PG_ENABLED). RLS + grants are
-- inherited from the existing `courses` policy + 9999_grant_app_roles.sql.

ALTER TABLE courses
    ADD COLUMN IF NOT EXISTS cert_enabled             BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS cert_type                TEXT,
    ADD COLUMN IF NOT EXISTS cert_passing_score_pct   INTEGER,
    ADD COLUMN IF NOT EXISTS cert_require_all_content  BOOLEAN NOT NULL DEFAULT TRUE;
