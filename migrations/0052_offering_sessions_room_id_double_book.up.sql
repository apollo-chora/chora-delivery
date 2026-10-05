-- =============================================================================
-- chora-delivery : 0052_offering_sessions_room_id_double_book.up.sql
--
-- Ratified CHO-2191 / ADR-237 (SP2) — introduce the ratified `room_id` room
-- double-book gate (a stable Room aggregate identity, chora_delivery.rooms, mig
-- 0051), superseding slice-1's free-text `room` STRING key. A free-text key let
-- a typo ("Room A " vs "Room A") mint a phantom room and re-open the very clash
-- it claimed to close — "a worse failure, because it looks fixed" (owner ruling
-- 2026-07-14).
--
-- Shape = EXPAND (of a gapless expand/contract cutover):
--   * ADD room_id + a room_id-keyed EXCLUDE (distinct name).
--   * KEEP slice-1's `room` string column AND its EXCLUDE
--     (offering_sessions_no_room_double_book, mig 0050).
--
-- Why keep both, rather than drop the string gate here: this migration is
-- applied while the slice-1 image (chora-delivery:e9a909c6a) is still the running
-- pod. That image's upsert writes `room` (not room_id), so its rows are keyed by
-- the STRING gate. Dropping the string gate now would leave every old-image write
-- UN-protected (room_id NULL ⇒ exempt from the new gate) until the SP2 image
-- ships — a transient double-book hole, the exact "looks fixed but isn't" failure
-- this story exists to kill. So both gates run concurrently, each protecting its
-- own era, with ZERO protection gap:
--   * old-image rows: room=<str>, room_id NULL  → string gate active, room_id gate exempt.
--   * SP2-image rows: room NULL,  room_id=<uuid>→ string gate exempt (room NULL),
--                                                  room_id gate active.
-- The CONTRACT half (drop the string EXCLUDE + the now-vestigial `room` column)
-- runs as mig 0053 at SP4, AFTER the SP2 image is confirmed rolled out. The
-- display name survives losslessly in the JSONB `data` snapshot (data->>'Room').
--
-- btree_gist supplies the '=' operator class for the scalar tenant_id + room_id
-- columns alongside the range '&&' operator (already installed by mig 0050;
-- IF NOT EXISTS is idempotent). No new GRANTs: offering_sessions already carries
-- app_rw/app_ro grants — ADD COLUMN inherits table-level privileges.
--
-- Date : 2026-07-16
-- =============================================================================

BEGIN;

CREATE EXTENSION IF NOT EXISTS btree_gist;

-- The ratified stable key. Nullable: a roomless session leaves it NULL and is
-- exempt from the gate (an unbooked session is not a shared resource).
ALTER TABLE offering_sessions
    ADD COLUMN IF NOT EXISTS room_id UUID;

-- Backfill room_id from the lossless JSONB snapshot (PascalCase Go field; blank
-- ⇒ NULL). Today every row is roomless-by-id (slice-1 never populated RoomID),
-- so this is a no-op — kept for idempotency + correctness.
UPDATE offering_sessions
   SET room_id = NULLIF(data->>'RoomID', '')::uuid
 WHERE room_id IS NULL;

-- The ratified room_id-keyed gate: no two ACTIVE, roomed sessions in the same
-- (tenant, room_id) may overlap in time. Half-open '[)' ⇒ abutting sessions
-- (prev.ends_at == next.starts_at) do NOT clash. Partial predicate: roomless
-- (room_id NULL) and soft-deleted (cancelled) sessions are exempt. tenant_id is
-- IN the key — EXCLUDE constraints are not RLS-filtered; explicit beats
-- correct-by-luck. Distinct name from the slice-1 string EXCLUDE so both may
-- coexist during the cutover.
ALTER TABLE offering_sessions
    ADD CONSTRAINT offering_sessions_no_room_id_double_book
    EXCLUDE USING gist (
        tenant_id                           WITH =,
        room_id                             WITH =,
        tstzrange(starts_at, ends_at, '[)') WITH &&
    ) WHERE (deleted_at IS NULL AND room_id IS NOT NULL);

COMMIT;
