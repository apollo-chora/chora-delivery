// submission.go — Submission aggregate per ADR-155 §D7 (Submission FSM) +
// §D8 (demo simplification: MCQ-only fast path).
//
// Append-only invariant (per .claude/rules/ddd-enforcement.md #4):
//   - Autosave creates/overwrites SubmissionAnswer rows in place (idempotent
//     on submission_id + test_set_question_id). Multiple answers per question
//     are NOT versioned — the latest answer wins. This matches the OpenAPI
//     contract: PATCH /autosave merges by question_id.
//
// State machine (Submission FSM per ADR-155 §D7):
//
//	STARTED → IN_PROGRESS  (after first autosave)
//	IN_PROGRESS → SUBMITTED       (via /submit; all-MCQ assessments graded inline)
//	SUBMITTED → PENDING_OE_GRADING (if assessment has any OE questions)
//	SUBMITTED → GRADED_PENDING_RELEASE (if MCQ-only; demo fast path)
//	PENDING_OE_GRADING → GRADED_PENDING_RELEASE (oe_batch_completed inbox event)
//	GRADED_PENDING_RELEASE → RELEASED (parent assessment /release-results)
//
// Demo simplification (ADR-155 §D8): MCQ-only assessments skip the GRADING
// phase and land directly in GRADED_PENDING_RELEASE on /submit. Release-
// results then flips the assessment + every PENDING_RELEASE submission.
//
// The OpenAPI states (IN_PROGRESS / SUBMITTED / GRADING / GRADED / RELEASED)
// map to the implementation states as follows for the FE wire shape:
//
//	IN_PROGRESS                              → "IN_PROGRESS"
//	PENDING_OE_GRADING                       → "GRADING"
//	GRADED_PENDING_RELEASE (pre-release)     → "GRADED"  (instructor view)
//	                                          "GRADED" (result endpoint maps
//	                                          to PENDING_RELEASE envelope)
//	RELEASED                                 → "RELEASED"
package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SubmissionState models the submission lifecycle.
type SubmissionState string

const (
	SubmissionStateStarted              SubmissionState = "STARTED"
	SubmissionStateInProgress           SubmissionState = "IN_PROGRESS"
	SubmissionStateSubmitted            SubmissionState = "SUBMITTED"
	SubmissionStatePendingOEGrading     SubmissionState = "PENDING_OE_GRADING"
	SubmissionStateGradedPendingRelease SubmissionState = "GRADED_PENDING_RELEASE"
	SubmissionStateReleased             SubmissionState = "RELEASED"
	SubmissionStateArchived             SubmissionState = "ARCHIVED"
)

// WireState maps the internal state to the OpenAPI-facing SubmissionState
// enum (IN_PROGRESS / SUBMITTED / GRADING / GRADED / RELEASED / ARCHIVED).
func (s SubmissionState) WireState() string {
	switch s {
	case SubmissionStateStarted:
		return "IN_PROGRESS"
	case SubmissionStateInProgress:
		return "IN_PROGRESS"
	case SubmissionStateSubmitted:
		return "SUBMITTED"
	case SubmissionStatePendingOEGrading:
		return "GRADING"
	case SubmissionStateGradedPendingRelease:
		return "GRADED"
	case SubmissionStateReleased:
		return "RELEASED"
	case SubmissionStateArchived:
		return "ARCHIVED"
	default:
		return string(s)
	}
}

// QuestionType enumerates the test-set question shapes.
type QuestionType string

const (
	QuestionTypeMCQ QuestionType = "mcq"
	QuestionTypeOE  QuestionType = "oe"
)

// ReviewStatus models the per-submission HITL grade-release gate (ADR-172 §D6).
//
//   - ReviewStatusNotRequired ("") — MCQ-only submissions; no human gate.
//   - ReviewStatusPendingReview    — OE answers graded by AI, awaiting an
//     instructor's review/approval before results can release.
//   - ReviewStatusApproved         — instructor signed off; releasable.
type ReviewStatus string

const (
	ReviewStatusNotRequired   ReviewStatus = ""
	ReviewStatusPendingReview ReviewStatus = "PENDING_REVIEW"
	ReviewStatusApproved      ReviewStatus = "APPROVED"
)

// Provenance is the authorship of a grading artifact (ADR-172 §D7). Drives the
// "AI comments" / "Human reviewer" badge. Empty string is treated as AI for
// back-compat with rows graded before this column existed.
type Provenance string

const (
	ProvenanceAI    Provenance = "AI"
	ProvenanceHuman Provenance = "HUMAN"
)

// IsHuman reports whether the artifact was last edited by a human.
func (p Provenance) IsHuman() bool { return p == ProvenanceHuman }

// Submission HITL domain errors (ADR-172).
var (
	// ErrSubmissionNotGraded — an override/approve was attempted before AI
	// grading landed (state must be GRADED_PENDING_RELEASE).
	ErrSubmissionNotGraded = errors.New("delivery: submission not graded (cannot review)")

	// ErrSubmissionAlreadyApproved — an edit was attempted after the
	// submission's grading was approved.
	ErrSubmissionAlreadyApproved = errors.New("delivery: submission grading already approved")

	// ErrOEAnswerNotFound — an override targeted a test_set_question_id that
	// is not an OE answer on this submission.
	ErrOEAnswerNotFound = errors.New("delivery: oe answer not found for test_set_question_id")

	// ErrScoreOutOfRange — an override score is negative or exceeds points_possible.
	ErrScoreOutOfRange = errors.New("delivery: override score out of range [0, points_possible]")
)

