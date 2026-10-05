// classroom_session_handler.go — HTTP handlers for the R+ classroom-realtime
// LiveQuizSession aggregate (Stage C-lite Wave-5 M8).
//
// 5 endpoints registered via handlers.go integration diff:
//
//	POST /api/v1/live-quizzes/{quizId}/sessions    — start (ARMED) [instructor]
//	POST /api/v1/classroom-sessions/{id}/start     — ARMED→LIVE   [instructor]
//	POST /api/v1/classroom-sessions/{id}/close     — LIVE→CLOSED  [instructor]
//	GET  /api/v1/classroom-sessions/{id}           — snapshot     [any in-tenant caller]
//	POST /api/v1/classroom-sessions/{id}/responses — SubmitResponse [learner]
//
// Authorisation (verified inside the handler, NOT at middleware) — mirrors
// the pattern in exam_handler.go + applications_admin_handler.go:
//
//   - tenantRequired enforces X-Tenant-Id presence (400 otherwise).
//   - gcid header is required on writes (401 if missing) — Bucket 4
//     servicemesh propagation guarantees this when the caller's JWT is valid.
//   - POST start / start-transition / close: caller carries `instructor`,
//     `admin`, or `training-admin` role (403 otherwise).
//   - POST responses: caller carries `learner`, `instructor`, `admin`, or
//     `training-admin` role (instructors test-submit during prep). 403
//     otherwise.
//   - GET snapshot: any in-tenant caller (no role gate — the presenter view
//     mirrors learner-visible state).
//
// Snapshot shape (matched by the FE polling adapter):
//
//	{
//	  "id": "...",
//	  "tenant_id": "...",
//	  "live_quiz_id": "...",
//	  "instructor_gcid": "...",
//	  "state": "ARMED" | "LIVE" | "CLOSED",
//	  "current_question_id": "<latest QuestionResponse.question_id, or '' empty>",
//	  "response_counts": { "<choice>": <int>, ... },  // scoped to current_question_id
//	  "joined_learners": <int>,                       // unique GCIDs across the session
//	  "started_at": "RFC3339 | null",
//	  "ended_at":   "RFC3339 | null",
//	  "created_at": "RFC3339",
//	  "updated_at": "RFC3339"
//	}
//
// Add-only: the existing Deps struct gains one new field
// `ClassroomSessions *inmem.ClassroomSessionRepo` via the integration diff;
// nothing else in this file touches handlers.go / cmd/server.
//
// Per the parallel-session contract (feedback_parallel_agent_contract_drift),
// master owns the handlers.go mux entries + cmd/server wiring + chora-gateway
// BFF prefix addition + chora-web nav/route/i18n integration — this file is
// the standalone handler implementation only.
package httpapi

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// errorsIs aliases errors.Is for the local error-mapping switch readability.
var errorsIs = errors.Is

// -----------------------------------------------------------------------------
// Request DTOs
// -----------------------------------------------------------------------------

type submitResponseReq struct {
	QuestionID string `json:"question_id"`
	Choice     string `json:"choice"`
}

type joinSessionReq struct {
	Nickname string `json:"nickname"`
}

// -----------------------------------------------------------------------------
// Dispatchers — registered by handlers.go integration diff
// -----------------------------------------------------------------------------

// liveQuizSessionsRootHandler dispatches POST /api/v1/live-quizzes/{quizId}/sessions.
// Path shape MUST be exactly `/api/v1/live-quizzes/{quizId}/sessions`.
//
//	POST → create ARMED session (RBAC: instructor / admin / training-admin)
func liveQuizSessionsRootHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/live-quizzes/")
		// Expect exactly `{quizId}/sessions`. Reject anything else.
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] != "sessions" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		quizID := parts[0]
		handleStartSession(deps, quizID, w, r)
	}
}

