-- Down: drop the cert-definition columns.
ALTER TABLE courses
    DROP COLUMN IF EXISTS cert_enabled,
    DROP COLUMN IF EXISTS cert_type,
    DROP COLUMN IF EXISTS cert_passing_score_pct,
    DROP COLUMN IF EXISTS cert_require_all_content;
