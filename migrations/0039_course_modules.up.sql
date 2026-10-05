-- =============================================================================
-- chora-delivery : 0039_course_modules.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : WS-A W7 course-structure layer
-- ADR           : ADR-190 (ONE Content Delivery context; Course structure)
--
-- Purpose:
--   Persist the Module aggregate — the "W7 course structure layer" that GROUPS
--   the flat course_content_items (0029) of a Course into named, ordered
--   modules, each carrying a completion Requirement. A Module is its own
--   aggregate root; a course_module_items row references a course_content_items
--   row BY UUID (content_item_id) with NO FK — a cross-aggregate reference per
--   .claude/rules/ddd-enforcement.md Invariant #1/#3 (collections query content;
--   they never own it). Intra-chora_delivery only; cross-DB queries FORBIDDEN.
--
--   course_module_items.module_id → course_modules(id) IS an FK: that edge is
--   WITHIN the aggregate (child → root), so referential integrity is enforced.
--
-- ROW LEVEL SECURITY (tenant_isolation): both tables are admin CRUD, always
--   tenant-scoped; pg.ModuleRepo calls rls.ApplySession (SET LOCAL
--   chora.tenant_id) before every query. RLS is ENABLED for defence-in-depth.
--   The USING/WITH CHECK pair uses the NULLIF-safe cast (mig-0019 lesson: a
--   pooled conn can leave the GUC at '' and ''::uuid throws 22P02) and blocks
--   writing a row into a foreign tenant — the 0031_offerings write-path form.
--
-- Positions: item positions are kept dense by the domain aggregate (AddItem
--   appends, RemoveItem re-compacts, Reorder permutes) and are NOT DB-unique so
--   the reorder write path stays a simple per-item UPDATE. Module position
--   within a course IS covered by a partial UNIQUE index — modules append-only
--   (dense MAX+1 on Create), so a concurrent duplicate fails loud instead of
--   silently colliding.
--
-- Grants: explicit per-DB app-role grants (0010_assessments form). These MUST
--   target chora_delivery_app_rw / chora_delivery_app_ro — a global app_rw role
--   does not exist and would abort + roll back the migration. (The persistent
--   ALTER DEFAULT PRIVILEGES in 9999_grant_app_roles.sql also covers these; the
--   explicit grants are belt-and-suspenders + self-documenting.)
-- =============================================================================

-- ----------------------------------------------------------------------------
-- course_modules — the aggregate root. Requirement is a value object stored
-- INLINE (1:1, no independent identity/lifecycle): kind + threshold columns for
-- keying/filtering, required_item_ids as a JSONB array.
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS course_modules (
    id                            UUID PRIMARY KEY,
    tenant_id                     UUID        NOT NULL,
    course_id                     UUID        NOT NULL,
    title                         TEXT        NOT NULL,
    position                      INTEGER     NOT NULL,
    requirement_kind              TEXT        NOT NULL DEFAULT 'all_items',
    requirement_threshold_n       INTEGER     NOT NULL DEFAULT 0,
    requirement_required_item_ids JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at                    TIMESTAMPTZ
);

-- Ordered active modules within a course; also the UNIQUE guard on
-- (tenant, course, position) so a concurrent dense-append collision fails loud.
CREATE UNIQUE INDEX IF NOT EXISTS uq_course_modules_course_position
    ON course_modules (tenant_id, course_id, position)
    WHERE deleted_at IS NULL;

ALTER TABLE course_modules ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON course_modules
    USING (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

-- ----------------------------------------------------------------------------
-- course_module_items — child membership entries (Module → ContentItem by UUID).
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS course_module_items (
    item_id         UUID PRIMARY KEY,
    module_id       UUID        NOT NULL REFERENCES course_modules (id),
    tenant_id       UUID        NOT NULL,
    content_item_id UUID        NOT NULL,
    position        INTEGER     NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

-- Ordered read of a module's active items (Get / List → ORDER BY position).
CREATE INDEX IF NOT EXISTS idx_course_module_items_module
    ON course_module_items (tenant_id, module_id, position)
    WHERE deleted_at IS NULL;

-- At most one ACTIVE membership of a given content item per module (mirror the
-- course_content_items (kind,ref) uniqueness). Soft-deleted rows excluded so a
-- removed item can be re-added.
CREATE UNIQUE INDEX IF NOT EXISTS uq_course_module_items_content
    ON course_module_items (module_id, content_item_id)
    WHERE deleted_at IS NULL;

ALTER TABLE course_module_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON course_module_items
    USING (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid);

-- ----------------------------------------------------------------------------
-- Per-DB app-role grants (MUST be chora_delivery_app_rw / _app_ro).
-- ----------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON course_modules      TO chora_delivery_app_rw;
GRANT SELECT, INSERT, UPDATE, DELETE ON course_module_items TO chora_delivery_app_rw;
GRANT SELECT                         ON course_modules      TO chora_delivery_app_ro;
GRANT SELECT                         ON course_module_items TO chora_delivery_app_ro;
