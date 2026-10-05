-- =============================================================================
-- chora-delivery : 0059_scheduled_classes_room_id_double_book.up.sql
--
-- CHO-2299, closing ADR-237 O3 - extend the ratified room_id-keyed double-book
-- gate to the SECOND sanctioned scheduled-meeting model.
--
-- ADR-236 D2 designates ScheduledClass and OfferingSession as the two sanctioned
-- durable scheduled-meeting models. ADR-237 gated only offering_sessions
-- (mig 0052), so the ratified no-double-book invariant was EVADABLE: booking the
-- same room for the same window through /r/scheduling was accepted, because
-- scheduled_classes had neither a room_id nor an ends_at column and no gate was
-- even representable there.
--
-- Mirrors 0052 exactly: same half-open range, same partial predicate, same
-- tenant-in-key reasoning. Two columns are extracted from the lossless JSONB
-- snapshot (mig 0025 extracted only id/tenant_id/iso_year/iso_week/starts_at).
--
-- NOT NULL is deliberately NOT applied to ends_at: a legacy row whose snapshot
-- lacks EndsAt must not abort the migration. The EXCLUDE predicate requires both
-- columns to be present, so such a row is simply exempt rather than fatal.
--
-- NO name-based backfill of room_id. Auto-provisioning a Room from a free-text
-- name was explicitly REJECTED by the owner (2026-07-16): capacity would be
-- unknown, killing the over-capacity check, and 'Room A ' would silently fork
-- from 'Room A', minting a second room and re-opening the very clash this gate
-- closes, "a worse failure, because it looks fixed". Legacy free-text rows
-- therefore land with room_id NULL and stay exempt until a human rebooks them
-- against a real Room. The legacy name survives losslessly in data->>'Room'.
--
-- Date : 2026-07-19
-- =============================================================================

BEGIN;

CREATE EXTENSION IF NOT EXISTS btree_gist;

-- The ratified stable key. Nullable: a roomless class leaves it NULL and is
-- exempt from the gate (an unbooked class is not a shared resource).
ALTER TABLE scheduled_classes
    ADD COLUMN IF NOT EXISTS room_id UUID;

-- ends_at was never extracted (mig 0025 kept only starts_at), so the range side
-- of the gate has nothing to read without this.
ALTER TABLE scheduled_classes
    ADD COLUMN IF NOT EXISTS ends_at TIMESTAMPTZ;

-- Backfill from the lossless JSONB snapshot (PascalCase Go fields).
-- room_id: blank or absent ⇒ NULL. Every legacy row is roomless-by-id because
-- ScheduledClass had no RoomID field before CHO-2299, so this is effectively a
-- no-op; kept for idempotency + correctness.
UPDATE scheduled_classes
   SET room_id = NULLIF(data->>'RoomID', '')::uuid
 WHERE room_id IS NULL;

-- ends_at: RFC3339 in the snapshot. Guarded so a malformed or absent value
-- leaves NULL rather than aborting the whole migration.
UPDATE scheduled_classes
   SET ends_at = (data->>'EndsAt')::timestamptz
 WHERE ends_at IS NULL
   AND data->>'EndsAt' IS NOT NULL
   AND data->>'EndsAt' ~ '^\d{4}-\d{2}-\d{2}T';

-- The gate: no two ACTIVE, roomed classes in the same (tenant, room_id) may
-- overlap in time. Half-open '[)' ⇒ abutting classes (prev.ends_at ==
-- next.starts_at) do NOT clash. Partial predicate exempts roomless rows,
-- soft-deleted rows, and legacy rows with no extracted ends_at. tenant_id is IN
-- the key: EXCLUDE constraints are not RLS-filtered, and explicit beats
-- correct-by-luck.
ALTER TABLE scheduled_classes
    ADD CONSTRAINT scheduled_classes_no_room_id_double_book
    EXCLUDE USING gist (
        tenant_id                           WITH =,
        room_id                             WITH =,
        tstzrange(starts_at, ends_at, '[)') WITH &&
    ) WHERE (deleted_at IS NULL AND room_id IS NOT NULL AND ends_at IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_scheduled_classes_room
    ON scheduled_classes (tenant_id, room_id)
    WHERE deleted_at IS NULL AND room_id IS NOT NULL;

COMMIT;