// Submission domain errors.
var (
	// ErrSubmissionRequiresInProgress — autosave / submit / etc require
	// IN_PROGRESS (or STARTED) state.
	ErrSubmissionRequiresInProgress = errors.New("delivery: submission not in IN_PROGRESS state")

	// ErrSubmissionWindowClosed — submission's window has elapsed and
	// further autosave is rejected.
	ErrSubmissionWindowClosed = errors.New("delivery: submission window closed")

	// ErrSubmissionAlreadySubmitted — submit called after SUBMITTED state.
	ErrSubmissionAlreadySubmitted = errors.New("delivery: submission already submitted (idempotent)")

	// ErrSubmissionLearnerRequired — empty learner_gcid.
	ErrSubmissionLearnerRequired = errors.New("delivery: learner_gcid required")

	// ErrSubmissionAssessmentRequired — empty assessment_id.
	ErrSubmissionAssessmentRequired = errors.New("delivery: assessment_id required")

	// ErrSubmissionForbidden — caller not authorised (learner mismatch).
	ErrSubmissionForbidden = errors.New("delivery: caller not authorised for this submission")

	// ErrSubmissionRequiredQuestionsUnanswered — submit with required
	// questions blank.
	ErrSubmissionRequiredQuestionsUnanswered = errors.New("delivery: required questions unanswered")

	// ErrMCQSnapshotMissing — fail-loud sentinel returned by
	// GradeMCQAnswersFromSnapshot when an MCQ answer has no corresponding
	// test_set_question_id snapshot. Per `feedback_no_stubs_real_wiring`:
	// never silently green-path a missing snapshot — surface the gap so the
	// HTTP layer can emit a structured error to the outbox. The per-answer
	// score is forced to 0 in that case.
	ErrMCQSnapshotMissing = errors.New("delivery: mcq snapshot missing for test_set_question_id (cannot grade — see test_set_questions.payload_snapshot)")
)

// SubmissionAnswer is a per-question response within a submission.
//
// Per the OpenAPI SubmissionAnswer schema: `mcq_choice_id` set for MCQs;
// `oe_response_text` for OE. Both nullable so partial autosave is valid.
type SubmissionAnswer struct {
	TestSetQuestionID string
	QuestionID        string
	QuestionType      QuestionType
	MCQChoiceID       string   // single-correct
	MCQChoiceIDs      []string // multi-correct
	OEResponseText    string
	AnsweredAt        *time.Time

	// Grading fields (filled by inline MCQ + inbox OE).
	MCQCorrect      *bool
	PointsEarned    float64
	PointsPossible  int
	OEFeedback      string
	OECriterionJSON string // raw JSON criterion_scores per oe_batch_completed
	GradingDispatch GradingDispatch
	OEGradedAt      *time.Time

	// ADR-172 HITL provenance + override fields (OE only).
	OEComment             string     // per-question grader comment — always present for a graded OE
	AIComment             string     // original AI comment, preserved when a human edits OEComment
	AIPointsEarned        *float64   // original AI composite, preserved when a human overrides the score
	ScoreProvenance       Provenance // AI by default; HUMAN after a score override
	CommentProvenance     Provenance // AI by default; HUMAN after a comment edit
	AmendedModelAnswer    string     // instructor-corrected model answer (empty = use authored snapshot)
	ModelAnswerProvenance Provenance // AI by default; HUMAN after a model-answer amendment
	QualityFlagged        bool       // evaluator→moderator loop exhausted retries — priority review
	GradingModelID        string     // LLM provenance (IMDA D2)
	GradingResponseID     string     // LLM provenance (IMDA D2)
	OverriddenByGCID      string     // last instructor who edited this answer
	OverriddenAt          *time.Time
}

// Submission is the per-learner attempt aggregate.
type Submission struct {
	ID             string
	AssessmentID   string
	TenantID       string
	LearnerGCID    string
	AttemptNumber  int
	State          SubmissionState
	OpensAt        time.Time
	ClosesAt       time.Time
	TimeLimitSecs  int
	StartedAt      time.Time
	LastSavedAt    *time.Time
	SubmittedAt    *time.Time
	GradedAt       *time.Time
	ReleasedAt     *time.Time
	MCQScore       float64
	OEScore        float64
	TotalScore     float64
	MaxScore       int
	PassingPercent int
	Passed         *bool
	Answers        []SubmissionAnswer
	DeletedAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time

	// ADR-172 HITL gate + overall comment.
	ReviewStatus             ReviewStatus // "" (MCQ-only) | PENDING_REVIEW | APPROVED
	ApprovedByGCID           string
	ApprovedAt               *time.Time
	OverallComment           string     // whole-assessment narrative (current value)
	AIOverallComment         string     // original AI-drafted overall comment
	OverallCommentProvenance Provenance // AI by default; HUMAN after an instructor edit
}

// NewSubmissionInput is the constructor payload.
type NewSubmissionInput struct {
	AssessmentID   string
	TenantID       string
	LearnerGCID    string
	AttemptNumber  int
	OpensAt        time.Time
	ClosesAt       time.Time
	MaxScore       int
	PassingPercent int
}