// classroomSessionsSubHandler dispatches /api/v1/classroom-sessions/{id}[/{action}].
//
//	GET  /api/v1/classroom-sessions/{id}            → snapshot
//	POST /api/v1/classroom-sessions/{id}/start      → ARMED→LIVE
//	POST /api/v1/classroom-sessions/{id}/close      → LIVE→CLOSED
//	POST /api/v1/classroom-sessions/{id}/advance    → AdvanceTo (instructor)
//	POST /api/v1/classroom-sessions/{id}/responses  → SubmitResponse (graded)
func classroomSessionsSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/classroom-sessions/")
		rest = strings.TrimSuffix(rest, "/")
		if rest == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		parts := strings.Split(rest, "/")
		// L5.2 — `by-code` is a RESERVED segment (session ids are UUIDv7, no
		// collision possible). Branch BEFORE treating parts[0] as an id.
		if parts[0] == "by-code" {
			if len(parts) != 2 || parts[1] == "" || r.Method != http.MethodGet {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			handleResolveByCode(deps, parts[1], w, r)
			return
		}
		sessionID := parts[0]
		if sessionID == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		switch len(parts) {
		case 1:
			// /{id}
			switch r.Method {
			case http.MethodGet:
				handleGetSnapshot(deps, sessionID, w, r)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
		case 2:
			// /{id}/{action}
			action := parts[1]
			switch action {
			case "start":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleTransitionStart(deps, sessionID, w, r)
			case "close":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleTransitionClose(deps, sessionID, w, r)
			case "advance":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleAdvanceQuestion(deps, sessionID, w, r)
			case "responses":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleSubmitResponse(deps, sessionID, w, r)
			case "join":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleJoinSession(deps, sessionID, w, r)
			default:
				writeError(w, http.StatusNotFound, "not found")
			}
		default:
			writeError(w, http.StatusNotFound, "not found")
		}
	}
}

// -----------------------------------------------------------------------------
// Per-endpoint handlers
// -----------------------------------------------------------------------------

func handleStartSession(deps Deps, quizID string, w http.ResponseWriter, r *http.Request) {
	if deps.ClassroomSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "classroom sessions repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasClassroomInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	s, err := classroom.NewLiveQuizSession(quizID, tenantID, gcid)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// L5.2 — join-code uniqueness among the tenant's ACTIVE sessions.
	// Collision odds are ~active_sessions/1.07e9; the bounded loop is
	// belt-and-braces before the first Save.
	for i := 0; i < 5; i++ {
		existing, taken, err := deps.ClassroomSessions.GetByJoinCode(tenantID, s.JoinCode)
		if err != nil {
			// A uniqueness probe that FAILED is not proof the code is free. Treating
			// it as free would hand two live sessions the same join code and send
			// learners into the wrong classroom (CHO-2184).
			writeError(w, http.StatusInternalServerError, "join-code uniqueness check failed: "+err.Error())
			return
		}
		if !taken || existing == nil {
			break
		}
		s.JoinCode = classroom.NewJoinCode()
	}
	if !persistOK(w, deps.ClassroomSessions.Save(s)) {
		return
	}
	writeJSON(w, http.StatusCreated, classroomSessionDTO(deps, s, gcid))
}

func handleTransitionStart(deps Deps, sessionID string, w http.ResponseWriter, r *http.Request) {
	if deps.ClassroomSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "classroom sessions repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasClassroomInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	s, ok, err := deps.ClassroomSessions.Get(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "classroom session lookup failed: "+err.Error())
		return
	}
	if !ok || s == nil || s.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "classroom session not found")
		return
	}
	if err := s.Start(time.Now().UTC()); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if !persistOK(w, deps.ClassroomSessions.Save(s)) {
		return
	}
	// ADR-168 durable lifecycle — best-effort, nil-guarded. The session state
	// is the source of truth; a publish failure never fails the transition.
	if deps.Publisher != nil {
		_, _ = deps.Publisher.PublishLiveQuizSessionStarted(events.LiveQuizSessionStarted{
			TenantID:       s.TenantID,
			GCID:           s.InstructorGCID,
			SessionID:      s.ID,
			LiveQuizID:     s.LiveQuizID,
			InstructorGCID: s.InstructorGCID,
			StartedAt:      derefTime(s.StartedAt),
		})
	}
	writeJSON(w, http.StatusOK, classroomSessionDTO(deps, s, gcid))
}

