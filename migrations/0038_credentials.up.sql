-- 0038_credentials.up.sql — operator-curated Credential-with-competencies
-- catalogue table (ADR-216 WS-1, D1/D2; Learner-Sovereign Discovery redesign).
--
-- A Credential (e.g. PMP) is an operator/awarding-body-defined catalogue entity
-- carrying a structured set of Competencies (the shared-vocabulary seed, D2).
-- It is DECOUPLED from courses (no course_id / offering_id — a Credential
-- references nothing) and learner-immutable (D1 — enforced at the API: no
-- learner write path). This is the CATALOGUE entity only; the opaque issued-cert
-- model (the certificates HMAC-signed record) is UNAFFECTED — no existing cert
-- row is migrated or altered.
--
-- The whole Credential — including the Competencies child collection — is stored
-- as a lossless JSONB snapshot in `data` (the offerings / offering_sessions
-- pattern; competencies ride in the aggregate JSONB, no own table, no FK per
-- ddd-enforcement invariant #3). Extracted columns exist only for RLS scoping
-- (tenant_id) and catalogue ordering (title).
--
-- Storage + RLS mirror 0036_offering_sessions / 0037_offering_attendance_records
-- (NULLIF-safe cast + WITH CHECK — the mig-0019 pooled-conn lesson). RLS is
-- ENABLED (not FORCEd): app_rw / app_ro are NOBYPASSRLS + non-owners so ENABLE
-- fully isolates them, while the migrate role (table owner, BYPASSRLS) must stay
-- exempt to run cross-tenant backfills — the deliberate chora_delivery
-- convention (see 9999_grant_app_roles.sql). No per-table grants: app_rw /
-- app_ro inherit via ALTER DEFAULT PRIVILEGES in 9999_grant_app_roles.sql
-- (sorts last). Soft-delete via deleted_at; NEVER hard-delete.

CREATE TABLE IF NOT EXISTS credentials (
    id          UUID PRIMARY KEY,
    tenant_id   UUID        NOT NULL,
    title       TEXT        NOT NULL,
    data        JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_credentials_tenant_title
    ON credentials (tenant_id, title) WHERE deleted_at IS NULL;

ALTER TABLE credentials ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON credentials
    USING      (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);
