// assessment.go — Assessment aggregate root for the assessments + grading
// surface per ADR-155 (Test-Set Editor + Dual-Path Grading) §D7-§D9.
//
// Aggregate invariants (per .claude/rules/ddd-enforcement.md):
//   - Soft-delete only (DeletedAt set; ARCHIVED state).
//   - Cohort scoping is part of the aggregate (InvitedGCIDs + ClassID).
//   - results_released_at is one-way: NULL → SET; never cleared.
//   - State machine enforced by transition methods (see assessmentTransition).
//
// Cross-DB queries FORBIDDEN. test_set_id is a soft FK (UUID, no constraint);
// existence is validated by chora-creation/chora-delivery via gRPC at create
// time (out of scope for the demo path — Lane A test-sets writes will land in
// the same chora_delivery DB and the foreign-key invariant will be wired by
// instructor responsibility).
//
// Cohort eligibility (ADR-155 §D9):
//   - open-link        : class_id=null AND invited_gcids=[]
//   - class-bound      : class_id=UUID AND invited_gcids=[]
//   - explicit cohort  : class_id=null AND invited_gcids=[UUID, ...]
//   - intersection     : class_id=UUID AND invited_gcids=[UUID, ...]
//
// v1 Phyllis demo path: explicit cohort via invited_gcids (class binding
// reserved for M16+).
package delivery

import (
	"errors"
	"strings"
	"time"
)

// AssessmentState models the Assessment FSM per ADR-155 §D7.
type AssessmentState string

const (
	AssessmentStateDraft     AssessmentState = "DRAFT"
	AssessmentStateScheduled AssessmentState = "SCHEDULED"
	AssessmentStateOpen      AssessmentState = "OPEN"
	AssessmentStateClosed    AssessmentState = "CLOSED"
	AssessmentStateGrading   AssessmentState = "GRADING"
	AssessmentStateGraded    AssessmentState = "GRADED"
	AssessmentStateReleased  AssessmentState = "RELEASED"
	AssessmentStateArchived  AssessmentState = "ARCHIVED"
)

// IsValid reports whether the state is one of the canonical FSM states.
func (s AssessmentState) IsValid() bool {
	switch s {
	case AssessmentStateDraft, AssessmentStateScheduled, AssessmentStateOpen,
		AssessmentStateClosed, AssessmentStateGrading, AssessmentStateGraded,
		AssessmentStateReleased, AssessmentStateArchived:
		return true
	}
	return false
}

// IsEditable reports whether metadata edits are allowed in this state
// (DRAFT or SCHEDULED only per the OpenAPI contract).
func (s AssessmentState) IsEditable() bool {
	return s == AssessmentStateDraft || s == AssessmentStateScheduled
}

// Assessment domain errors.
var (
	// ErrAssessmentNotEditable — edits attempted past the editable window.
	ErrAssessmentNotEditable = errors.New("delivery: assessment not editable in current state")

	// ErrAssessmentInvalidWindow — opens_at/closes_at violate ordering.
	ErrAssessmentInvalidWindow = errors.New("delivery: invalid scheduling window")

	// ErrAssessmentNotDraft — operation requires DRAFT state.
	ErrAssessmentNotDraft = errors.New("delivery: assessment not in DRAFT state")

	// ErrAssessmentNotOpen — operation requires OPEN state.
	ErrAssessmentNotOpen = errors.New("delivery: assessment not in OPEN state")

	// ErrAssessmentNotYetOpen — an attempt was started before
	// scheduled_open_at. Distinct from ErrAssessmentWindowClosed so the
	// learner is told which end of the window they are on (CHO-2153 F6).
	ErrAssessmentNotYetOpen = errors.New("delivery: assessment window has not opened")

	// ErrAssessmentWindowClosed — an attempt was started at or after
	// scheduled_close_at (CHO-2153 F6).
	ErrAssessmentWindowClosed = errors.New("delivery: assessment window has closed")

	// ErrAssessmentNotGraded — release-results called before GRADED.
	ErrAssessmentNotGraded = errors.New("delivery: assessment not in GRADED state")

	// ErrAssessmentTestSetIDRequired — create without test_set_id.
	ErrAssessmentTestSetIDRequired = errors.New("delivery: test_set_id required")

	// ErrAssessmentTenantRequired — create without tenant_id.
	ErrAssessmentTenantRequired = errors.New("delivery: tenant_id required")

	// ErrAssessmentInstructorRequired — create without instructor_gcid.
	ErrAssessmentInstructorRequired = errors.New("delivery: instructor_gcid required")

	// ErrAssessmentTitleRequired — create with empty title.
	ErrAssessmentTitleRequired = errors.New("delivery: title required")

	// ErrAssessmentMaxAttemptsOutOfRange — max_attempts ∉ [1,3].
	ErrAssessmentMaxAttemptsOutOfRange = errors.New("delivery: max_attempts must be in [1,3]")

	// ErrAssessmentForbidden — caller lacks rights (cohort / role gate).
	ErrAssessmentForbidden = errors.New("delivery: caller not authorised for this assessment")
)

