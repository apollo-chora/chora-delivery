-- =============================================================================
-- chora-delivery : 0050_offering_sessions_room_double_book.up.sql
--
-- ADR-237 / CHO-2191 — DB-enforced room double-book gate for the R+ Four-Mode
-- scheduler. Promote `room` + `ends_at` to real, queryable columns on
-- offering_sessions and forbid two ACTIVE sessions from sharing a (tenant, room)
-- over an overlapping time window, via a GiST EXCLUDE constraint (declarative,
-- index-backed, un-bypassable across pods / races / replays — the CHO-2157
-- lesson). Before this, room + ends_at lived ONLY inside the JSONB `data`
-- snapshot (mig 0036), so the invariant was unrepresentable: two sessions in
-- the same room + window both returned 201 (live-proven, CHO-2191).
--
-- btree_gist supplies the '=' operator class for the scalar tenant_id + room
-- columns alongside the range '&&' operator (precedent: uuid-ossp / pgcrypto in
-- mig 0001). The JSONB `data` stays the lossless source of truth; these columns
-- are extracted alongside it (the offerings / scheduled_classes pattern).
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS btree_gist;

ALTER TABLE offering_sessions
    ADD COLUMN IF NOT EXISTS room    TEXT,
    ADD COLUMN IF NOT EXISTS ends_at TIMESTAMPTZ;

-- Backfill the extracted columns from the lossless JSONB snapshot. The struct
-- carries NO json tags, so JSONB keys are the PascalCase Go field names; EndsAt
-- is an RFC3339 string.
UPDATE offering_sessions
   SET room    = NULLIF(data->>'Room', ''),
       ends_at = NULLIF(data->>'EndsAt', '')::timestamptz
 WHERE ends_at IS NULL;

-- No two active, roomed sessions in the same room may overlap in time.
-- Half-open ranges '[)' ⇒ abutting sessions (prev.ends_at == next.starts_at) do
-- NOT clash. Blank/absent room or a missing ends_at is exempt — an unbooked
-- session cannot double-book a room. A soft-deleted (cancelled) session frees
-- its slot.
ALTER TABLE offering_sessions
    ADD CONSTRAINT offering_sessions_no_room_double_book
    EXCLUDE USING gist (
        tenant_id                     WITH =,
        room                          WITH =,
        tstzrange(starts_at, ends_at) WITH &&
    ) WHERE (deleted_at IS NULL AND room IS NOT NULL AND room <> '' AND ends_at IS NOT NULL);
