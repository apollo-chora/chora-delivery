-- =============================================================================
-- chora-delivery : 0052_offering_sessions_room_id_double_book.down.sql
--
-- Reverse SP2's EXPAND — drop the room_id EXCLUDE + the room_id column. The
-- slice-1 (mig 0050) `room` string column and its EXCLUDE
-- (offering_sessions_no_room_double_book) were NOT touched by 0052.up, so this
-- down leaves them intact — the DB returns to exactly the slice-1 (0050/0051)
-- state.
-- =============================================================================

BEGIN;

ALTER TABLE offering_sessions
    DROP CONSTRAINT IF EXISTS offering_sessions_no_room_id_double_book;

ALTER TABLE offering_sessions
    DROP COLUMN IF EXISTS room_id;

COMMIT;
