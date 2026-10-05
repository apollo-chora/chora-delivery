-- 0029_course_content_items — persist the heterogeneous course curriculum
-- (CHO-1612 / CHO-1794, L3). Replaces the in-memory CourseContentRepo so
-- authored content survives pod restart and is tenant-isolated via RLS.
--
-- Aggregate: CourseContent (identified by course_id) is the ordered collection
-- of typed ContentItem children (atom/video/youtube/document/live_classroom/
-- assessment). One row per item. Soft-delete only (deleted_at).
--
-- RLS scopes per tenant_id (chora.tenant_id session var), matching the
-- `courses` table policy from 0014. Grants are inherited from
-- 9999_grant_app_roles.sql (GRANT ... ON ALL TABLES + ALTER DEFAULT
-- PRIVILEGES — this table, created by the migrate role, picks them up).

CREATE TABLE IF NOT EXISTS course_content_items (
    item_id    UUID PRIMARY KEY,
    course_id  UUID NOT NULL,
    tenant_id  UUID NOT NULL,
    kind       TEXT NOT NULL,
    ref        TEXT NOT NULL,
    title      TEXT NOT NULL,
    position   INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);

-- Ordered read of a course's active curriculum (Get → ORDER BY position).
CREATE INDEX IF NOT EXISTS idx_course_content_items_course
    ON course_content_items (tenant_id, course_id, position)
    WHERE deleted_at IS NULL;

-- Mirror the domain ErrDuplicateItem invariant: at most one ACTIVE item per
-- (tenant, course, kind, ref). Soft-deleted rows are excluded so a removed
-- item can be re-added.
CREATE UNIQUE INDEX IF NOT EXISTS uq_course_content_items_kindref
    ON course_content_items (tenant_id, course_id, kind, ref)
    WHERE deleted_at IS NULL;

ALTER TABLE course_content_items ENABLE ROW LEVEL SECURITY;

-- tenant_isolation: reads + writes filtered by the chora.tenant_id session
-- var. WITH CHECK blocks inserting/updating a row into a foreign tenant.
CREATE POLICY tenant_isolation ON course_content_items
    USING (tenant_id = current_setting('chora.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('chora.tenant_id', true)::uuid);
