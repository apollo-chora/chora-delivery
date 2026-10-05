// live_quiz_producer.go — ADR-168 classroom-realtime PRODUCER side.
//
// The WS consumer side (ws.Broker + WS handlers) and the realtime adapters
// (realtime.Backplane/TallyStore/LeaderboardStore) existed but nothing fed
// them. This file is the missing producer:
//
//   - handleAdvanceQuestion (instructor) opens a question — sets the per-session
//     question-open clock (LiveQuizSession.AdvanceTo) and fans out a
//     `question_advanced` event so every connected client switches question.
//   - gradeAndFanOutResponse (called after a learner SubmitResponse) grades the
//     answer with time-decay (classroom.TimeDecayScore over the open-clock),
//     bumps the authoritative TallyStore, credits the per-session ZSET
//     LeaderboardStore, fans out an `answer_graded` event (with live counts +
//     top-N leaderboard, the shape the R+ play view consumes), and emits the
//     durable chora.delivery.live_quiz_session.score_awarded.v1 event for
//     chora-sharing's cross-session economy.
//
// Hot-path fan-out goes onto the cross-pod Backplane (Redis pub/sub in prod);
// each pod's realtime.FanIn re-emits into its local ws.Broker. All realtime
// deps are nil-guarded — when unwired (e.g. no Redis in dev), response
// recording + the durable event still happen; only the live fan-out is skipped.
package httpapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/realtime"
	wsadapter "github.com/apollo-chora/chora-delivery/internal/adapter/ws"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// leaderboardTopN caps the live leaderboard broadcast to each client.
const leaderboardTopN = 20

type advanceQuestionReq struct {
	QuestionID string `json:"question_id"`
}

// handleAdvanceQuestion opens a question for answering (instructor action).
func handleAdvanceQuestion(deps Deps, sessionID string, w http.ResponseWriter, r *http.Request) {
	if deps.ClassroomSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "classroom sessions repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasClassroomInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	pre, ok, err := deps.ClassroomSessions.Get(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "classroom session lookup failed: "+err.Error())
		return
	}
	if !ok || pre == nil || pre.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "classroom session not found")
		return
	}
	var req advanceQuestionReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.QuestionID) == "" {
		writeError(w, http.StatusBadRequest, classroom.ErrLiveQuizSessionQuestionRequired.Error())
		return
	}
	var s *classroom.LiveQuizSession
	mutErr := deps.ClassroomSessions.Mutate(sessionID, func(in *classroom.LiveQuizSession) error {
		if in.TenantID != tenantID {
			return classroom.ErrSessionNotFound
		}
		if err := in.AdvanceTo(req.QuestionID, time.Now().UTC()); err != nil {
			return err
		}
		s = in
		return nil
	})
	if mutErr != nil {
		// Not LIVE / already-asked → 409; unknown id → 404 (writeClassroom-
		// SessionError); blank id already handled above.
		writeClassroomSessionError(w, mutErr)
		return
	}
	// The fan-out frame MUST be a coherent snapshot: the chora-web play view
	// runs decodeSnapshot on EVERY event frame, so a partial frame would
	// clobber its live snapshot to a blank ARMED state
	// (feedback_parallel_agent_contract_drift). Build the snapshot then layer
	// the advance annotation on top.
	fanOutQuiz(r.Context(), deps, s.ID, "question_advanced", questionAdvancedFrame{
		liveQuizSnapshotPayload: buildLiveQuizSnapshotDeps(r.Context(), deps, s),
		QuestionID:              req.QuestionID,
		OpenedAt:                s.CurrentQuestionOpenedAt,
	})
	_, callerGCID := tenantID, strings.TrimSpace(r.Header.Get("gcid"))
	writeJSON(w, http.StatusOK, classroomSessionDTO(deps, s, callerGCID))
}

// fanOutResponseEffects runs the ADR-168 hot path after a GRADED answer is
// recorded (the domain already composed the score — L5.2). Best-effort: every
// step is nil-guarded and never fails the request.
//
// ADR-179 D3 (leak #2): the `answer_graded` broadcast is a single in-tenant
// fan-out with NO role gate — it must stay learner-safe permanently, so it
// carries NO correctness and NO points. The submitter's own grade travels
// only on their REST 201 (`your_result`); the durable score_awarded.v1 (a
// backend topic, not learner-reachable) keeps full fidelity.
func fanOutResponseEffects(r *http.Request, deps Deps, s *classroom.LiveQuizSession, quiz *classroom.LiveQuiz, q classroom.LiveQuizQuestion, gcid string, res classroom.ScoreResult, elapsedMs int64) {
	ctx := r.Context()

	// Authoritative tally (cross-pod, no split-brain).
	if deps.RealtimeTally != nil {
		_, _ = deps.RealtimeTally.Incr(ctx, s.ID, q.QuestionID, latestChoice(s, gcid, q.QuestionID))
	}
	// Cumulative score (per-session ZSET leaderboard).
	cumulative := res.Awarded
	if deps.RealtimeLeaderboard != nil && res.Awarded > 0 {
		if total, err := deps.RealtimeLeaderboard.Credit(ctx, s.ID, gcid, int64(res.Awarded)); err == nil {
			cumulative = int(total)
		}
	}

	nickname := ""
	if p, ok := s.Participants[gcid]; ok {
		nickname = p.Nickname
	}
	// Cross-pod fan-out as a COHERENT SNAPSHOT (correctness-stripped). The
	// chora-web play view runs decodeSnapshot on every event frame, so the
	// frame must carry the full snapshot or it clobbers the live view
	// (feedback_parallel_agent_contract_drift).
	fanOutQuiz(ctx, deps, s.ID, "answer_graded", answerGradedFrame{
		liveQuizSnapshotPayload: buildLiveQuizSnapshot(ctx, s, quiz, deps.RealtimeTally, deps.RealtimeLeaderboard),
		QuestionID:              q.QuestionID,
		LearnerGCID:             gcid,
		Nickname:                nickname,
	})

	// Durable path: score_awarded → chora-sharing Ranker + chora-consumption
	// derived-weakness (outbox + DLQ). AwardedPoints is bonus-inclusive
	// (streak + double) from L5.2 — same field semantics, larger values.
	if deps.Publisher != nil {
		_, _ = deps.Publisher.PublishLiveQuizScoreAwarded(events.LiveQuizScoreAwarded{
			TenantID:        s.TenantID,
			GCID:            gcid,
			SessionID:       s.ID,
			LiveQuizID:      s.LiveQuizID,
			QuestionID:      q.QuestionID,
			AwardedPoints:   res.Awarded,
			CumulativeScore: cumulative,
			Correct:         res.Correct,
			AnswerMillis:    elapsedMs,
			AtomID:          q.AtomID,
			TopicTags:       q.TopicTags,
		})
	}
}

