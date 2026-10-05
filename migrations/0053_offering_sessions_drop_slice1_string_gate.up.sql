-- =============================================================================
-- chora-delivery : 0053_offering_sessions_drop_slice1_string_gate.up.sql
--
-- CONTRACT half of the CHO-2191 room double-book cutover (mig 0052 was the
-- EXPAND). Applied AFTER the SP2 delivery image is deployed — that image's
-- upsert writes `room_id` and NEVER the legacy free-text `room` column, so
-- slice-1's `room`-string EXCLUDE (offering_sessions_no_room_double_book, mig
-- 0050) is now fully superseded by the ratified room_id EXCLUDE
-- (offering_sessions_no_room_id_double_book, mig 0052). Drop the superseded gate
-- so exactly ONE double-book constraint remains — the room_id one.
--
-- The now-inert `room` column is intentionally KEPT: it is a nullable TEXT with
-- no reader (the display name lives losslessly in the JSONB `data` snapshot,
-- data->>'Room'), a column DROP is refused by the migration destructive-guard,
-- and its removal carries no value beyond cosmetics. A trivial future cleanup
-- may drop it once the guard allow-lists it.
--
-- Date : 2026-07-16
-- =============================================================================

BEGIN;

ALTER TABLE offering_sessions
    DROP CONSTRAINT IF EXISTS offering_sessions_no_room_double_book;

COMMIT;
