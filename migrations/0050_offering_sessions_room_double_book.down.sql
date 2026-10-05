-- =============================================================================
-- chora-delivery : 0050_offering_sessions_room_double_book.down.sql
--
-- Reverse ADR-237 / CHO-2191. Drops the EXCLUDE gate + the extracted columns.
-- The JSONB `data` snapshot retains room + ends_at, so no data is lost. The
-- btree_gist extension is left installed (may be shared by future constraints).
-- =============================================================================

ALTER TABLE offering_sessions
    DROP CONSTRAINT IF EXISTS offering_sessions_no_room_double_book;

ALTER TABLE offering_sessions
    DROP COLUMN IF EXISTS room,
    DROP COLUMN IF EXISTS ends_at;
