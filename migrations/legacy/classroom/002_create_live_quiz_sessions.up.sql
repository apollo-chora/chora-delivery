-- 002_create_live_quiz_sessions.up.sql
-- LiveQuizSession aggregate root + QuizParticipant + quiz_answers.
-- tenant_id and training_session_id are cross-context UUID references (no FK per DDD rule #3).

CREATE TABLE live_quiz_sessions (
    id                     UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id              UUID          NOT NULL,
    training_session_id    UUID,         -- cross-context reference to training-admin
    title                  VARCHAR(200)  NOT NULL,
    status                 quiz_status   NOT NULL DEFAULT 'waiting',
    questions              JSONB         NOT NULL DEFAULT '[]'::jsonb,
    current_question_index INTEGER       NOT NULL DEFAULT -1,
    participant_count      INTEGER       NOT NULL DEFAULT 0,
    time_limit_seconds     INTEGER       NOT NULL DEFAULT 30,
    created_by_gcid        UUID          NOT NULL,
    created_at             TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX idx_live_quiz_sessions_tenant ON live_quiz_sessions (tenant_id);
CREATE INDEX idx_live_quiz_sessions_training ON live_quiz_sessions (training_session_id) WHERE training_session_id IS NOT NULL;
CREATE INDEX idx_live_quiz_sessions_status ON live_quiz_sessions (tenant_id, status);

CREATE TRIGGER trg_live_quiz_sessions_updated_at
    BEFORE UPDATE ON live_quiz_sessions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- RLS (DP-02)
ALTER TABLE live_quiz_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE live_quiz_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON live_quiz_sessions
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- QuizParticipant — tracks individual learner scores within a quiz session.
CREATE TABLE quiz_participants (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    quiz_session_id     UUID        NOT NULL REFERENCES live_quiz_sessions(id) ON DELETE CASCADE,
    tenant_id           UUID        NOT NULL,
    gcid                UUID        NOT NULL,
    display_name        VARCHAR(100) NOT NULL DEFAULT '',
    total_points        INTEGER     NOT NULL DEFAULT 0,
    correct_count       INTEGER     NOT NULL DEFAULT 0,
    answered_count      INTEGER     NOT NULL DEFAULT 0,
    avg_time_ms         INTEGER     NOT NULL DEFAULT 0,
    last_answer_correct BOOLEAN     NOT NULL DEFAULT false,
    joined_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One participant per GCID per quiz session.
CREATE UNIQUE INDEX uq_quiz_participants_quiz_gcid
    ON quiz_participants (quiz_session_id, gcid);

CREATE INDEX idx_quiz_participants_quiz ON quiz_participants (quiz_session_id);
CREATE INDEX idx_quiz_participants_tenant ON quiz_participants (tenant_id);

CREATE TRIGGER trg_quiz_participants_updated_at
    BEFORE UPDATE ON quiz_participants
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- RLS
ALTER TABLE quiz_participants ENABLE ROW LEVEL SECURITY;
ALTER TABLE quiz_participants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON quiz_participants
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- Quiz answers — append-only record of individual answer submissions.
CREATE TABLE quiz_answers (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    participant_id        UUID        NOT NULL REFERENCES quiz_participants(id) ON DELETE CASCADE,
    question_index        INTEGER     NOT NULL,
    selected_option_index INTEGER     NOT NULL,
    correct               BOOLEAN     NOT NULL,
    points_earned         INTEGER     NOT NULL DEFAULT 0,
    time_taken_ms         INTEGER     NOT NULL DEFAULT 0,
    submitted_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Prevent duplicate answers: one answer per participant per question.
CREATE UNIQUE INDEX uq_quiz_answers_participant_question
    ON quiz_answers (participant_id, question_index);

CREATE INDEX idx_quiz_answers_participant ON quiz_answers (participant_id);
