// live_quiz_ws_handler.go — HTTP/WebSocket adapter for the Live Quiz
// fan-out endpoint (Wave-5, R+ classroom-realtime M10).
//
// One endpoint:
//
//	GET /api/v1/live-quizzes/{sessionId}/ws  — WebSocket upgrade.
//
// On connect:
//  1. The handler resolves sessionId against LiveQuizSessionPort.Get.
//     Miss → 404 (the WS upgrade NEVER starts so the client sees a clean
//     HTTP failure, not an opaque upgrade-then-immediate-close).
//  2. Subscribes to the broker for sessionId.
//  3. Sends a `snapshot` envelope carrying the current session state +
//     per-question response counts so the FE can render immediately on
//     reconnect without waiting for the next mutation.
//  4. Goroutine-pumps every subsequent broker Message onto the WS.
//  5. On read-side disconnect (FE closes, network drops, server shutdown)
//     calls cleanup() so the broker doesn't leak per-orphan-client state.
//
// What is NOT here:
//   - Learner POST /api/v1/classroom-sessions/{id}/responses — that REST
//     mutation handler is owned by agent ι. When ι's handler lands, it
//     MUST call deps.LiveQuizSessionsBroker.Publish(sessionID, Message{
//     Type: "event", Payload: <response_distribution_snapshot>}) so the
//     existing connections see the update.
//   - Instructor POST /api/v1/live-quizzes/{quizId}/advance — when that
//     handler lands it ALSO MUST Publish to the broker on the corresponding
//     session ID with a `question_advanced` event.
//
// Hexagonal: this file defines its own port LiveQuizSessionPort so the
// handler does not depend on a concrete repo implementation. The integration
// diff (NOT done in this file) registers the route + injects the port +
// the shared *ws.Broker via Deps.
//
// Per `.claude/rules/ddd-enforcement.md` cross-DB queries FORBIDDEN — the
// port returns the aggregate ONLY by primary key. List / search / filter
// surfaces live in other R+ handlers.
package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"github.com/apollo-chora/chora-delivery/internal/adapter/realtime"
	wsadapter "github.com/apollo-chora/chora-delivery/internal/adapter/ws"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// -----------------------------------------------------------------------------
// Port — what the live-quiz WS handler needs from the repo
// -----------------------------------------------------------------------------

// LiveQuizSessionPort is the minimal repo surface the WS fan-out handler
// depends on. The production wiring (M12+) will swap in a Postgres adapter
// against chora_delivery via PgBouncer; the in-memory adapter (chora-delivery
// internal/adapter/inmem) satisfies this interface trivially via a
// thin wrapper.
type LiveQuizSessionPort interface {
	// Get returns the LiveQuizSession by primary key. ok=false is a GENUINE
	// miss; a backing-store failure returns a non-nil error and must not be
	// reported as a miss (CHO-2184 — a dead store 404'd as an absent session).
	// Caller MUST NOT mutate the returned aggregate (the in-mem repo returns
	// shared pointers per existing chora-delivery convention; the WS handler
	// is read-only).
	Get(ctx context.Context, id string) (*classroom.LiveQuizSession, bool, error)
}

// -----------------------------------------------------------------------------
// LiveQuizWSHandler — public constructor wired by handlers.go integration diff
// -----------------------------------------------------------------------------