// latestChoice returns the caller's recorded choice for a question (the one
// the Mutate just appended) so the tally increments the right bucket.
func latestChoice(s *classroom.LiveQuizSession, gcid, questionID string) string {
	for i := len(s.QuestionResponses) - 1; i >= 0; i-- {
		r := s.QuestionResponses[i]
		if r.LearnerGCID == gcid && r.QuestionID == questionID {
			return r.Choice
		}
	}
	return ""
}

// buildLiveQuizSnapshotDeps resolves the quiz template (nil-safe) and builds
// the coherent WS snapshot — the shared entry for advance/join/close/grade
// fan-outs.
// A failed quiz read here is surfaced as a LOUD LOG rather than an error
// (CHO-2184). Every caller runs AFTER its mutation has committed — they are
// building the fan-out frame, not deciding the write. Returning an error would
// make the handler 500 a write that actually succeeded, and the client would
// retry it: a double mutation. So the failure must be visible (log + Error
// Reporting), while the committed write is still honestly reported as committed.
// The snapshot degrades to quiz-less, which the FE already renders nil-safe.
func buildLiveQuizSnapshotDeps(ctx context.Context, deps Deps, s *classroom.LiveQuizSession) liveQuizSnapshotPayload {
	var quiz *classroom.LiveQuiz
	if deps.LiveQuizzes != nil {
		q, ok, err := deps.LiveQuizzes.Get(s.LiveQuizID)
		if err != nil {
			log.Printf("live-quiz snapshot: quiz %s lookup FAILED for session %s — fan-out frame will be quiz-less: %v",
				s.LiveQuizID, s.ID, err)
		} else if ok {
			quiz = q
		}
	}
	return buildLiveQuizSnapshot(ctx, s, quiz, deps.RealtimeTally, deps.RealtimeLeaderboard)
}

// questionAdvancedFrame is the `question_advanced` fan-out payload: a coherent
// snapshot (so the FE's decodeSnapshot keeps the play view live) annotated with
// the just-opened question. liveQuizSnapshotPayload is embedded so its fields
// flatten into the JSON object.
type questionAdvancedFrame struct {
	liveQuizSnapshotPayload
	QuestionID string     `json:"question_id"`
	OpenedAt   *time.Time `json:"opened_at,omitempty"`
}

// answerGradedFrame is the `answer_graded` fan-out payload: a coherent snapshot
// annotated with WHO answered (nickname for the roster ticker) — and, per
// ADR-179 D3, deliberately NOTHING about how they scored.
type answerGradedFrame struct {
	liveQuizSnapshotPayload
	QuestionID  string `json:"question_id"`
	LearnerGCID string `json:"learner_gcid"`
	Nickname    string `json:"nickname,omitempty"`
}

// participantJoinedFrame is the lobby fan-out: the roster lives inside the
// embedded snapshot.
type participantJoinedFrame struct {
	liveQuizSnapshotPayload
}

// sessionClosedFrame is the podium moment: the embedded snapshot carries
// final_scoreboard + podium.
type sessionClosedFrame struct {
	liveQuizSnapshotPayload
}

// fanOutQuiz marshals a ws.Message and publishes it on the session's backplane
// channel. The per-pod realtime.FanIn re-emits it into the local ws.Broker.
// No-op when the backplane is unwired.
func fanOutQuiz(ctx context.Context, deps Deps, sessionID, msgType string, payload any) {
	if deps.RealtimeBackplane == nil {
		return
	}
	b, err := json.Marshal(wsadapter.Message{
		Type:      msgType,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	})
	if err != nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_ = deps.RealtimeBackplane.Publish(ctx, realtime.QuizChannel(sessionID), b)
}

func findQuestion(q *classroom.LiveQuiz, questionID string) (classroom.LiveQuizQuestion, bool) {
	for _, qq := range q.Questions {
		if qq.QuestionID == questionID {
			return qq, true
		}
	}
	return classroom.LiveQuizQuestion{}, false
}

// isCorrectChoice reports whether the learner's choice matches the question's
// correct option. Choice is matched against the option Label (the answer-content
// per feedback_mcq_option_naming).
func isCorrectChoice(q classroom.LiveQuizQuestion, choice string) bool {
	for _, opt := range q.Options {
		if opt.IsCorrect && opt.Label == choice {
			return true
		}
	}
	return false
}