// NewSubmission constructs a fresh STARTED submission.
//
// Returns:
//   - ErrSubmissionAssessmentRequired / ErrSubmissionLearnerRequired
//     for missing required fields.
func NewSubmission(in NewSubmissionInput) (*Submission, error) {
	if strings.TrimSpace(in.AssessmentID) == "" {
		return nil, ErrSubmissionAssessmentRequired
	}
	if strings.TrimSpace(in.LearnerGCID) == "" {
		return nil, ErrSubmissionLearnerRequired
	}
	now := time.Now().UTC()
	if in.AttemptNumber < 1 {
		in.AttemptNumber = 1
	}
	s := &Submission{
		ID:             NewUUIDv7(),
		AssessmentID:   in.AssessmentID,
		TenantID:       in.TenantID,
		LearnerGCID:    in.LearnerGCID,
		AttemptNumber:  in.AttemptNumber,
		State:          SubmissionStateStarted,
		OpensAt:        in.OpensAt.UTC(),
		ClosesAt:       in.ClosesAt.UTC(),
		MaxScore:       in.MaxScore,
		PassingPercent: in.PassingPercent,
		StartedAt:      now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if !in.OpensAt.IsZero() && !in.ClosesAt.IsZero() {
		s.TimeLimitSecs = int(in.ClosesAt.Sub(in.OpensAt).Seconds())
		if s.TimeLimitSecs < 0 {
			s.TimeLimitSecs = 0
		}
	}
	return s, nil
}

// TimeRemainingSeconds returns the remaining window seconds (0 once closed
// or post-submit).
func (s *Submission) TimeRemainingSeconds(now time.Time) int {
	if s.State != SubmissionStateStarted && s.State != SubmissionStateInProgress {
		return 0
	}
	if s.ClosesAt.IsZero() || now.IsZero() {
		return 0
	}
	remaining := int(s.ClosesAt.Sub(now).Seconds())
	if remaining < 0 {
		return 0
	}
	return remaining
}

// CanAutosave reports whether autosave is currently allowed.
func (s *Submission) CanAutosave(now time.Time) error {
	if s.State != SubmissionStateStarted && s.State != SubmissionStateInProgress {
		return ErrSubmissionRequiresInProgress
	}
	if !s.ClosesAt.IsZero() && now.After(s.ClosesAt) {
		return ErrSubmissionWindowClosed
	}
	return nil
}

// MergeAnswers is the idempotent autosave operation. Each item in `incoming`
// overrides any existing answer for the same TestSetQuestionID. Bumps
// LastSavedAt + flips STARTED → IN_PROGRESS on the first save.
func (s *Submission) MergeAnswers(now time.Time, incoming []SubmissionAnswer) error {
	if err := s.CanAutosave(now); err != nil {
		return err
	}
	if len(incoming) == 0 {
		return nil
	}
	// Index existing by test_set_question_id.
	idx := make(map[string]int, len(s.Answers))
	for i, ex := range s.Answers {
		idx[ex.TestSetQuestionID] = i
	}
	for _, in := range incoming {
		if strings.TrimSpace(in.TestSetQuestionID) == "" {
			continue
		}
		t := now.UTC()
		in.AnsweredAt = &t
		if i, ok := idx[in.TestSetQuestionID]; ok {
			// Preserve any grading fields if already filled
			existing := s.Answers[i]
			in.MCQCorrect = existing.MCQCorrect
			in.PointsEarned = existing.PointsEarned
			in.PointsPossible = existing.PointsPossible
			in.OEFeedback = existing.OEFeedback
			in.OEGradedAt = existing.OEGradedAt
			s.Answers[i] = in
		} else {
			s.Answers = append(s.Answers, in)
		}
	}
	t := now.UTC()
	s.LastSavedAt = &t
	s.UpdatedAt = t
	if s.State == SubmissionStateStarted {
		s.State = SubmissionStateInProgress
	}
	return nil
}

// MarkSubmitted transitions IN_PROGRESS/STARTED → SUBMITTED.
// Idempotent: re-submitting an already-SUBMITTED submission returns nil.
func (s *Submission) MarkSubmitted(now time.Time) error {
	switch s.State {
	case SubmissionStateSubmitted,
		SubmissionStatePendingOEGrading,
		SubmissionStateGradedPendingRelease,
		SubmissionStateReleased:
		return ErrSubmissionAlreadySubmitted // idempotent — caller maps to 200
	case SubmissionStateStarted, SubmissionStateInProgress:
		t := now.UTC()
		s.State = SubmissionStateSubmitted
		s.SubmittedAt = &t
		s.UpdatedAt = t
		return nil
	default:
		return ErrSubmissionRequiresInProgress
	}
}

// HasOEAnswers reports whether any answer is OE-typed.
func (s *Submission) HasOEAnswers() bool {
	for _, a := range s.Answers {
		if a.QuestionType == QuestionTypeOE && strings.TrimSpace(a.OEResponseText) != "" {
			return true
		}
	}
	return false
}

// MoveToOEPending transitions SUBMITTED → PENDING_OE_GRADING (when at least
// one OE answer needs grading).
func (s *Submission) MoveToOEPending(now time.Time) {
	if s.State == SubmissionStateSubmitted {
		s.State = SubmissionStatePendingOEGrading
		s.UpdatedAt = now.UTC()
	}
}

// MarkGradedPendingRelease transitions to GRADED_PENDING_RELEASE.
// Called after MCQ inline grading (when no OE) or after the OE batch
// completion inbox event fills OE answers.
//
// Computes TotalScore + Passed.
func (s *Submission) MarkGradedPendingRelease(now time.Time) {
	switch s.State {
	case SubmissionStateSubmitted, SubmissionStatePendingOEGrading:
		// allowed
	default:
		return
	}
	s.recomputeTotals()
	t := now.UTC()
	s.GradedAt = &t
	s.State = SubmissionStateGradedPendingRelease
	s.UpdatedAt = t
}

// recomputeTotals re-derives MCQScore / OEScore / TotalScore / Passed from the
// current per-answer PointsEarned. Idempotent; called both at AI grading time
// and after a HITL score override (which mutates a per-answer score).
func (s *Submission) recomputeTotals() {
	mcq, oe := 0.0, 0.0
	for _, a := range s.Answers {
		switch a.QuestionType {
		case QuestionTypeMCQ:
			mcq += a.PointsEarned
		case QuestionTypeOE:
			oe += a.PointsEarned
		}
	}
	s.MCQScore = mcq
	s.OEScore = oe
	s.TotalScore = mcq + oe
	if s.MaxScore > 0 {
		percent := (s.TotalScore / float64(s.MaxScore)) * 100
		passed := percent >= float64(s.PassingPercent)
		s.Passed = &passed
	}
}

// OEGradingResult is a single graded OE-answer entry inside an OE-batch
// completion event. Field names mirror QuestionGradeBatchResult in
// chora-contracts/proto/events-flat/delivery/grading/oe_batch_completed.proto.
type OEGradingResult struct {
	TestSetQuestionID    string
	QuestionID           string
	PointsEarned         float64
	PointsPossible       int
	CriterionScoresJSON  string
	LLMEvaluatorFeedback string
}

// OEGradingBatch is the projected payload of the
// chora.delivery.grading.oe_batch_completed.v1 event. Mirrors the load-bearing
// fields of GradingOeBatchCompleted in chora-contracts.
//
// BatchOutcome carries the orchestrator's outcome: "ok" (results populated),
// "failed" (FailureMessage populated, Results empty), or "skipped" (mana /
// guardrail veto). For the demo we treat anything != "ok" as a no-op grade
// pass — the FSM stays in PENDING_OE_GRADING so a retry can drive it forward.
type OEGradingBatch struct {
	SubmissionID   string
	AssessmentID   string
	BatchOutcome   string
	FailureMessage string
	GradedAt       time.Time
	Results        []OEGradingResult
}

// ErrSubmissionMismatch — ApplyOEGradingBatch invoked with a batch whose
// SubmissionID does not match the aggregate's own ID. Subscriber treats this
// as a routing bug and Nacks (Pub/Sub will redeliver to the correct
// subscriber).
var ErrSubmissionMismatch = errors.New("delivery: oe batch submission_id does not match aggregate id")

// ApplyOEGradingBatch is the load-bearing aggregate operation for the
// completion-event inbox flow (chora.delivery.grading.oe_batch_completed.v1).
//
// Per ADR-155 §"Locked architectural rule" + .claude/rules/ddd-enforcement.md
// #2 — the inbox subscriber MUST go through the Submission aggregate; it
// MUST NOT touch submission_answers directly.
//
// Behaviour:
//   - Each OEGradingResult is matched to an OE answer by TestSetQuestionID.
//     PointsEarned / OEFeedback / OECriterionJSON / OEGradedAt are written
//     on the matched answer; GradingDispatch is stamped LLM_EVALUATOR.
//   - MCQ answers are NEVER overwritten — even if a result accidentally
//     targets an MCQ test_set_question_id (defensive).
//   - When ALL OE answers (those with non-empty OEResponseText) have
//     OEGradedAt set, the aggregate advances to GRADED_PENDING_RELEASE via
//     MarkGradedPendingRelease (which computes MCQScore + OEScore +
//     TotalScore + Passed). Returns allGraded=true in that case.
//   - On a failed batch (BatchOutcome != "ok") the FSM stays
//     PENDING_OE_GRADING and no scores change. Returns allGraded=false.
//
// Idempotent: re-applying the same batch keeps the same final state. The
// underlying SubmissionAnswer.OEGradedAt is set unconditionally on a
// successful match (so the timestamp updates on replay, which is fine for
// audit) but PointsEarned / OEFeedback / OECriterionJSON are deterministic
// over the same input so no double-counting can occur.
//
// Returns ErrSubmissionMismatch when the batch's SubmissionID doesn't match
// the aggregate.
func (s *Submission) ApplyOEGradingBatch(now time.Time, in OEGradingBatch) (allGraded bool, err error) {
	if s == nil {
		return false, errors.New("delivery: nil submission")
	}
	if strings.TrimSpace(in.SubmissionID) != "" && in.SubmissionID != s.ID {
		return false, ErrSubmissionMismatch
	}
	// Failed batches do NOT mutate scores; subscriber records the failure
	// via the outbox (separate path) + Pub/Sub retries until the orchestrator
	// re-issues an "ok" batch.
	if !strings.EqualFold(in.BatchOutcome, "ok") {
		return false, nil
	}
	// Index OE answers by TestSetQuestionID for O(1) lookup.
	oeIdx := make(map[string]int, len(s.Answers))
	for i, a := range s.Answers {
		if a.QuestionType == QuestionTypeOE {
			oeIdx[a.TestSetQuestionID] = i
		}
	}
	gradedAt := now.UTC()
	if !in.GradedAt.IsZero() {
		gradedAt = in.GradedAt.UTC()
	}
	for _, r := range in.Results {
		i, ok := oeIdx[r.TestSetQuestionID]
		if !ok {
			// Defensive: ignore MCQ-targeted results (would be a producer
			// bug) + ignore unknown question ids (similar).
			continue
		}
		ans := &s.Answers[i]
		ans.PointsEarned = r.PointsEarned
		if r.PointsPossible > 0 && ans.PointsPossible == 0 {
			ans.PointsPossible = r.PointsPossible
		}
		ans.OEFeedback = r.LLMEvaluatorFeedback
		ans.OECriterionJSON = r.CriterionScoresJSON
		ans.OEGradedAt = &gradedAt
		ans.GradingDispatch = GradingDispatchLLMEvaluator
	}
	// Aggregate completeness check — every OE answer with response text must
	// now have OEGradedAt set.
	allGraded = true
	for _, a := range s.Answers {
		if a.QuestionType != QuestionTypeOE {
			continue
		}
		if strings.TrimSpace(a.OEResponseText) == "" {
			continue
		}
		if a.OEGradedAt == nil {
			allGraded = false
			break
		}
	}
	if allGraded {
		s.MarkGradedPendingRelease(now)
	} else {
		// Bump UpdatedAt so the persistence layer captures the partial state.
		s.UpdatedAt = now.UTC()
	}
	return allGraded, nil
}

// ============================================================================
// ADR-172 — per-submission grading + HITL grade-review
// ============================================================================

// OEQuestionGrade is one graded OE question inside a per-submission grading
// completion (GradingSubmissionCompleted / submission_completed.v1).
type OEQuestionGrade struct {
	TestSetQuestionID   string
	QuestionID          string
	PointsEarned        float64
	PointsPossible      int
	CriterionScoresJSON string
	Comment             string // per-question grader comment — always present
	GradingModelID      string
	GradingResponseID   string
	QualityFlagged      bool
}

// SubmissionGrading is the projected payload of
// chora.delivery.grading.submission_completed.v1 (ADR-172). The orchestrator's
// evaluator→moderator loop produces per-OE-question results + a whole-assessment
// overall comment.
//
// Outcome is one of SUCCESS / PARTIAL / FAILED. FAILED is a no-op grade pass —
// the FSM stays PENDING_OE_GRADING so a retry can drive it forward.
type SubmissionGrading struct {
	SubmissionID             string
	AssessmentID             string
	Outcome                  string
	FailureMessage           string
	OverallComment           string
	OverallCommentModelID    string
	OverallCommentResponseID string
	GradedAt                 time.Time
	Graded                   []OEQuestionGrade
}

// ApplyGrading is the load-bearing inbox operation for the per-submission OE
// grading flow (ADR-172, supersedes ApplyOEGradingBatch). It stamps each OE
// answer's AI score + comment + per-criterion scores + LLM provenance (with
// provenance=AI and the AI originals preserved for later HITL diffing), records
// the overall comment, and — when every OE answer is graded — advances to
// GRADED_PENDING_RELEASE with review_status=PENDING_REVIEW (the mandatory HITL
// gate). MCQ answers are never touched.
//
// Idempotent over the same input. FAILED outcome leaves the FSM untouched.
func (s *Submission) ApplyGrading(now time.Time, in SubmissionGrading) (allGraded bool, err error) {
	if s == nil {
		return false, errors.New("delivery: nil submission")
	}
	if strings.TrimSpace(in.SubmissionID) != "" && in.SubmissionID != s.ID {
		return false, ErrSubmissionMismatch
	}
	if strings.EqualFold(in.Outcome, "FAILED") {
		return false, nil
	}
	oeIdx := make(map[string]int, len(s.Answers))
	for i, a := range s.Answers {
		if a.QuestionType == QuestionTypeOE {
			oeIdx[a.TestSetQuestionID] = i
		}
	}
	gradedAt := now.UTC()
	if !in.GradedAt.IsZero() {
		gradedAt = in.GradedAt.UTC()
	}
	for _, r := range in.Graded {
		i, ok := oeIdx[r.TestSetQuestionID]
		if !ok {
			continue // defensive: ignore MCQ-targeted / unknown ids
		}
		ans := &s.Answers[i]
		ans.PointsEarned = r.PointsEarned
		aiPts := r.PointsEarned
		ans.AIPointsEarned = &aiPts
		if r.PointsPossible > 0 && ans.PointsPossible == 0 {
			ans.PointsPossible = r.PointsPossible
		}
		ans.OEComment = r.Comment
		ans.AIComment = r.Comment
		ans.OEFeedback = r.Comment // legacy mirror for older result projections
		ans.OECriterionJSON = r.CriterionScoresJSON
		ans.GradingModelID = r.GradingModelID
		ans.GradingResponseID = r.GradingResponseID
		ans.QualityFlagged = r.QualityFlagged
		ans.ScoreProvenance = ProvenanceAI
		ans.CommentProvenance = ProvenanceAI
		ans.ModelAnswerProvenance = ProvenanceAI
		ans.OEGradedAt = &gradedAt
		ans.GradingDispatch = GradingDispatchLLMEvaluator
	}
	if strings.TrimSpace(in.OverallComment) != "" {
		s.OverallComment = in.OverallComment
		s.AIOverallComment = in.OverallComment
		s.OverallCommentProvenance = ProvenanceAI
	}
	allGraded = true
	for _, a := range s.Answers {
		if a.QuestionType != QuestionTypeOE || strings.TrimSpace(a.OEResponseText) == "" {
			continue
		}
		if a.OEGradedAt == nil {
			allGraded = false
			break
		}
	}
	if allGraded {
		s.MarkGradedPendingRelease(now)
		s.ReviewStatus = ReviewStatusPendingReview // ADR-172 §D6 mandatory HITL gate
	} else {
		s.UpdatedAt = now.UTC()
	}
	return allGraded, nil
}

// GradeOverride is one append-only HITL audit entry (ADR-172 §D7). The aggregate
// returns these from override methods; the handler persists them to
// grade_overrides and emits score_overridden.v1 (+ model_answer_amended.v1 for
// MODEL_ANSWER edits).
type GradeOverride struct {
	Field             string // SCORE | COMMENT | MODEL_ANSWER | OVERALL_COMMENT
	TestSetQuestionID string // empty for OVERALL_COMMENT
	QuestionID        string
	OldValue          string
	NewValue          string
	ActorGCID         string
	Reason            string
	At                time.Time
}

// requireReviewable guards HITL edits — grading must have landed and the
// submission must not already be approved.
func (s *Submission) requireReviewable() error {
	if s.State != SubmissionStateGradedPendingRelease {
		return ErrSubmissionNotGraded
	}
	if s.ReviewStatus == ReviewStatusApproved {
		return ErrSubmissionAlreadyApproved
	}
	return nil
}

func (s *Submission) findOEAnswer(tsqID string) int {
	for i := range s.Answers {
		if s.Answers[i].QuestionType == QuestionTypeOE && s.Answers[i].TestSetQuestionID == tsqID {
			return i
		}
	}
	return -1
}

// OverrideQuestionGrade applies an opt-in instructor edit to an OE answer's
// score and/or comment. Flips the affected artifact's provenance AI→HUMAN,
// preserving the AI original, and re-derives totals when the score changes.
// Returns the audit entries describing what changed.
func (s *Submission) OverrideQuestionGrade(now time.Time, actorGCID, tsqID string, newScore *float64, newComment *string, reason string) ([]GradeOverride, error) {
	if err := s.requireReviewable(); err != nil {
		return nil, err
	}
	i := s.findOEAnswer(tsqID)
	if i < 0 {
		return nil, ErrOEAnswerNotFound
	}
	ans := &s.Answers[i]
	t := now.UTC()
	var out []GradeOverride
	if newScore != nil {
		if *newScore < 0 || (ans.PointsPossible > 0 && *newScore > float64(ans.PointsPossible)) {
			return nil, ErrScoreOutOfRange
		}
		old := strconv.FormatFloat(ans.PointsEarned, 'f', -1, 64)
		ans.PointsEarned = *newScore
		ans.ScoreProvenance = ProvenanceHuman
		out = append(out, GradeOverride{Field: "SCORE", TestSetQuestionID: tsqID, QuestionID: ans.QuestionID,
			OldValue: old, NewValue: strconv.FormatFloat(*newScore, 'f', -1, 64), ActorGCID: actorGCID, Reason: reason, At: t})
	}
	if newComment != nil {
		old := ans.OEComment
		ans.OEComment = *newComment
		ans.CommentProvenance = ProvenanceHuman
		out = append(out, GradeOverride{Field: "COMMENT", TestSetQuestionID: tsqID, QuestionID: ans.QuestionID,
			OldValue: old, NewValue: *newComment, ActorGCID: actorGCID, Reason: reason, At: t})
	}
	if len(out) == 0 {
		return nil, nil
	}
	ans.OverriddenByGCID = actorGCID
	ans.OverriddenAt = &t
	s.recomputeTotals()
	s.UpdatedAt = t
	return out, nil
}

// AmendModelAnswer records an instructor correction to an OE question's
// canonical model answer (scoped to this grading record). Flips
// ModelAnswerProvenance AI→HUMAN; the handler emits model_answer_amended.v1 so
// chora-creation appends a QuestionRevision (ADR-172 §D8).
func (s *Submission) AmendModelAnswer(now time.Time, actorGCID, tsqID, newModelAnswer string) ([]GradeOverride, error) {
	if err := s.requireReviewable(); err != nil {
		return nil, err
	}
	i := s.findOEAnswer(tsqID)
	if i < 0 {
		return nil, ErrOEAnswerNotFound
	}
	ans := &s.Answers[i]
	t := now.UTC()
	old := ans.AmendedModelAnswer
	ans.AmendedModelAnswer = newModelAnswer
	ans.ModelAnswerProvenance = ProvenanceHuman
	ans.OverriddenByGCID = actorGCID
	ans.OverriddenAt = &t
	s.UpdatedAt = t
	return []GradeOverride{{Field: "MODEL_ANSWER", TestSetQuestionID: tsqID, QuestionID: ans.QuestionID,
		OldValue: old, NewValue: newModelAnswer, ActorGCID: actorGCID, At: t}}, nil
}

// EditOverallComment overrides the AI-drafted whole-assessment overall comment.
func (s *Submission) EditOverallComment(now time.Time, actorGCID, newComment string) ([]GradeOverride, error) {
	if err := s.requireReviewable(); err != nil {
		return nil, err
	}
	t := now.UTC()
	old := s.OverallComment
	s.OverallComment = newComment
	s.OverallCommentProvenance = ProvenanceHuman
	s.UpdatedAt = t
	return []GradeOverride{{Field: "OVERALL_COMMENT", OldValue: old, NewValue: newComment, ActorGCID: actorGCID, At: t}}, nil
}

// ApproveAsIs marks the submission's grading approved (review_status →
// APPROVED), satisfying the HITL gate so /release-results can release it.
// Idempotent on an already-approved submission.
func (s *Submission) ApproveAsIs(now time.Time, actorGCID string) error {
	if s.State == SubmissionStateReleased {
		return nil
	}
	if s.State != SubmissionStateGradedPendingRelease {
		return ErrSubmissionNotGraded
	}
	if s.ReviewStatus == ReviewStatusApproved {
		return nil
	}
	t := now.UTC()
	s.ReviewStatus = ReviewStatusApproved
	s.ApprovedByGCID = actorGCID
	s.ApprovedAt = &t
	s.UpdatedAt = t
	return nil
}

// MarkReleased transitions GRADED_PENDING_RELEASE → RELEASED. Called by the
// parent assessment's /release-results handler. Idempotent.
func (s *Submission) MarkReleased(now time.Time) {
	if s.State == SubmissionStateReleased {
		return
	}
	if s.State != SubmissionStateGradedPendingRelease {
		return // skip — only release the graded ones (per ADR-155 §D9)
	}
	// ADR-172 §D6 — mandatory HITL gate: a submission whose OE answers were
	// AI-graded cannot release until an instructor approves it. MCQ-only
	// submissions carry ReviewStatusNotRequired ("") and release freely.
	if s.ReviewStatus == ReviewStatusPendingReview {
		return
	}
	t := now.UTC()
	s.State = SubmissionStateReleased
	s.ReleasedAt = &t
	s.UpdatedAt = t
}

// CanRelease reports whether MarkReleased would actually release this
// submission (graded + not gated on a pending review). Lets the release-results
// handler skip-and-report rather than silently no-op.
func (s *Submission) CanRelease() bool {
	if s.State == SubmissionStateReleased {
		return true
	}
	return s.State == SubmissionStateGradedPendingRelease && s.ReviewStatus != ReviewStatusPendingReview
}

// IsPendingRelease reports whether the learner GET /result should return the
// PENDING_RELEASE envelope (graded but not yet released) vs the RELEASED
// full body.
func (s *Submission) IsPendingRelease() bool {
	switch s.State {
	case SubmissionStateSubmitted, SubmissionStatePendingOEGrading,
		SubmissionStateGradedPendingRelease:
		return true
	}
	return false
}

// ScorePercent returns the score as a 0-100 percentage (0 if not graded
// or MaxScore = 0).
func (s *Submission) ScorePercent() float64 {
	if s.MaxScore <= 0 {
		return 0
	}
	return (s.TotalScore / float64(s.MaxScore)) * 100
}

// ----------------------------------------------------------------------------
// MCQSnapshot — projected MCQPayload from chora_creation, cached at
// chora_delivery.test_set_questions.payload_snapshot at TestSet.Publish() time
// (per migrations/0011_test_set_questions_snapshot.up.sql + the
// chora.services.creation.v1.Creation/SnapshotQuestionByID gRPC).
//
// Per ddd-enforcement #3: cross-DB queries are FORBIDDEN. The snapshot is the
// only mechanism by which chora-delivery sees the canonical answer key — the
// runtime grading path NEVER touches chora_creation.
//
// Shape mirrors chora-contracts/openapi/creation-questions.yaml MCQPayload:
//
//	{
//	  "options": [
//	    {"option_id": "<uuid>", "label": "A", "text": "...", "is_correct": true, "explainer": "..."},
//	    ...
//	  ],
//	  "scoring": {"mode": "single_correct"|"multi_correct"|"all_or_nothing"}
//	}
//
// Unmarshalling is lenient (extra keys ignored) so additive changes to
// MCQPayload in chora_creation never break delivery grading.
// ----------------------------------------------------------------------------

// MCQSnapshot is the deterministic-grading view over an MCQ question's
// canonical payload as captured by chora-delivery at TestSet.Publish() time.
type MCQSnapshot struct {
	// Options preserves the option objects (each carries `option_id` +
	// `is_correct`). Type is map[string]any because the producer schema is
	// additive — we deliberately do NOT bind to a struct so MCQOption can
	// gain fields in chora-creation without breaking delivery's grader.
	Options []map[string]any

	// ScoringMode mirrors MCQScoring.mode in OpenAPI. One of:
	//   - single_correct  (default; exactly one correct option)
	//   - multi_correct   (multiple correct; all_or_nothing fallback)
	//   - all_or_nothing  (every correct must be selected, no extras)
	ScoringMode string

	// AnswerImageURL is the OPTIONAL model-answer illustration captured from
	// the MCQ payload's top-level `answer_image_url` (W8 image-gen). Surfaced
	// post-RELEASE on mcq_post_grade.answer_image_url so the learner sees the
	// generated model-answer image in their result reveal. nil when absent.
	// Field name is IDENTICAL across every layer (snapshot JSON tag → DTO json
	// tag → FE model → template).
	AnswerImageURL *string

	// ImageURL is the OPTIONAL question/stem illustration captured from the
	// MCQ payload's top-level `image_url` (W8 image-gen). The learner sees it
	// while TAKING the assessment (prompt.image_url); surfaced again on the
	// result reveal as `question_image_url` so the review shows the question
	// WITH its picture, not just the model-answer image. nil when absent.
	ImageURL *string
}

// CorrectOptionIDs returns the option_ids of every option flagged
// is_correct=true. Stable ordering across calls is NOT guaranteed (the
// MCQ grader does set-equality).
func (s MCQSnapshot) CorrectOptionIDs() []string {
	out := make([]string, 0, len(s.Options))
	for _, opt := range s.Options {
		correct, _ := opt["is_correct"].(bool)
		if !correct {
			continue
		}
		switch v := opt["option_id"].(type) {
		case string:
			if v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// MCQSnapshotFromJSON parses the canonical MCQPayload JSON shape (as stored
// in chora_delivery.test_set_questions.payload_snapshot at Publish() time)
// into the strongly-typed MCQSnapshot.
//
// Lenient on shape — additional MCQPayload fields are ignored so this stays
// additive-stable per chora-contracts/CLAUDE.md §8 versioning rules.
//
// Returns an error when:
//   - the input is invalid JSON
//   - `options` is missing / empty
func MCQSnapshotFromJSON(payload string) (MCQSnapshot, error) {
	var doc struct {
		Options []map[string]any `json:"options"`
		Scoring struct {
			Mode string `json:"mode"`
		} `json:"scoring"`
		// AnswerImageURL — OPTIONAL W8 model-answer illustration. Field name is
		// IDENTICAL across every layer. Lenient: absent → nil.
		AnswerImageURL *string `json:"answer_image_url,omitempty"`
		// ImageURL — OPTIONAL W8 question/stem illustration (top-level, same
		// place the take path reads it). Surfaced on the result reveal as
		// question_image_url. Lenient: absent → nil.
		ImageURL *string `json:"image_url,omitempty"`
	}
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return MCQSnapshot{}, fmt.Errorf("delivery: parse mcq snapshot: %w", err)
	}
	if len(doc.Options) == 0 {
		return MCQSnapshot{}, errors.New("delivery: mcq snapshot has no options")
	}
	mode := doc.Scoring.Mode
	if mode == "" {
		mode = "single_correct"
	}
	return MCQSnapshot{Options: doc.Options, ScoringMode: mode, AnswerImageURL: doc.AnswerImageURL, ImageURL: doc.ImageURL}, nil
}

// GradeMCQAnswersFromSnapshot is the deterministic grader called at /submit
// time. It walks every MCQ answer in the submission, looks up the
// corresponding snapshot by test_set_question_id, and updates
// PointsEarned + MCQCorrect + GradingDispatch in place.
//
// Behaviour contract (per Fix-F and `feedback_no_stubs_real_wiring`):
//
//   - For each MCQ answer with a snapshot: grade per scoring_mode. Result
//     is exact-match (no partial credit) for the v1 demo; future
//     proportional-credit policies hook here once chora-creation surfaces
//     `partial_credit_policy` in the snapshot.
//   - For each MCQ answer WITHOUT a snapshot: PointsEarned = 0,
//     MCQCorrect = nil, return ErrMCQSnapshotMissing. The caller (HTTP
//     handler) MUST surface this as a structured error event — DO NOT
//     mask. The placeholder "award full credit on any selection" path is
//     gone; never recreate it.
//   - OE answers are NOT graded here — the OE batch event flow handles them.
//
// Returns:
//   - nil on success (every MCQ answer graded)
//   - the first ErrMCQSnapshotMissing encountered (other answers still
//     graded in the same pass; caller can decide whether to fail the
//     submit or continue with partial grading)
func (s *Submission) GradeMCQAnswersFromSnapshot(snapshots map[string]MCQSnapshot) error {
	var firstErr error
	for i := range s.Answers {
		ans := &s.Answers[i]
		if ans.QuestionType != QuestionTypeMCQ {
			continue
		}
		snap, ok := snapshots[ans.TestSetQuestionID]
		if !ok {
			ans.PointsEarned = 0
			ans.MCQCorrect = nil
			ans.GradingDispatch = GradingDispatchDeterministic
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: test_set_question_id=%s", ErrMCQSnapshotMissing, ans.TestSetQuestionID)
			}
			continue
		}
		correctIDs := snap.CorrectOptionIDs()
		pts, correct := GradeMCQAnswer(
			ans.MCQChoiceID, ans.MCQChoiceIDs,
			correctIDs, ans.PointsPossible, snap.ScoringMode,
		)
		ans.PointsEarned = pts
		c := correct
		ans.MCQCorrect = &c
		ans.GradingDispatch = GradingDispatchDeterministic
	}
	return firstErr
}

// GradeMCQAnswer is the inline-grading helper for an MCQ answer. Returns
// the points earned + correctness boolean. The handler iterates submission
// answers against the canonical answer key (test-set snapshot) and calls
// this per answer.
func GradeMCQAnswer(learnerChoiceID string, learnerChoiceIDs []string,
	correctChoiceIDs []string, pointsPossible int, scoringMode string) (float64, bool) {

	if len(correctChoiceIDs) == 0 || pointsPossible <= 0 {
		return 0, false
	}
	// Build correct set
	correct := make(map[string]struct{}, len(correctChoiceIDs))
	for _, c := range correctChoiceIDs {
		correct[c] = struct{}{}
	}
	// Learner selections
	selected := make(map[string]struct{})
	if learnerChoiceID != "" {
		selected[learnerChoiceID] = struct{}{}
	}
	for _, s := range learnerChoiceIDs {
		if s != "" {
			selected[s] = struct{}{}
		}
	}
	if len(selected) == 0 {
		return 0, false
	}
	// single_correct: 1 selected == correct
	switch strings.ToLower(scoringMode) {
	case "multi_correct", "all_or_nothing":
		// All correct selected AND no extras
		if len(selected) != len(correct) {
			return 0, false
		}
		for s := range selected {
			if _, ok := correct[s]; !ok {
				return 0, false
			}
		}
		return float64(pointsPossible), true
	default: // single_correct
		if len(selected) != 1 {
			return 0, false
		}
		for s := range selected {
			if _, ok := correct[s]; ok {
				return float64(pointsPossible), true
			}
		}
		return 0, false
	}
}