// LiveQuizWSHandler returns the http.Handler for
// GET /api/v1/live-quizzes/{sessionId}/ws.
//
// Signature accepts the port + broker directly so the existing Deps struct
// can wire it as e.g. `LiveQuizWSHandler(deps.LiveQuizSessions,
// deps.LiveQuizSessionsBroker, ...)` without leaking the WS internals into the
// shared Deps signature.
//
// tally + lb are the ADR-168 cross-pod realtime stores. When non-nil the
// snapshot-on-connect sources the LIVE question's response counts + the
// cumulative leaderboard from them (authoritative across HPA-scaled pods) so a
// client connecting to a non-mutating pod still sees current data. When nil
// (no Redis in dev) the snapshot falls back to the pod-local aggregate.
func LiveQuizWSHandler(repo LiveQuizSessionPort, quizzes classroom.QuizStore, broker *wsadapter.Broker, tally realtime.TallyStore, lb realtime.LeaderboardStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repo == nil || broker == nil {
			writeError(w, http.StatusServiceUnavailable, "live-quiz WS not wired")
			return
		}

		sessionID, ok := extractLiveQuizWSSessionID(r.URL.Path)
		if !ok {
			writeError(w, http.StatusNotFound, "session id required")
			return
		}

		// ADR-168 / CHO-1616 — authenticate the caller BEFORE the lookup so a
		// missing-identity probe is 401 regardless of session existence (no
		// 404-vs-401 oracle). The gateway stamps X-Tenant-Id + gcid from the
		// validated session JWT.
		callerTenant, ok := requireWSCallerIdentity(w, r)
		if !ok {
			return
		}

		sess, found, err := repo.Get(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "live quiz session lookup failed: "+err.Error())
			return
		}
		if !found || sess == nil {
			writeError(w, http.StatusNotFound, "live quiz session not found")
			return
		}

		// Tenant-scope the subscribe. A cross-tenant caller gets 404 (same as
		// not-found) so the existence of another tenant's session never leaks.
		if !wsTenantMatches(callerTenant, sess.TenantID) {
			writeError(w, http.StatusNotFound, "live quiz session not found")
			return
		}

		// Subscribe BEFORE the upgrade so any client-visible failure surfaces
		// as a clean HTTP error (Subscribe only fails if the broker is
		// closed — degraded state).
		ch, cleanup, err := broker.Subscribe(sessionID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "broker unavailable: "+err.Error())
			return
		}

		// Capture the snapshot envelope value BEFORE handing off to the
		// websocket.Server — the underlying aggregate is shared state and
		// may mutate while we're talking to the client; the snapshot reflects
		// the instant the client connected.
		var quiz *classroom.LiveQuiz
		if quizzes != nil {
			q, found, qErr := quizzes.Get(sess.LiveQuizID)
			if qErr != nil {
				// Still pre-upgrade, so this surfaces as a clean HTTP error rather
				// than a silently quiz-less snapshot (CHO-2184).
				writeError(w, http.StatusInternalServerError, "live quiz lookup failed: "+qErr.Error())
				return
			}
			if found {
				quiz = q
			}
		}
		snapshot := wsadapter.Message{
			Type:      "snapshot",
			Timestamp: time.Now().UTC(),
			Payload:   buildLiveQuizSnapshot(r.Context(), sess, quiz, tally, lb),
		}

		handler := websocket.Handler(func(conn *websocket.Conn) {
			defer cleanup()
			defer conn.Close()

			// Send the snapshot first so the FE has something to render
			// immediately on reconnect.
			if err := websocket.JSON.Send(conn, snapshot); err != nil {
				return
			}

			// Pump broker events. Stop on either:
			//   - broker channel close (Close() or cleanup race)
			//   - WS Send error (client gone / network drop)
			//   - Read-side error from the connection (client closed)
			done := make(chan struct{})
			go func() {
				defer close(done)
				// Drain client→server traffic so we notice disconnects
				// promptly. The Live Quiz protocol is server-push only;
				// learner submissions arrive via REST. Anything received
				// here is discarded.
				var sink any
				for {
					if err := websocket.JSON.Receive(conn, &sink); err != nil {
						return
					}
				}
			}()
			for {
				select {
				case <-done:
					return
				case msg, ok := <-ch:
					if !ok {
						return
					}
					if err := websocket.JSON.Send(conn, msg); err != nil {
						return
					}
				}
			}
		})

		handler.ServeHTTP(w, r)
	})
}

// -----------------------------------------------------------------------------
// Snapshot payload
// -----------------------------------------------------------------------------