func handleTransitionClose(deps Deps, sessionID string, w http.ResponseWriter, r *http.Request) {
	if deps.ClassroomSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "classroom sessions repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasClassroomInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	s, ok, err := deps.ClassroomSessions.Get(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "classroom session lookup failed: "+err.Error())
		return
	}
	if !ok || s == nil || s.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "classroom session not found")
		return
	}
	if err := s.Close(time.Now().UTC()); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if !persistOK(w, deps.ClassroomSessions.Save(s)) {
		return
	}
	// ADR-168 durable lifecycle — best-effort, nil-guarded.
	if deps.Publisher != nil {
		_, _ = deps.Publisher.PublishLiveQuizSessionEnded(events.LiveQuizSessionEnded{
			TenantID:       s.TenantID,
			GCID:           s.InstructorGCID,
			SessionID:      s.ID,
			LiveQuizID:     s.LiveQuizID,
			TotalResponses: len(s.QuestionResponses),
			EndedAt:        derefTime(s.EndedAt),
		})
	}
	// L5.2 — podium moment: fan out `session_closed` so the projector play
	// view flips to the podium with zero extra fetches (frame embeds the
	// coherent snapshot incl. final_scoreboard/podium).
	fanOutQuiz(r.Context(), deps, s.ID, "session_closed", sessionClosedFrame{
		liveQuizSnapshotPayload: buildLiveQuizSnapshotDeps(r.Context(), deps, s),
	})
	writeJSON(w, http.StatusOK, classroomSessionDTO(deps, s, gcid))
}

// derefTime returns the pointed-at time or the zero time when nil. The
// publisher tolerates a zero time (omits the timestamp field on the wire).
func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func handleGetSnapshot(deps Deps, sessionID string, w http.ResponseWriter, r *http.Request) {
	if deps.ClassroomSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "classroom sessions repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	s, ok, err := deps.ClassroomSessions.Get(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "classroom session lookup failed: "+err.Error())
		return
	}
	if !ok || s == nil || s.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "classroom session not found")
		return
	}
	// Per-caller `me`/`reveal` blocks render only when the caller carries a
	// gcid — header-only callers keep the legacy tenant-scoped snapshot.
	callerGCID := strings.TrimSpace(r.Header.Get("gcid"))
	writeJSON(w, http.StatusOK, classroomSessionDTO(deps, s, callerGCID))
}

func handleSubmitResponse(deps Deps, sessionID string, w http.ResponseWriter, r *http.Request) {
	if deps.ClassroomSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "classroom sessions repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasClassroomLearnerOrInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks learner/instructor role")
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
	var req submitResponseReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Pre-validate emptiness so we 400 (not 409) on empty field —
	// matches the test contract.
	if strings.TrimSpace(req.QuestionID) == "" {
		writeError(w, http.StatusBadRequest, classroom.ErrLiveQuizSessionQuestionRequired.Error())
		return
	}
	if strings.TrimSpace(req.Choice) == "" {
		writeError(w, http.StatusBadRequest, classroom.ErrLiveQuizSessionChoiceRequired.Error())
		return
	}

	// Resolve the question template for the GRADED path (L5.2). When the quiz
	// (or the question) is unavailable the legacy ungraded SubmitResponse
	// records the answer without scoring — old sessions keep working.
	var (
		quiz     *classroom.LiveQuiz
		question classroom.LiveQuizQuestion
		hasQ     bool
	)
	if deps.LiveQuizzes != nil {
		qz, found, err := deps.LiveQuizzes.Get(pre.LiveQuizID)
		if err != nil {
			// The quiz carries the question being answered — a failed read here
			// would silently drop the submission's grading basis (CHO-2184).
			writeError(w, http.StatusInternalServerError, "live quiz lookup failed: "+err.Error())
			return
		}
		if found && qz != nil {
			quiz = qz
			if q, foundQ := findQuestion(qz, req.QuestionID); foundQ {
				question, hasQ = q, true
			}
		}
	}

	submittedAt := time.Now().UTC()
	var (
		graded bool
		res    classroom.ScoreResult
		sess   *classroom.LiveQuizSession
	)
	// Mutate serialises concurrent submits (pg: row lock; inmem: mutex) —
	// without it simultaneous learners read-modify-write the same JSONB
	// snapshot and silently drop each other's responses.
	mutErr := deps.ClassroomSessions.Mutate(sessionID, func(s *classroom.LiveQuizSession) error {
		if s.TenantID != tenantID {
			return classroom.ErrSessionNotFound
		}
		sess = s
		if hasQ {
			r2, err := s.SubmitGraded(classroom.SubmitGradedInput{
				Question:    question,
				LearnerGCID: gcid,
				Choice:      req.Choice,
				At:          submittedAt,
			})
			if err != nil {
				return err
			}
			res, graded = r2, true
			return nil
		}
		return s.SubmitResponse(req.QuestionID, gcid, req.Choice, submittedAt)
	})
	if mutErr != nil {
		writeClassroomSessionError(w, mutErr)
		return
	}
	// ADR-168 hot path — tally + leaderboard + cross-pod fan-out + durable
	// score_awarded. Best-effort + nil-guarded: the recorded response is the
	// source of truth; side effects never fail the request.
	if graded {
		var elapsedMs int64
		if sess.CurrentQuestionOpenedAt != nil {
			if d := submittedAt.Sub(*sess.CurrentQuestionOpenedAt); d > 0 {
				elapsedMs = d.Milliseconds()
			}
		}
		fanOutResponseEffects(r, deps, sess, quiz, question, gcid, res, elapsedMs)
	}
	out := classroomSessionDTO(deps, sess, gcid)
	if graded {
		// The submitter's OWN grade — the only pre-reveal correctness channel
		// (ADR-179 D3). The broadcast frame carries no correctness at all.
		out["your_result"] = map[string]interface{}{
			"question_id":    req.QuestionID,
			"correct":        res.Correct,
			"base_points":    res.Base,
			"streak_bonus":   res.StreakBonus,
			"awarded_points": res.Awarded,
			"streak_after":   res.StreakAfter,
		}
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleJoinSession claims a nickname for the caller (L5.2, ADR-179 ruling 6).
func handleJoinSession(deps Deps, sessionID string, w http.ResponseWriter, r *http.Request) {
	if deps.ClassroomSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "classroom sessions repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasClassroomLearnerOrInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks learner/instructor role")
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
	var req joinSessionReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	var sess *classroom.LiveQuizSession
	mutErr := deps.ClassroomSessions.Mutate(sessionID, func(s *classroom.LiveQuizSession) error {
		if s.TenantID != tenantID {
			return classroom.ErrSessionNotFound
		}
		if _, err := s.Join(gcid, req.Nickname, now); err != nil {
			return err
		}
		sess = s
		return nil
	})
	if mutErr != nil {
		writeClassroomSessionError(w, mutErr)
		return
	}
	// Lobby fan-out — coherent snapshot so the projector roster pops live.
	fanOutQuiz(r.Context(), deps, sess.ID, "participant_joined", participantJoinedFrame{
		liveQuizSnapshotPayload: buildLiveQuizSnapshotDeps(r.Context(), deps, sess),
	})
	writeJSON(w, http.StatusOK, classroomSessionDTO(deps, sess, gcid))
}

