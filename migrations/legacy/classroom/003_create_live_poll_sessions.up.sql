-- 003_create_live_poll_sessions.up.sql
-- LivePollSession + PollVote.
-- tenant_id and training_session_id are cross-context UUID references (no FK per DDD rule #3).

CREATE TABLE live_poll_sessions (
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID         NOT NULL,
    training_session_id UUID,        -- cross-context reference to training-admin
    question            VARCHAR(500) NOT NULL,
    options             JSONB        NOT NULL DEFAULT '[]'::jsonb,
    poll_type           poll_type    NOT NULL DEFAULT 'single_choice',
    status              poll_status  NOT NULL DEFAULT 'open',
    anonymous           BOOLEAN      NOT NULL DEFAULT false,
    vote_count          INTEGER      NOT NULL DEFAULT 0,
    created_by_gcid     UUID         NOT NULL,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_live_poll_sessions_tenant ON live_poll_sessions (tenant_id);
CREATE INDEX idx_live_poll_sessions_training ON live_poll_sessions (training_session_id) WHERE training_session_id IS NOT NULL;

CREATE TRIGGER trg_live_poll_sessions_updated_at
    BEFORE UPDATE ON live_poll_sessions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- RLS (DP-02)
ALTER TABLE live_poll_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE live_poll_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON live_poll_sessions
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- PollVote — records individual votes.
CREATE TABLE poll_votes (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    poll_session_id         UUID        NOT NULL REFERENCES live_poll_sessions(id) ON DELETE CASCADE,
    tenant_id               UUID        NOT NULL,
    gcid                    UUID        NOT NULL,
    selected_option_indices JSONB       NOT NULL DEFAULT '[]'::jsonb,
    cast_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One vote per GCID per poll session.
CREATE UNIQUE INDEX uq_poll_votes_poll_gcid
    ON poll_votes (poll_session_id, gcid);

CREATE INDEX idx_poll_votes_poll ON poll_votes (poll_session_id);
CREATE INDEX idx_poll_votes_tenant ON poll_votes (tenant_id);

-- RLS
ALTER TABLE poll_votes ENABLE ROW LEVEL SECURITY;
ALTER TABLE poll_votes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON poll_votes
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
