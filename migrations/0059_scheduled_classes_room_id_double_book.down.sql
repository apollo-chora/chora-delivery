-- =============================================================================
-- chora-delivery : 0059_scheduled_classes_room_id_double_book.down.sql
--
-- Reverses 0059 (CHO-2299).
--
-- WARNING: dropping the constraint RE-OPENS the room double-book evasion on
-- /r/scheduling that ADR-237 O3 closed. The columns are kept: room_id carries
-- real bookings that only exist here, and dropping it would lose them (the JSONB
-- snapshot is the source of truth for the value, but the extracted column is
-- what the gate reads). Drop the gate only, and only deliberately.
-- =============================================================================

BEGIN;

ALTER TABLE scheduled_classes
    DROP CONSTRAINT IF EXISTS scheduled_classes_no_room_id_double_book;

DROP INDEX IF EXISTS idx_scheduled_classes_room;

COMMIT;
