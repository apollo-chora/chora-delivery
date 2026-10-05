-- =============================================================================
-- chora-delivery : 0053_offering_sessions_drop_slice1_string_gate.down.sql
--
-- Restore the slice-1 (mig 0050) free-text `room` string EXCLUDE verbatim. NB:
-- re-adding it FAILS if any two active same-`room` sessions overlap; soft-delete
-- the later violator first (as slice-1's apply required). The `room` column was
-- never dropped, so it is available for the constraint.
-- =============================================================================

BEGIN;

ALTER TABLE offering_sessions
    ADD CONSTRAINT offering_sessions_no_room_double_book
    EXCLUDE USING gist (
        tenant_id                     WITH =,
        room                          WITH =,
        tstzrange(starts_at, ends_at) WITH &&
    ) WHERE (deleted_at IS NULL AND room IS NOT NULL AND room <> '' AND ends_at IS NOT NULL);

COMMIT;