// liveQuizSnapshotPayload is the wire shape inside Message.Payload for the
// initial `snapshot` envelope and every event frame.
//
// ⚠ ADR-179 D3: frames are a single in-tenant broadcast (no role gate) — this
// payload must stay LEARNER-SAFE permanently. open_question carries labels
// only; the scoreboard carries nicknames; no is_correct / explainer / per-
// answer correctness may ever be added here.
type liveQuizSnapshotPayload struct {
	ID                string                      `json:"id"`
	LiveQuizID        string                      `json:"live_quiz_id"`
	TenantID          string                      `json:"tenant_id"`
	InstructorGCID    string                      `json:"instructor_gcid"`
	State             string                      `json:"state"`
	CurrentQuestionID string                      `json:"current_question_id,omitempty"`
	StartedAt         *time.Time                  `json:"started_at,omitempty"`
	EndedAt           *time.Time                  `json:"ended_at,omitempty"`
	ResponseCounts    map[string]map[string]int   `json:"response_counts"`
	TotalResponses    int                         `json:"total_responses"`
	Leaderboard       []realtime.LeaderboardEntry `json:"leaderboard,omitempty"`
	// L5.2 (ADR-179) — lobby + stage + podium projections.
	JoinCode         string                 `json:"join_code,omitempty"`
	Participants     []wsParticipantDTO     `json:"participants"`
	ParticipantCount int                    `json:"participant_count"`
	OpenQuestion     *wsOpenQuestionDTO     `json:"open_question,omitempty"`
	Scoreboard       []wsScoreboardEntryDTO `json:"scoreboard,omitempty"`
	Podium           []wsScoreboardEntryDTO `json:"podium,omitempty"`
	FinalScoreboard  []wsScoreboardEntryDTO `json:"final_scoreboard,omitempty"`
}

// wsParticipantDTO is a roster entry — nicknames only, never gcids.
type wsParticipantDTO struct {
	Nickname string    `json:"nickname"`
	JoinedAt time.Time `json:"joined_at"`
}

// wsOpenQuestionDTO is the learner-safe open-question block (labels only).
type wsOpenQuestionDTO struct {
	QuestionID   string             `json:"question_id"`
	Prompt       string             `json:"prompt"`
	Options      []wsOptionLabelDTO `json:"options"`
	OpenedAt     *time.Time         `json:"opened_at,omitempty"`
	TimerSeconds int                `json:"timer_seconds"`
	DoublePoints bool               `json:"double_points"`
	LocksAt      *time.Time         `json:"locks_at,omitempty"`
	Locked       bool               `json:"locked"`
}

type wsOptionLabelDTO struct {
	Label string `json:"label"`
}

// wsScoreboardEntryDTO is a ranked row — gcid-stripped for the broadcast.
type wsScoreboardEntryDTO struct {
	Nickname string `json:"nickname"`
	Score    int    `json:"score"`
	Streak   int    `json:"streak"`
	Rank     int    `json:"rank"`
}