// handleResolveByCode maps a projector join code to its ACTIVE session
// (learner entry — GET /api/v1/classroom-sessions/by-code/{code}).
func handleResolveByCode(deps Deps, rawCode string, w http.ResponseWriter, r *http.Request) {
	if deps.ClassroomSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "classroom sessions repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	code := classroom.NormalizeJoinCode(rawCode)
	if code == "" {
		writeError(w, http.StatusNotFound, "classroom session not found")
		return
	}
	s, ok, err := deps.ClassroomSessions.GetByJoinCode(tenantID, code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "classroom session lookup failed: "+err.Error())
		return
	}
	if !ok || s == nil {
		writeError(w, http.StatusNotFound, "classroom session not found")
		return
	}
	title := ""
	if deps.LiveQuizzes != nil {
		q, found, err := deps.LiveQuizzes.Get(s.LiveQuizID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "live quiz lookup failed: "+err.Error())
			return
		}
		if found && q != nil {
			title = q.Title
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id":        s.ID,
		"live_quiz_id":      s.LiveQuizID,
		"quiz_title":        title,
		"state":             string(s.State),
		"participant_count": len(s.Participants),
	})
}

// classroomSentinelCodes maps every L5.2 domain sentinel to its HTTP status +
// machine `code` (contract: delivery-live-classroom.yaml). The FE play-stage
// switches inline copy on `code` (profane vs taken vs closed), so codes are
// wire contract — rename only with a contract bump.
var classroomSentinelCodes = []struct {
	err    error
	status int
	code   string
}{
	{classroom.ErrSessionNotFound, http.StatusNotFound, "session_not_found"},
	{classroom.ErrLiveQuizSessionDuplicateResponse, http.StatusConflict, "duplicate_response"},
	{classroom.ErrLiveQuizSessionNotLive, http.StatusConflict, "session_not_live"},
	{classroom.ErrLiveQuizSessionQuestionNotOpen, http.StatusConflict, "question_not_open"},
	{classroom.ErrLiveQuizSessionQuestionLocked, http.StatusConflict, "question_locked"},
	{classroom.ErrLiveQuizSessionQuestionAlreadyAsked, http.StatusConflict, "question_already_asked"},
	{classroom.ErrLiveQuizSessionNicknameTaken, http.StatusConflict, "nickname_taken"},
	{classroom.ErrLiveQuizSessionCannotJoin, http.StatusConflict, "session_closed"},
	{classroom.ErrNicknameRequired, http.StatusBadRequest, "nickname_required"},
	{classroom.ErrNicknameTooLong, http.StatusBadRequest, "nickname_too_long"},
	{classroom.ErrNicknameInvalid, http.StatusBadRequest, "nickname_invalid"},
	{classroom.ErrNicknameProfane, http.StatusBadRequest, "nickname_profane"},
}

