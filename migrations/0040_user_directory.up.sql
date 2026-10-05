-- 0040_user_directory.up.sql — tenant-agnostic GCID → display-name projection
-- (Q3 name projection, delivery side).
--
-- Purpose:
--   chora-delivery renders course rosters with each learner's display name, but
--   the name is owned by chora-identity. Cross-DB queries are FORBIDDEN across
--   the 13-DB topology (.claude/rules/ddd-enforcement.md HARD RULE #1), so
--   chora-delivery cannot JOIN chora_identity. It instead keeps a local
--   read-model, updated over Pub/Sub from chora.identity.user.profile_updated.v1
--   (internal/adapter/events/identity_profile_subscriber.go). The course roster
--   READ VIEW then LEFT-JOIN-stitches this table onto its RLS-scoped enrolment
--   rows (internal/adapter/inmem/roster_repo.go via UserDirectoryPort.LookupNames).
--
-- TENANT-AGNOSTIC + NO RLS — deliberate, justified:
--   The key is the GLOBAL GCID (opaque UUIDv7, no tenant context embedded); a
--   display name is not tenant-scoped (one person, one name, portable across
--   tenants). So there is no tenant_id column and no RLS policy. Isolation is
--   preserved at the JOIN boundary: a name is only ever surfaced by stitching
--   against the roster's OWN RLS-scoped rows, so a caller resolves names only
--   for GCIDs already visible in their own tenant's roster. RLS here would be
--   meaningless (no tenant column to scope on) and would break the global
--   lookup. This is NOT one of the two ADR-scoped RLS-bypass surfaces
--   (ADR-165 / ADR-184) — it is a table that never carried tenant data.
--
-- Last-writer-wins: reconciliation on updated_at happens in the app
--   (pg.SQLUpsertUserDirectory: DO UPDATE ... WHERE user_directory.updated_at
--   < EXCLUDED.updated_at), so an out-of-order redelivery cannot clobber a
--   newer name and an equal-timestamp replay no-ops. No soft-delete column —
--   this is a derived projection, not a system of record; identity's closure
--   saga pseudonymises the source name and a subsequent profile_updated
--   projects the tokenised value here.
--
-- Grants: explicit per-DB app-role grants (0039_course_modules form). These
--   MUST target chora_delivery_app_rw / chora_delivery_app_ro — a global app_rw
--   role does not exist in chora_delivery and would abort + roll back the whole
--   migration. (ALTER DEFAULT PRIVILEGES in 9999_grant_app_roles.sql also
--   covers this table; the explicit grants are belt-and-suspenders +
--   self-documenting.) DELETE is deliberately withheld — never hard-delete.

CREATE TABLE IF NOT EXISTS user_directory (
    gcid         UUID        PRIMARY KEY,
    display_name TEXT        NOT NULL DEFAULT '',
    email        TEXT,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-DB app-role grants (MUST be chora_delivery_app_rw / _app_ro). The upsert
-- path needs SELECT/INSERT/UPDATE; DELETE is intentionally not granted.
GRANT SELECT, INSERT, UPDATE ON user_directory TO chora_delivery_app_rw;
GRANT SELECT                 ON user_directory TO chora_delivery_app_ro;