// buildLiveQuizSnapshot projects the aggregate to the wire shape.
//
// When the cross-pod realtime stores are wired (tally/lb non-nil per ADR-168),
// the LIVE question's response counts come from the authoritative TallyStore
// and the cumulative leaderboard from the LeaderboardStore — so a client
// connecting to a non-mutating pod (whose aggregate never recorded those
// per-vote writes) still renders current data. The aggregate supplies the
// historical per-question counts as a baseline; the tally overlays the live
// question on top.
func buildLiveQuizSnapshot(ctx context.Context, s *classroom.LiveQuizSession, quiz *classroom.LiveQuiz, tally realtime.TallyStore, lb realtime.LeaderboardStore) liveQuizSnapshotPayload {
	out := liveQuizSnapshotPayload{
		ID:                s.ID,
		LiveQuizID:        s.LiveQuizID,
		TenantID:          s.TenantID,
		InstructorGCID:    s.InstructorGCID,
		State:             string(s.State),
		CurrentQuestionID: s.CurrentQuestionID,
		StartedAt:         s.StartedAt,
		EndedAt:           s.EndedAt,
		ResponseCounts:    make(map[string]map[string]int),
		TotalResponses:    len(s.QuestionResponses),
		JoinCode:          s.JoinCode,
		Participants:      []wsParticipantDTO{},
	}
	seenQuestions := make(map[string]struct{})
	for _, r := range s.QuestionResponses {
		seenQuestions[r.QuestionID] = struct{}{}
	}
	for qid := range seenQuestions {
		out.ResponseCounts[qid] = s.CountResponses(qid)
	}
	// Overlay the LIVE question's authoritative cross-pod counts.
	if tally != nil && s.CurrentQuestionID != "" {
		if snap, err := tally.Snapshot(ctx, s.ID, s.CurrentQuestionID); err == nil && len(snap) > 0 {
			counts := make(map[string]int, len(snap))
			for choice, n := range snap {
				counts[choice] = int(n)
			}
			out.ResponseCounts[s.CurrentQuestionID] = counts
		}
	}
	if lb != nil {
		if board, err := lb.Top(ctx, s.ID, leaderboardTopN); err == nil {
			out.Leaderboard = board
		}
	}

	// ----- L5.2 (ADR-179) — lobby roster + learner-safe open question +
	// nickname scoreboard + podium. All derived from the aggregate; the quiz
	// template is nil-tolerated (legacy sessions skip open_question).
	for _, p := range s.Roster() {
		out.Participants = append(out.Participants, wsParticipantDTO{Nickname: p.Nickname, JoinedAt: p.JoinedAt})
	}
	out.ParticipantCount = len(out.Participants)
	if quiz != nil && s.CurrentQuestionID != "" {
		for _, q := range quiz.Questions {
			if q.QuestionID != s.CurrentQuestionID {
				continue
			}
			oq := &wsOpenQuestionDTO{
				QuestionID:   q.QuestionID,
				Prompt:       q.Prompt,
				Options:      make([]wsOptionLabelDTO, 0, len(q.Options)),
				OpenedAt:     s.CurrentQuestionOpenedAt,
				TimerSeconds: q.TimerSecs,
				DoublePoints: q.DoublePoints,
				LocksAt:      classroom.LocksAt(s.CurrentQuestionOpenedAt, q.TimerSecs),
				Locked:       classroom.QuestionLocked(s.CurrentQuestionOpenedAt, q.TimerSecs, time.Now().UTC()),
			}
			for _, opt := range q.Options {
				oq.Options = append(oq.Options, wsOptionLabelDTO{Label: opt.Label})
			}
			out.OpenQuestion = oq
			break
		}
	}
	if s.State != classroom.LiveQuizSessionStateArmed {
		out.Scoreboard = wsScoreboardWire(s.Scoreboard(), leaderboardTopN)
	}
	if s.State == classroom.LiveQuizSessionStateClosed {
		final := s.FinalScoreboard
		if final == nil {
			final = s.Scoreboard()
		}
		out.FinalScoreboard = wsScoreboardWire(final, 0)
		podium := final
		if len(podium) > 5 {
			podium = podium[:5]
		}
		out.Podium = wsScoreboardWire(podium, 0)
	}
	return out
}

// wsScoreboardWire strips gcids for the broadcast. n caps; n<=0 = no cap.
func wsScoreboardWire(entries []classroom.ScoreboardEntry, n int) []wsScoreboardEntryDTO {
	if n > 0 && len(entries) > n {
		entries = entries[:n]
	}
	out := make([]wsScoreboardEntryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, wsScoreboardEntryDTO{Nickname: e.Nickname, Score: e.Score, Streak: e.Streak, Rank: e.Rank})
	}
	return out
}

// -----------------------------------------------------------------------------
// Path parsing
// -----------------------------------------------------------------------------

// extractLiveQuizWSSessionID parses /api/v1/live-quizzes/{sessionId}/ws and
// returns the session ID. Empty sessionID returns ok=false so the caller
// can 404 (preventing an upgrade attempt on a malformed path). Tolerates
// Go's http.ServeMux path-canonicalisation (// collapsed to /) — the
// degenerate /api/v1/live-quizzes/ws shape has rest="ws" which lacks the
// /ws suffix and returns ok=false.
func extractLiveQuizWSSessionID(urlPath string) (string, bool) {
	const prefix = "/api/v1/live-quizzes/"
	const suffix = "/ws"
	if !strings.HasPrefix(urlPath, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(urlPath, prefix)
	if !strings.HasSuffix(rest, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(rest, suffix)
	id = strings.Trim(id, "/")
	if id == "" {
		return "", false
	}
	return id, true
}