// GradingDispatch identifies how a question was graded (DETERMINISTIC for
// MCQ in-process; LLM_EVALUATOR for OE batched).
type GradingDispatch string

const (
	GradingDispatchDeterministic GradingDispatch = "DETERMINISTIC"
	GradingDispatchLLMEvaluator  GradingDispatch = "LLM_EVALUATOR"
)

// Accommodations matches the OpenAPI Accommodations schema.
type Accommodations struct {
	ExtraTimeMinutes int     `json:"extra_time_minutes,omitempty"`
	AllowCalculator  bool    `json:"allow_calculator,omitempty"`
	AllowOpenBook    bool    `json:"allow_open_book,omitempty"`
	FontScaleFactor  float64 `json:"font_scale_factor,omitempty"`
}

// GradingConfigSnapshot is the per-Assessment grading config locked at
// create-time (snapshot from test-set's default_grading_config).
type GradingConfigSnapshot struct {
	MCQDispatch                string `json:"mcq_dispatch"`             // "DETERMINISTIC"
	OEDispatch                 string `json:"oe_dispatch"`              // "LLM_EVALUATOR_AGENT"
	LLMEvaluatorModelTier      string `json:"llm_evaluator_model_tier"` // "T1" | "T2"
	PassingThresholdPercent    int    `json:"passing_threshold_percent"`
	PerQuestionFeedbackEnabled bool   `json:"per_question_feedback_enabled"`
	AutoRelease                bool   `json:"auto_release"`
}

