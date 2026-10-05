// live_quiz_session.go — LiveQuizSession aggregate for the R+ classroom-realtime
// stack (M8/M9/M10/M11). A LiveQuizSession is the per-run, instructor-driven
// instance of a published LiveQuiz: it captures per-(learner, question)
// responses with first-write-wins idempotency and tracks the ARMED → LIVE →
// CLOSED state machine that mirrors the parent quiz lifecycle.
//
// Hexagonal: pure domain. No infra, no repository imports.
//
// Aggregate invariants per `.claude/rules/ddd-enforcement.md`:
//   - QuestionResponses is append-only within a (learner, question) tuple.
//     A second submit by the same learner for the same question is rejected
//     as a duplicate — first-write-wins (per `feedback_strict_tdd` semantics
//     surfaced in the RED tests).
//   - State transitions are one-way: ARMED → LIVE → CLOSED. Start is
//     idempotent within the LIVE state (re-firing Start does not move the
//     StartedAt clock). Close is idempotent within CLOSED.
package classroom

import (
	"errors"
	"math"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// LiveQuizSession state machine
// -----------------------------------------------------------------------------

// LiveQuizSessionState models the LiveQuizSession lifecycle FSM:
// ARMED → LIVE → CLOSED.
type LiveQuizSessionState string

const (
	// LiveQuizSessionStateArmed — session created, awaiting Start.
	LiveQuizSessionStateArmed LiveQuizSessionState = "ARMED"
	// LiveQuizSessionStateLive — session running, accepting responses.
	LiveQuizSessionStateLive LiveQuizSessionState = "LIVE"
	// LiveQuizSessionStateClosed — session ended; no further responses
	// accepted, analytics frozen.
	LiveQuizSessionStateClosed LiveQuizSessionState = "CLOSED"
)

// IsValid reports whether s is a canonical FSM state.
func (s LiveQuizSessionState) IsValid() bool {
	switch s {
	case LiveQuizSessionStateArmed, LiveQuizSessionStateLive, LiveQuizSessionStateClosed:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	ErrLiveQuizSessionQuizIDRequired     = errors.New("classroom: live_quiz_id required")
	ErrLiveQuizSessionTenantRequired     = errors.New("classroom: tenant_id required")
	ErrLiveQuizSessionInstructorRequired = errors.New("classroom: instructor_gcid required")
	ErrLiveQuizSessionNotLive            = errors.New("classroom: session not LIVE")
	ErrLiveQuizSessionCannotStart        = errors.New("classroom: session not startable from current state")
	ErrLiveQuizSessionCannotClose        = errors.New("classroom: session not closable from current state")
	ErrLiveQuizSessionDuplicateResponse  = errors.New("classroom: learner already responded to this question (first-write-wins)")
	ErrLiveQuizSessionQuestionRequired   = errors.New("classroom: question_id required")
	ErrLiveQuizSessionLearnerRequired    = errors.New("classroom: learner_gcid required")
	ErrLiveQuizSessionChoiceRequired     = errors.New("classroom: choice required")
)

// -----------------------------------------------------------------------------
// QuestionResponse value object
// -----------------------------------------------------------------------------

// QuestionResponse is one learner's answer to one question within a session.
// Captured at SubmitResponse time and frozen forever (first-write-wins).
type QuestionResponse struct {
	QuestionID  string    `json:"question_id"`
	LearnerGCID string    `json:"learner_gcid"`
	Choice      string    `json:"choice"`
	SubmittedAt time.Time `json:"submitted_at"`
	// Graded fields (L5.2, ADR-179): set by SubmitGraded, which captures the
	// composed stage score at submit time so the scoreboard/podium derive
	// purely from the response log. Legacy SubmitResponse rows keep
	// Graded=false and never feed the scoreboard.
	Graded        bool  `json:"graded,omitempty"`
	Correct       bool  `json:"correct,omitempty"`
	BasePoints    int   `json:"base_points,omitempty"`
	StreakBonus   int   `json:"streak_bonus,omitempty"`
	AwardedPoints int   `json:"awarded_points,omitempty"`
	StreakAfter   int   `json:"streak_after,omitempty"`
	ElapsedMillis int64 `json:"elapsed_millis,omitempty"`
}

// -----------------------------------------------------------------------------
// LiveQuizSession aggregate root
// -----------------------------------------------------------------------------

// LiveQuizSession is the per-run instance of a published LiveQuiz.
type LiveQuizSession struct {
	ID                string               `json:"id"`
	LiveQuizID        string               `json:"live_quiz_id"`
	TenantID          string               `json:"tenant_id"`
	InstructorGCID    string               `json:"instructor_gcid"`
	State             LiveQuizSessionState `json:"state"`
	QuestionResponses []QuestionResponse   `json:"question_responses"`
	// CurrentQuestionID + CurrentQuestionOpenedAt are the instructor-driven
	// "open question" clock (ADR-168). AdvanceTo sets both; the producer
	// handler reads CurrentQuestionOpenedAt to compute answer-speed for the
	// TimeDecayScore at submit time.
	CurrentQuestionID       string     `json:"current_question_id,omitempty"`
	CurrentQuestionOpenedAt *time.Time `json:"current_question_opened_at,omitempty"`
	StartedAt               *time.Time `json:"started_at,omitempty"`
	EndedAt                 *time.Time `json:"ended_at,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	// L5.2 Live Classroom stage fields (ADR-179). All JSON-tagged so the JSONB
	// aggregate snapshot persists them with zero schema change.
	//
	// JoinCode is the ephemeral 6-char code learners type (or QR-scan) to
	// reach this session. Uniqueness among a tenant's ACTIVE sessions is
	// enforced at the store layer (collision-regenerate on create).
	JoinCode string `json:"join_code,omitempty"`
	// Participants is the lobby roster: gcid → Participant (nickname claimed
	// via Join). Everyone competes, instructor included (ruling 5).
	Participants map[string]Participant `json:"participants,omitempty"`
	// AskedQuestionIDs is the append-only presentation log (AdvanceTo order).
	// A question is asked ONCE — its answer window never reopens (protects
	// streak derivation + first-write-wins).
	AskedQuestionIDs []string `json:"asked_question_ids,omitempty"`
	// FinalScoreboard is frozen by Close (LIVE→CLOSED) — the podium source.
	// Ephemeral by design: lives only inside this aggregate (ADR-179 D4).
	FinalScoreboard []ScoreboardEntry `json:"final_scoreboard,omitempty"`
}

// NewLiveQuizSession constructs an ARMED LiveQuizSession ready to be Started.
//
// Returns:
//   - ErrLiveQuizSessionQuizIDRequired when quizID is blank.
//   - ErrLiveQuizSessionTenantRequired when tenantID is blank.
//   - ErrLiveQuizSessionInstructorRequired when instructorGCID is blank.
func NewLiveQuizSession(quizID, tenantID, instructorGCID string) (*LiveQuizSession, error) {
	if strings.TrimSpace(quizID) == "" {
		return nil, ErrLiveQuizSessionQuizIDRequired
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrLiveQuizSessionTenantRequired
	}
	if strings.TrimSpace(instructorGCID) == "" {
		return nil, ErrLiveQuizSessionInstructorRequired
	}
	now := time.Now().UTC()
	return &LiveQuizSession{
		ID:                NewUUIDv7(),
		LiveQuizID:        quizID,
		TenantID:          tenantID,
		InstructorGCID:    instructorGCID,
		State:             LiveQuizSessionStateArmed,
		QuestionResponses: []QuestionResponse{},
		JoinCode:          NewJoinCode(),
		Participants:      map[string]Participant{},
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// Start transitions ARMED → LIVE. Idempotent within LIVE (re-calling Start
// on an already-LIVE session preserves the original StartedAt). Returns
// ErrLiveQuizSessionCannotStart from any other state (e.g., CLOSED).
func (s *LiveQuizSession) Start(now time.Time) error {
	switch s.State {
	case LiveQuizSessionStateLive:
		return nil // idempotent — preserve original StartedAt
	case LiveQuizSessionStateArmed:
		started := now.UTC()
		s.State = LiveQuizSessionStateLive
		s.StartedAt = &started
		s.UpdatedAt = started
		return nil
	default:
		return ErrLiveQuizSessionCannotStart
	}
}

// Close transitions LIVE → CLOSED. Idempotent within CLOSED. Returns
// ErrLiveQuizSessionCannotClose when called from ARMED (session never ran).
func (s *LiveQuizSession) Close(now time.Time) error {
	switch s.State {
	case LiveQuizSessionStateClosed:
		return nil // idempotent — FinalScoreboard stays frozen
	case LiveQuizSessionStateLive:
		ended := now.UTC()
		s.State = LiveQuizSessionStateClosed
		s.EndedAt = &ended
		s.UpdatedAt = ended
		if s.FinalScoreboard == nil {
			// Freeze the podium source at close time (ADR-179 D4). Derived
			// purely from the graded response log — ephemeral, no new tables.
			s.FinalScoreboard = s.Scoreboard()
		}
		return nil
	default:
		return ErrLiveQuizSessionCannotClose
	}
}

// AdvanceTo opens a question for answering — the instructor "advance" action
// (ADR-168). It records the per-session question-open clock used by
// TimeDecayScore. Valid only while LIVE. Re-advancing to a new question
// re-opens the clock. Returns ErrLiveQuizSessionNotLive from non-LIVE states
// and ErrLiveQuizSessionQuestionRequired for a blank id.
func (s *LiveQuizSession) AdvanceTo(questionID string, now time.Time) error {
	if s.State != LiveQuizSessionStateLive {
		return ErrLiveQuizSessionNotLive
	}
	if strings.TrimSpace(questionID) == "" {
		return ErrLiveQuizSessionQuestionRequired
	}
	// L5.2 (ADR-179 ruling 7): a question is asked exactly once — re-opening
	// the window would corrupt streak derivation and resurrect locked
	// questions. Behaviour change from the silent clock re-open, by design.
	for _, asked := range s.AskedQuestionIDs {
		if asked == questionID {
			return ErrLiveQuizSessionQuestionAlreadyAsked
		}
	}
	opened := now.UTC()
	s.CurrentQuestionID = questionID
	s.CurrentQuestionOpenedAt = &opened
	s.AskedQuestionIDs = append(s.AskedQuestionIDs, questionID)
	s.UpdatedAt = opened
	return nil
}

// TimeDecayScore grades one answer with time-decay (ADR-168): a correct answer
// earns `points` scaled by how fast it arrived within the question timer —
// full points at the instant the question opened, decaying linearly to half
// points at the timer expiry, and clamped at half thereafter. A wrong answer
// (or zero points) earns 0. With no timer (timerSecs ≤ 0) a correct answer
// earns full points.
//
//	awarded = round(points · (1 − 0.5·min(elapsed/timer, 1)))   when correct
func TimeDecayScore(correct bool, points int, elapsed time.Duration, timerSecs int) int {
	if !correct || points <= 0 {
		return 0
	}
	if timerSecs <= 0 || elapsed <= 0 {
		return points
	}
	frac := elapsed.Seconds() / float64(timerSecs)
	if frac > 1 {
		frac = 1
	}
	return int(math.Round(float64(points) * (1 - 0.5*frac)))
}

// SubmitResponse records a learner's choice for a question. First-write-wins
// per (learner, question) — a second submit by the same learner for the same
// question is rejected with ErrLiveQuizSessionDuplicateResponse. Only valid
// when the session is LIVE.
func (s *LiveQuizSession) SubmitResponse(questionID, learnerGCID, choice string, at time.Time) error {
	if s.State != LiveQuizSessionStateLive {
		return ErrLiveQuizSessionNotLive
	}
	if strings.TrimSpace(questionID) == "" {
		return ErrLiveQuizSessionQuestionRequired
	}
	if strings.TrimSpace(learnerGCID) == "" {
		return ErrLiveQuizSessionLearnerRequired
	}
	if strings.TrimSpace(choice) == "" {
		return ErrLiveQuizSessionChoiceRequired
	}
	for _, r := range s.QuestionResponses {
		if r.QuestionID == questionID && r.LearnerGCID == learnerGCID {
			return ErrLiveQuizSessionDuplicateResponse
		}
	}
	s.QuestionResponses = append(s.QuestionResponses, QuestionResponse{
		QuestionID:  questionID,
		LearnerGCID: learnerGCID,
		Choice:      choice,
		SubmittedAt: at.UTC(),
	})
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// CountResponses tallies the per-choice counts for a given question. Returns
// an empty (non-nil) map when no responses exist for the question.
func (s *LiveQuizSession) CountResponses(questionID string) map[string]int {
	counts := make(map[string]int)
	for _, r := range s.QuestionResponses {
		if r.QuestionID == questionID {
			counts[r.Choice]++
		}
	}
	return counts
}