// writeClassroomSessionError maps L5.2 domain sentinels to HTTP codes:
// state/window/identity conflicts → 409; nickname validation → 400;
// unknown/cross-tenant session → 404; everything else → 400. The body is
// the standard writeError envelope plus the machine `code`:
// {"error": <status text>, "message": <human>, "code": <machine>}.
func writeClassroomSessionError(w http.ResponseWriter, err error) {
	for _, m := range classroomSentinelCodes {
		if errorsIs(err, m.err) {
			msg := err.Error()
			if m.err == classroom.ErrSessionNotFound {
				msg = "classroom session not found"
			}
			writeJSON(w, m.status, map[string]interface{}{
				"error":   http.StatusText(m.status),
				"message": msg,
				"code":    m.code,
			})
			return
		}
	}
	writeJSON(w, http.StatusBadRequest, map[string]interface{}{
		"error":   http.StatusText(http.StatusBadRequest),
		"message": err.Error(),
		"code":    "bad_request",
	})
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

// classroomSessionDTO renders a LiveQuizSession for the JSON wire as a
// snapshot — adds `current_question_id`, `response_counts`,
// `joined_learners` over the raw aggregate fields.
//
// `current_question_id` is the question_id of the latest QuestionResponse
// (the one the FE expects to render). When no responses exist, it's the
// empty string — the FE shows the "waiting for responses" state.
//
// `response_counts` is `LiveQuizSession.CountResponses(current_question_id)`
// — always a non-nil map (empty when no responses for the current q).
//
// `joined_learners` is the unique learner-GCID count across ALL responses
// in the session.
func classroomSessionDTO(deps Deps, s *classroom.LiveQuizSession, callerGCID string) map[string]interface{} {
	// Prefer the instructor-opened question (ADR-168 AdvanceTo). The REST
	// snapshot MUST reflect the instructor's "advance" immediately — the R+
	// presenter + learner views poll this DTO and render the open question
	// BEFORE any response exists. Fall back to the latest response's
	// question_id for legacy sessions driven purely by answers.
	currentQ := s.CurrentQuestionID
	if currentQ == "" {
		if n := len(s.QuestionResponses); n > 0 {
			currentQ = s.QuestionResponses[n-1].QuestionID
		}
	}
	counts := s.CountResponses(currentQ)
	wireCounts := make(map[string]int, len(counts))
	for k, v := range counts {
		wireCounts[k] = v
	}
	joined := uniqueLearners(s.QuestionResponses)

	out := map[string]interface{}{
		"id":                  s.ID,
		"tenant_id":           s.TenantID,
		"live_quiz_id":        s.LiveQuizID,
		"instructor_gcid":     s.InstructorGCID,
		"state":               string(s.State),
		"current_question_id": currentQ,
		"response_counts":     wireCounts,
		"joined_learners":     joined,
		"created_at":          s.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":          s.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if s.StartedAt != nil {
		out["started_at"] = s.StartedAt.UTC().Format(time.RFC3339)
	}
	if s.EndedAt != nil {
		out["ended_at"] = s.EndedAt.UTC().Format(time.RFC3339)
	}

	// ----- L5.2 Live Classroom stage extensions (ADR-179) — all additive. ----

	if s.JoinCode != "" {
		out["join_code"] = s.JoinCode
	}
	roster := s.Roster()
	participants := make([]map[string]interface{}, 0, len(roster))
	for _, p := range roster {
		// Nicknames ONLY — gcids never reach the roster wire (ruling 1).
		participants = append(participants, map[string]interface{}{
			"nickname":  p.Nickname,
			"joined_at": p.JoinedAt.UTC().Format(time.RFC3339),
		})
	}
	out["participants"] = participants
	out["participant_count"] = len(roster)

	// open_question — the learner-safe projection (labels only, no
	// is_correct/explainer) + the server-anchored countdown anchors. Emitted
	// only when the quiz template resolves (legacy sessions keep the bare
	// current_question_id field above).
	var (
		quiz            *classroom.LiveQuiz
		currentQuestion *classroom.LiveQuizQuestion
		locked          bool
	)
	// A failed quiz read is surfaced as a LOUD LOG, not an error (CHO-2184).
	// classroomSessionDTO is a projection: most callers reach it AFTER their
	// mutation has committed, so erroring out would 500 a write that actually
	// succeeded and invite a retry (a double mutation). The failure must be
	// visible; the response degrades to the bare current_question_id the legacy
	// path already emits. What it must NOT do is vanish.
	if deps.LiveQuizzes != nil {
		qz, found, err := deps.LiveQuizzes.Get(s.LiveQuizID)
		if err != nil {
			log.Printf("classroom session DTO: quiz %s lookup FAILED for session %s — open_question omitted: %v",
				s.LiveQuizID, s.ID, err)
		} else if found && qz != nil {
			quiz = qz
		}
	}
	now := time.Now().UTC()
	if quiz != nil && s.CurrentQuestionID != "" {
		if q, found := findQuestion(quiz, s.CurrentQuestionID); found {
			currentQuestion = &q
			locked = classroom.QuestionLocked(s.CurrentQuestionOpenedAt, q.TimerSecs, now)
			options := make([]map[string]interface{}, 0, len(q.Options))
			for _, opt := range q.Options {
				options = append(options, map[string]interface{}{"label": opt.Label})
			}
			oq := map[string]interface{}{
				"question_id":   q.QuestionID,
				"prompt":        q.Prompt,
				"options":       options,
				"timer_seconds": q.TimerSecs,
				"double_points": q.DoublePoints,
				"locked":        locked,
			}
			if s.CurrentQuestionOpenedAt != nil {
				oq["opened_at"] = s.CurrentQuestionOpenedAt.UTC().Format(time.RFC3339Nano)
			}
			if la := classroom.LocksAt(s.CurrentQuestionOpenedAt, q.TimerSecs); la != nil {
				oq["locks_at"] = la.UTC().Format(time.RFC3339Nano)
			}
			out["open_question"] = oq
		}
	}

	// Interim scoreboard (every question — ruling 3) once the game starts;
	// podium + frozen final board once CLOSED. Nickname-keyed, gcid-stripped.
	board := s.Scoreboard()
	if s.State != classroom.LiveQuizSessionStateArmed {
		out["scoreboard"] = scoreboardWire(board, leaderboardTopN)
	}
	if s.State == classroom.LiveQuizSessionStateClosed {
		final := s.FinalScoreboard
		if final == nil {
			final = board
		}
		out["final_scoreboard"] = scoreboardWire(final, 0)
		podium := final
		if len(podium) > 5 {
			podium = podium[:5]
		}
		out["podium"] = scoreboardWire(podium, 0)
	}

	// Per-caller blocks — omitted entirely when the caller has no gcid.
	if callerGCID != "" {
		callerSubmittedCurrent := false
		var lastGraded *classroom.QuestionResponse
		for i := range s.QuestionResponses {
			r := s.QuestionResponses[i]
			if r.LearnerGCID != callerGCID {
				continue
			}
			if r.QuestionID == currentQ {
				callerSubmittedCurrent = true
			}
			if r.Graded {
				lastGraded = &s.QuestionResponses[i]
			}
		}
		for _, e := range board {
			if e.GCID != callerGCID {
				continue
			}
			me := map[string]interface{}{
				"nickname": e.Nickname,
				"score":    e.Score,
				"rank":     e.Rank,
				"streak":   e.Streak,
			}
			if lastGraded != nil {
				me["last_result"] = map[string]interface{}{
					"question_id":    lastGraded.QuestionID,
					"correct":        lastGraded.Correct,
					"base_points":    lastGraded.BasePoints,
					"streak_bonus":   lastGraded.StreakBonus,
					"awarded_points": lastGraded.AwardedPoints,
					"streak_after":   lastGraded.StreakAfter,
				}
			}
			out["me"] = me
			break
		}
		// reveal — correct label + explainer for the current question, gated
		// by the quiz's explainer_mode (ADR-179 D3).
		if quiz != nil && currentQuestion != nil &&
			classroom.RevealAllowed(quiz.ExplainerMode, locked, callerSubmittedCurrent,
				s.State == classroom.LiveQuizSessionStateClosed) {
			reveal := map[string]interface{}{"question_id": currentQuestion.QuestionID}
			for _, opt := range currentQuestion.Options {
				if opt.IsCorrect {
					reveal["correct_label"] = opt.Label
					if opt.Explainer != "" {
						reveal["explainer"] = opt.Explainer
					}
					break
				}
			}
			out["reveal"] = reveal
		}
		// reveals — the post-close full answer review (HANDOFF_L52 §4.6).
		// `reveal` scopes to the current question, so END_OF_SESSION only
		// ever surfaced the LAST question; once CLOSED every asked question
		// is revealable under any mode except NEVER. Per-caller because each
		// entry carries the caller's own choice/grade.
		if quiz != nil && s.State == classroom.LiveQuizSessionStateClosed &&
			classroom.RevealAllowed(quiz.ExplainerMode, true, true, true) {
			mine := make(map[string]*classroom.QuestionResponse, len(s.QuestionResponses))
			for i := range s.QuestionResponses {
				r := &s.QuestionResponses[i]
				if r.LearnerGCID == callerGCID && r.Graded {
					mine[r.QuestionID] = r
				}
			}
			reveals := make([]map[string]interface{}, 0, len(s.AskedQuestionIDs))
			for _, qid := range s.AskedQuestionIDs {
				var question *classroom.LiveQuizQuestion
				for i := range quiz.Questions {
					if quiz.Questions[i].QuestionID == qid {
						question = &quiz.Questions[i]
						break
					}
				}
				if question == nil {
					continue
				}
				entry := map[string]interface{}{
					"question_id": qid,
					"prompt":      question.Prompt,
				}
				for _, opt := range question.Options {
					if opt.IsCorrect {
						entry["correct_label"] = opt.Label
						if opt.Explainer != "" {
							entry["explainer"] = opt.Explainer
						}
						break
					}
				}
				if r, ok := mine[qid]; ok {
					entry["your_choice"] = r.Choice
					entry["your_correct"] = r.Correct
				}
				reveals = append(reveals, entry)
			}
			if len(reveals) > 0 {
				out["reveals"] = reveals
			}
		}
	}
	return out
}

// scoreboardWire strips gcids off ScoreboardEntries for the learner-reachable
// wire (nickname/score/streak/rank only). n caps the list; n<=0 = no cap.
func scoreboardWire(entries []classroom.ScoreboardEntry, n int) []map[string]interface{} {
	if n > 0 && len(entries) > n {
		entries = entries[:n]
	}
	out := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]interface{}{
			"nickname": e.Nickname,
			"score":    e.Score,
			"streak":   e.Streak,
			"rank":     e.Rank,
		})
	}
	return out
}

