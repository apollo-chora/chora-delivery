-- 004_create_jam_boards.up.sql
-- JamBoard + JamBoardEntry.
-- tenant_id and training_session_id are cross-context UUID references (no FK per DDD rule #3).

CREATE TABLE jam_boards (
    id                  UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID          NOT NULL,
    training_session_id UUID,         -- cross-context reference to training-admin
    title               VARCHAR(200)  NOT NULL,
    description         TEXT          DEFAULT '',
    status              board_status  NOT NULL DEFAULT 'open',
    entry_count         INTEGER       NOT NULL DEFAULT 0,
    created_by_gcid     UUID          NOT NULL,
    created_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX idx_jam_boards_tenant ON jam_boards (tenant_id);
CREATE INDEX idx_jam_boards_training ON jam_boards (training_session_id) WHERE training_session_id IS NOT NULL;

CREATE TRIGGER trg_jam_boards_updated_at
    BEFORE UPDATE ON jam_boards
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- RLS (DP-02)
ALTER TABLE jam_boards ENABLE ROW LEVEL SECURITY;
ALTER TABLE jam_boards FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON jam_boards
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- JamBoardEntry — individual entries on a jamboard.
CREATE TABLE jam_board_entries (
    id              UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id        UUID          NOT NULL REFERENCES jam_boards(id) ON DELETE CASCADE,
    tenant_id       UUID          NOT NULL,
    entry_type      entry_type    NOT NULL,
    content         TEXT          NOT NULL,
    color           VARCHAR(7),   -- hex color code for sticky notes
    position_x      REAL,
    position_y      REAL,
    created_by_gcid UUID          NOT NULL,
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX idx_jam_board_entries_board ON jam_board_entries (board_id);
CREATE INDEX idx_jam_board_entries_tenant ON jam_board_entries (tenant_id);

-- RLS
ALTER TABLE jam_board_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE jam_board_entries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON jam_board_entries
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