// Assessment is the Content Delivery aggregate root tracking a live run of
// a published TestSet (per ADR-155 §D7 FSM).
type Assessment struct {
	ID                      string
	TenantID                string
	InstructorGCID          string
	TestSetID               string
	TestSetRevisionSnapshot int
	ClassID                 string   // nullable
	OfferingID              string   // nullable; W3.A delivery-Offering scope (soft FK to offerings; distinct from ClassID)
	InvitedGCIDs            []string // explicit cohort scoping (ADR-155 §D9)
	Title                   string
	LearnerFacingName       string
	State                   AssessmentState
	ScheduledOpenAt         time.Time
	ScheduledCloseAt        time.Time
	MaxAttempts             int
	ShuffleQuestions        bool
	ShuffleMCQOptions       bool
	Accommodations          *Accommodations
	TotalPoints             int
	QuestionCount           int
	GradingConfigSnapshot   GradingConfigSnapshot
	PublishedAt             *time.Time
	ClosedAt                *time.Time
	ResultsReleasedAt       *time.Time
	ArchivedAt              *time.Time
	ReleaseAnnouncement     string
	DeletedAt               *time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// NewAssessmentInput is the constructor payload.
type NewAssessmentInput struct {
	TenantID                string
	InstructorGCID          string
	TestSetID               string
	TestSetRevisionSnapshot int
	ClassID                 string
	OfferingID              string
	InvitedGCIDs            []string
	Title                   string
	LearnerFacingName       string
	ScheduledOpenAt         time.Time
	ScheduledCloseAt        time.Time
	MaxAttempts             int
	ShuffleQuestions        bool
	ShuffleMCQOptions       bool
	Accommodations          *Accommodations
	TotalPoints             int
	QuestionCount           int
	GradingConfigSnapshot   GradingConfigSnapshot
}

// NewAssessment constructs a DRAFT Assessment with full invariant checks.
//
// Returns:
//   - ErrAssessmentTenantRequired when TenantID empty
//   - ErrAssessmentInstructorRequired when InstructorGCID empty
//   - ErrAssessmentTestSetIDRequired when TestSetID empty
//   - ErrAssessmentTitleRequired when Title empty
//   - ErrAssessmentInvalidWindow when scheduled_open_at > scheduled_close_at,
//     or when scheduled_close_at is in the past
//   - ErrAssessmentMaxAttemptsOutOfRange when MaxAttempts ∉ [1,3]
func NewAssessment(in NewAssessmentInput) (*Assessment, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, ErrAssessmentTenantRequired
	}
	if strings.TrimSpace(in.InstructorGCID) == "" {
		return nil, ErrAssessmentInstructorRequired
	}
	if strings.TrimSpace(in.TestSetID) == "" {
		return nil, ErrAssessmentTestSetIDRequired
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, ErrAssessmentTitleRequired
	}
	if in.MaxAttempts < 1 || in.MaxAttempts > 3 {
		return nil, ErrAssessmentMaxAttemptsOutOfRange
	}
	if !in.ScheduledOpenAt.IsZero() && !in.ScheduledCloseAt.IsZero() {
		if !in.ScheduledCloseAt.After(in.ScheduledOpenAt) {
			return nil, ErrAssessmentInvalidWindow
		}
	}
	now := time.Now().UTC()
	a := &Assessment{
		ID:                      NewUUIDv7(),
		TenantID:                in.TenantID,
		InstructorGCID:          in.InstructorGCID,
		TestSetID:               in.TestSetID,
		TestSetRevisionSnapshot: in.TestSetRevisionSnapshot,
		ClassID:                 in.ClassID,
		OfferingID:              strings.TrimSpace(in.OfferingID),
		InvitedGCIDs:            sanitizeInvitedGCIDs(in.InvitedGCIDs),
		Title:                   in.Title,
		LearnerFacingName:       in.LearnerFacingName,
		State:                   AssessmentStateDraft,
		ScheduledOpenAt:         in.ScheduledOpenAt.UTC(),
		ScheduledCloseAt:        in.ScheduledCloseAt.UTC(),
		MaxAttempts:             in.MaxAttempts,
		ShuffleQuestions:        in.ShuffleQuestions,
		ShuffleMCQOptions:       in.ShuffleMCQOptions,
		Accommodations:          in.Accommodations,
		TotalPoints:             in.TotalPoints,
		QuestionCount:           in.QuestionCount,
		GradingConfigSnapshot:   in.GradingConfigSnapshot,
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if a.TestSetRevisionSnapshot < 1 {
		a.TestSetRevisionSnapshot = 1
	}
	return a, nil
}

// sanitizeInvitedGCIDs trims + dedups, dropping empties.
func sanitizeInvitedGCIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if _, ok := seen[g]; ok {
			continue
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CohortMode classifies the eligibility model per ADR-155 §D9.
type CohortMode string

const (
	CohortOpenLink     CohortMode = "OPEN_LINK"
	CohortClassBound   CohortMode = "CLASS_BOUND"
	CohortExplicit     CohortMode = "EXPLICIT"
	CohortIntersection CohortMode = "INTERSECTION"
)

// Cohort returns the cohort mode based on class_id + invited_gcids
// presence (open-link, class-bound, explicit, intersection).
func (a *Assessment) Cohort() CohortMode {
	hasClass := strings.TrimSpace(a.ClassID) != ""
	hasInvited := len(a.InvitedGCIDs) > 0
	switch {
	case !hasClass && !hasInvited:
		return CohortOpenLink
	case hasClass && !hasInvited:
		return CohortClassBound
	case !hasClass && hasInvited:
		return CohortExplicit
	default:
		return CohortIntersection
	}
}

// LearnerCohortFacts carries the roster facts an eligibility decision needs but
// that the domain cannot fetch for itself. The adapter resolves them (see the
// CohortRoster port) and hands them in, keeping the rule a pure function.
//
// Both fields are POSITIVE grants and both default to false, so a caller that
// forgets to resolve them — or a roster lookup that errors and is ignored —
// denies rather than admits. That is deliberate: this is an authz gate, and
// the defect it replaces (CHO-2153) was precisely a gate that failed OPEN.
type LearnerCohortFacts struct {
	// EnrolledInOffering — the learner has a live course_enrollments row for
	// at least one course of a.OfferingID.
	EnrolledInOffering bool
	// BookedOnClass — the learner has a live bookings row for a.ClassID.
	BookedOnClass bool
}

// RequiresOfferingEnrolment reports whether an eligibility decision for this
// assessment needs LearnerCohortFacts.EnrolledInOffering resolved. Lets the
// adapter skip a query it does not need.
func (a *Assessment) RequiresOfferingEnrolment() bool {
	return a.Cohort() == CohortOpenLink && strings.TrimSpace(a.OfferingID) != ""
}

// RequiresClassBooking reports whether an eligibility decision for this
// assessment needs LearnerCohortFacts.BookedOnClass resolved.
func (a *Assessment) RequiresClassBooking() bool {
	switch a.Cohort() {
	case CohortClassBound, CohortIntersection:
		return true
	}
	return false
}

// IsLearnerEligible reports whether the given learner may attempt this
// assessment, per the cohort model of ADR-155 §D9 **as amended by ADR-234**.
//
//   - OPEN_LINK, offering-attached → enrolled in ≥1 course of the offering.
//     "Open link" means open WITHIN the cohort, not open to the tenant.
//   - OPEN_LINK, freestanding      → any tenant member. With no offering, no
//     class and no invite list there is no cohort to scope to; the row is
//     tenant-open by construction. Discouraged (ADR-234); zero such rows live.
//   - CLASS_BOUND                  → on the class roster (bookings).
//   - EXPLICIT                     → on InvitedGCIDs. An explicit invite is a
//     deliberate grant and stands on its own — it does not additionally
//     require enrolment (this is the ADR-155 v1 demo path, 85 live rows).
//   - INTERSECTION                 → on InvitedGCIDs AND the class roster.
//
// The roster facts are supplied by the caller; this function performs no IO.
// Before ADR-234 this returned true unconditionally for OPEN_LINK and
// CLASS_BOUND, deferring the roster check to an "upstream" caller that never
// existed — so any tenant member could list and start any offering's
// assessment, burning an attempt (CHO-2153 F5).
func (a *Assessment) IsLearnerEligible(learnerGCID string, facts LearnerCohortFacts) bool {
	learnerGCID = strings.TrimSpace(learnerGCID)
	if learnerGCID == "" {
		return false
	}
	switch a.Cohort() {
	case CohortOpenLink:
		if strings.TrimSpace(a.OfferingID) == "" {
			return true // freestanding: no cohort to scope to
		}
		return facts.EnrolledInOffering
	case CohortClassBound:
		return facts.BookedOnClass
	case CohortExplicit:
		return a.isInvited(learnerGCID)
	case CohortIntersection:
		return a.isInvited(learnerGCID) && facts.BookedOnClass
	}
	return false
}

// isInvited reports membership of the explicit invite list.
func (a *Assessment) isInvited(learnerGCID string) bool {
	for _, g := range a.InvitedGCIDs {
		if g == learnerGCID {
			return true
		}
	}
	return false
}

// IsLearnerVisible reports whether the assessment is visible to a learner.
// Visible = SCHEDULED / OPEN / CLOSED / GRADING / GRADED / RELEASED states
// AND cohort eligibility. ARCHIVED + DRAFT excluded.
//
// Visibility and attemptability are the SAME cohort decision: a listed card the
// learner cannot start is a broken UI, and the card itself leaks the
// assessment's existence to someone outside the cohort.
func (a *Assessment) IsLearnerVisible(learnerGCID string, facts LearnerCohortFacts) bool {
	switch a.State {
	case AssessmentStateDraft, AssessmentStateArchived:
		return false
	}
	if a.DeletedAt != nil {
		return false
	}
	return a.IsLearnerEligible(learnerGCID, facts)
}

// Publish transitions DRAFT → SCHEDULED. Idempotent if already SCHEDULED.
//
// Returns:
//   - nil on success (and when re-publishing an already-SCHEDULED assessment).
//   - ErrAssessmentNotDraft when state is past DRAFT/SCHEDULED.
//   - ErrAssessmentInvalidWindow when scheduling fields invalid.
func (a *Assessment) Publish(now time.Time) error {
	if a.State == AssessmentStateScheduled {
		return nil // idempotent
	}
	if a.State != AssessmentStateDraft {
		return ErrAssessmentNotDraft
	}
	if a.ScheduledOpenAt.IsZero() || a.ScheduledCloseAt.IsZero() {
		return ErrAssessmentInvalidWindow
	}
	if !a.ScheduledCloseAt.After(a.ScheduledOpenAt) {
		return ErrAssessmentInvalidWindow
	}
	a.State = AssessmentStateScheduled
	pa := now.UTC()
	a.PublishedAt = &pa
	a.UpdatedAt = pa
	return nil
}

// ForceClose transitions OPEN → CLOSED. Returns ErrAssessmentNotOpen when
// state ≠ OPEN.
func (a *Assessment) ForceClose(now time.Time) error {
	if a.State != AssessmentStateOpen {
		return ErrAssessmentNotOpen
	}
	a.State = AssessmentStateClosed
	ca := now.UTC()
	a.ClosedAt = &ca
	a.UpdatedAt = ca
	return nil
}

// MarkGrading transitions CLOSED → GRADING (auto after force-close /
// scheduled-close fires). Internal lifecycle helper.
func (a *Assessment) MarkGrading(now time.Time) {
	if a.State == AssessmentStateClosed {
		a.State = AssessmentStateGrading
		a.UpdatedAt = now.UTC()
	}
}

// MarkGraded transitions GRADING → GRADED. Internal lifecycle helper.
func (a *Assessment) MarkGraded(now time.Time) {
	if a.State == AssessmentStateGrading {
		a.State = AssessmentStateGraded
		a.UpdatedAt = now.UTC()
	}
}

// ReleaseResults transitions GRADED → RELEASED and sets ResultsReleasedAt
// atomically.
//
// Idempotent: re-calling on RELEASED returns nil with no event re-emission.
// Returns ErrAssessmentNotGraded for any state ∉ {GRADED, RELEASED, OPEN, CLOSED, GRADING}.
//
// **Demo simplification (ADR-155 §D8)**: in the v1 Phyllis demo path with
// MCQ-only assessments, submission FSM short-circuits SUBMITTED →
// GRADED_PENDING_RELEASE without GRADING phase. Assessment may stay in OPEN
// after first submission is graded. We allow release from any state that
// has at least one GRADED submission — the upstream handler checks that.
func (a *Assessment) ReleaseResults(now time.Time, announcement string) error {
	if a.State == AssessmentStateReleased {
		return nil // idempotent
	}
	// We allow release from OPEN / CLOSED / GRADING / GRADED for the demo.
	switch a.State {
	case AssessmentStateOpen, AssessmentStateClosed,
		AssessmentStateGrading, AssessmentStateGraded:
		// allowed
	default:
		return ErrAssessmentNotGraded
	}
	a.State = AssessmentStateReleased
	t := now.UTC()
	a.ResultsReleasedAt = &t
	a.ReleaseAnnouncement = announcement
	a.UpdatedAt = t
	return nil
}

// Archive transitions to ARCHIVED (soft-delete) from any state.
// Idempotent.
func (a *Assessment) Archive(now time.Time) {
	if a.State == AssessmentStateArchived {
		return
	}
	t := now.UTC()
	a.State = AssessmentStateArchived
	a.ArchivedAt = &t
	a.DeletedAt = &t
	a.UpdatedAt = t
}

// AutoFlipToOpen transitions SCHEDULED → OPEN when now ≥ ScheduledOpenAt.
// Used by the read-time lazy-flip and the Cloud Scheduler cron path.
func (a *Assessment) AutoFlipToOpen(now time.Time) {
	if a.State == AssessmentStateScheduled && !now.Before(a.ScheduledOpenAt) {
		a.State = AssessmentStateOpen
		a.UpdatedAt = now.UTC()
	}
}

// AutoFlipToClosed transitions OPEN → CLOSED when now ≥ ScheduledCloseAt — the
// mirror of AutoFlipToOpen, and the transition that did not exist (CHO-2153
// F6). Until this, the ONLY OPEN → CLOSED path was an instructor's manual
// ForceClose, so an assessment whose window elapsed stayed OPEN — and startable
// — forever. Live: two SE-Test001 assessments sat OPEN with scheduled_close_at
// = 2026-07-08 and were still startable on 2026-07-14.
//
// A ZERO ScheduledCloseAt means "no window" and must never auto-close: now is
// always ≥ the zero time, so omitting this guard would slam every windowless
// assessment shut on first read.
func (a *Assessment) AutoFlipToClosed(now time.Time) {
	if a.State != AssessmentStateOpen {
		return
	}
	if a.ScheduledCloseAt.IsZero() {
		return
	}
	if now.Before(a.ScheduledCloseAt) {
		return
	}
	a.State = AssessmentStateClosed
	ca := now.UTC()
	a.ClosedAt = &ca
	a.UpdatedAt = ca
}

// StartWindowError reports why a new attempt may not begin at `now`, or nil if
// the window permits one. Enforced at BOTH ends: before this, START accepted
// state SCHEDULED as well as OPEN, so an assessment could be sat before its
// window opened as well as after it closed.
//
// A zero bound means "unbounded at that end" and is not enforced.
func (a *Assessment) StartWindowError(now time.Time) error {
	if !a.ScheduledOpenAt.IsZero() && now.Before(a.ScheduledOpenAt) {
		return ErrAssessmentNotYetOpen
	}
	if !a.ScheduledCloseAt.IsZero() && !now.Before(a.ScheduledCloseAt) {
		return ErrAssessmentWindowClosed
	}
	return nil
}