// uniqueLearners returns the number of distinct learner GCIDs across the
// session's responses.
func uniqueLearners(responses []classroom.QuestionResponse) int {
	if len(responses) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(responses))
	for _, r := range responses {
		seen[r.LearnerGCID] = struct{}{}
	}
	return len(seen)
}

// -----------------------------------------------------------------------------
// RBAC helpers
// -----------------------------------------------------------------------------

// hasClassroomInstructorRole reports whether the caller carries one of the
// roles permitted to create or transition classroom sessions. Mirrors
// hasExamAdminRole — local to the classroom-session surface.
func hasClassroomInstructorRole(r *http.Request) bool {
	return callerHasMeshRole(r, roleInstructor, roleAdmin, roleTrainingAdmin, roleTenantAdmin)
}

// hasClassroomLearnerOrInstructorRole reports whether the caller may
// submit a response — learners (the primary submitter) and instructors
// (during prep / smoke tests).
func hasClassroomLearnerOrInstructorRole(r *http.Request) bool {
	return callerHasMeshRole(r, roleLearner, roleInstructor, roleAdmin,
		roleTrainingAdmin, roleTenantAdmin)
}

// Keep _ = inmem.NewClassroomSessionRepo bound so the import is referenced
// even if a future refactor inlines the deps reference. The actual
// reference lives via deps.ClassroomSessions.
var _ = func() *inmem.ClassroomSessionRepo { return nil }
