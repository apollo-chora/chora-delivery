// assessment_port.go — hexagonal ports for Assessment + Submission persistence.
//
// The HTTP layer depends on these interfaces (NOT on concrete pg / inmem
// implementations) per .claude/skills/hexagonal/SKILL.md. Production wires
// pg.AssessmentRepo + pg.SubmissionRepo; local dev / unit tests wire the
// InMem variants (this file).
//
// All methods are RLS-scoped via the caller's ctx (the production pg
// implementation calls rls.ApplySession before each query).
//
// Owned aggregates:
//   - Assessment   — instructor-facing (CRUD + lifecycle + monitor)
//   - Submission   — learner-facing (start / autosave / submit / result)
package delivery

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// AssessmentRepo is the persistence port for the Assessment aggregate.
type AssessmentRepo interface {
	// Save inserts or updates (UPSERT on id). Idempotent.
	Save(ctx context.Context, a *Assessment) error

	// Get returns the assessment by id; ok=false when not found / deleted.
	Get(ctx context.Context, tenantID, id string) (*Assessment, bool, error)

	// ListByInstructor returns the instructor's authored assessments.
	ListByInstructor(ctx context.Context, tenantID, instructorGCID string, pageSize int, pageToken string) ([]*Assessment, string, error)

	// ListByOffering returns the assessments scoped to a delivery Offering
	// (W3.A — making an Offering's assessments addressable). Tenant + RLS
	// scoped, soft-delete-filtered, created_at DESC. Mirrors ListByInstructor.
	ListByOffering(ctx context.Context, tenantID, offeringID string, pageSize int, pageToken string) ([]*Assessment, string, error)

	// ListVisibleToLearner returns assessments visible to a learner per
	// the cohort scoping rules (ADR-155 §D9). State filter: visible
	// states only (SCHEDULED..RELEASED; excludes DRAFT/ARCHIVED).
	ListVisibleToLearner(ctx context.Context, tenantID, learnerGCID string, pageSize int, pageToken string) ([]*Assessment, string, error)

	// MonitorCounts returns dashboard counts for an assessment.
	MonitorCounts(ctx context.Context, tenantID, assessmentID string) (AssessmentMonitorCounts, error)

	// ReleaseAllSubmissions atomically flips every GRADED_PENDING_RELEASE
	// submission for the assessment to RELEASED. Returns the released
	// submission ids for fan-out events.
	ReleaseAllSubmissions(ctx context.Context, tenantID, assessmentID string) ([]string, error)
}

// SubmissionRepo is the persistence port for the Submission aggregate.
type SubmissionRepo interface {
	// Save inserts or updates the submission + replaces its answers.
	Save(ctx context.Context, s *Submission) error

	// Get returns the submission by id; ok=false when not found.
	Get(ctx context.Context, tenantID, id string) (*Submission, bool, error)

	// GetByLearner returns the learner's existing IN_PROGRESS submission for
	// an assessment (or nil if none exists). Used for idempotent /start.
	GetInProgressByLearner(ctx context.Context, tenantID, assessmentID, learnerGCID string) (*Submission, bool, error)

	// ListByAssessment returns submissions for an assessment (instructor view).
	ListByAssessment(ctx context.Context, tenantID, assessmentID string, pageSize int, pageToken string) ([]*Submission, string, error)

	// ListByLearner returns the learner's own submissions across assessments.
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*Submission, error)

	// CountAttemptsByLearner returns how many submissions the learner has
	// for the given assessment (used for attempts-exhausted check).
	CountAttemptsByLearner(ctx context.Context, tenantID, assessmentID, learnerGCID string) (int, error)

	// FindLatestByLearner returns the learner's most-recent submission
	// (highest attempt_number) for the assessment. (nil, false, nil) when
	// the learner has no attempts. Used by the LearnerAssessmentSummary
	// projection to populate `learner_latest_submission_id` +
	// `learner_latest_submission_state` so the FE "View result" CTA can
	// deep-link straight to /a/me/assessments/{id}/result/{subId} without
	// a second round-trip (E2E-BE-RESULT-LINK-SUBMISSION-ID).
	FindLatestByLearner(ctx context.Context, tenantID, assessmentID, learnerGCID string) (*Submission, bool, error)

	// AppendGradeOverrides persists append-only HITL audit rows (ADR-172 §D7)
	// produced by a grade override. No-op for an empty slice.
	AppendGradeOverrides(ctx context.Context, tenantID, submissionID string, overrides []GradeOverride) error

	// ListReleasedByLearner returns the learner's RELEASED submissions,
	// newest grading-completion (GradedAt) first, windowed by (limit,
	// offset). Backs the ListLearnerGradedSubmissions gRPC read — the
	// CHO-2040 ceremony learning-edges seam consumed by chora-consumption's
	// Familiar edge-scout (cross-DB reads forbidden).
	//
	// VISIBILITY: state = RELEASED only — the exact gate the learner-facing
	// result endpoint (http myResultHandler) applies before revealing the
	// ADR-172 overall_comment; RELEASED is reachable only through the
	// ADR-172 §D6 HITL gate, so review_status is subsumed. Soft-delete
	// respected. Answers are NOT loaded (summary projection only).
	ListReleasedByLearner(ctx context.Context, tenantID, learnerGCID string, limit, offset int) ([]*Submission, error)
}

// AssessmentMonitorCounts is the projection returned by MonitorCounts.
type AssessmentMonitorCounts struct {
	TotalInvited     int
	TotalStarted     int
	TotalSubmitted   int
	TotalGraded      int
	TotalReleased    int
	InProgressCount  int
	AverageScorePct  float64
	HasGradedSamples bool
}

// -----------------------------------------------------------------------------
// InMemAssessmentRepo — local dev + unit test fallback (NOT production).
// -----------------------------------------------------------------------------

// InMemAssessmentRepo is an in-memory AssessmentRepo for tests + local dev.
type InMemAssessmentRepo struct {
	mu         sync.RWMutex
	data       map[string]*Assessment // key: id
	linkedSubs submissionLister       // optional link for MonitorCounts + ReleaseAll
	roster     CohortRoster           // cohort authz facts for ListVisibleToLearner
}

// NewInMemAssessmentRepo returns a fresh repo. Its roster starts EMPTY, not
// absent: ListVisibleToLearner therefore denies offering-attached and
// class-bound assessments until the test enrols/books the learner. An authz
// gate must default to closed.
func NewInMemAssessmentRepo() *InMemAssessmentRepo {
	return &InMemAssessmentRepo{
		data:   make(map[string]*Assessment),
		roster: NewInMemCohortRoster(),
	}
}

// Save stores the assessment (deep-copy elided — caller owns the lifecycle).
func (r *InMemAssessmentRepo) Save(_ context.Context, a *Assessment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a == nil || a.ID == "" {
		return ErrAssessmentTenantRequired
	}
	r.data[a.ID] = a
	return nil
}

// Get returns the assessment + ok flag.
func (r *InMemAssessmentRepo) Get(_ context.Context, tenantID, id string) (*Assessment, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.data[id]
	if !ok || a.TenantID != tenantID {
		return nil, false, nil
	}
	if a.DeletedAt != nil {
		return nil, false, nil
	}
	return a, true, nil
}

// ListByInstructor returns the instructor's assessments (descending by created_at).
func (r *InMemAssessmentRepo) ListByInstructor(_ context.Context, tenantID, instructorGCID string, pageSize int, _ string) ([]*Assessment, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if pageSize <= 0 {
		pageSize = 20
	}
	out := make([]*Assessment, 0)
	for _, a := range r.data {
		if a.TenantID != tenantID || a.InstructorGCID != instructorGCID {
			continue
		}
		if a.DeletedAt != nil {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > pageSize {
		out = out[:pageSize]
	}
	return out, "", nil
}

// ListByOffering returns the offering's assessments (descending by created_at).
func (r *InMemAssessmentRepo) ListByOffering(_ context.Context, tenantID, offeringID string, pageSize int, _ string) ([]*Assessment, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if pageSize <= 0 {
		pageSize = 20
	}
	out := make([]*Assessment, 0)
	for _, a := range r.data {
		if a.TenantID != tenantID || a.OfferingID != offeringID {
			continue
		}
		if a.DeletedAt != nil {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > pageSize {
		out = out[:pageSize]
	}
	return out, "", nil
}

// ListVisibleToLearner filters assessments where the learner is eligible.
//
// This MUST stay decision-identical to pg.SQLListAssessmentsVisibleToLearner —
// the two encode the same cohort rule in different languages, and a divergence
// between them is invisible to any test that exercises only one side.
// TestCohortRule_InMemAndSQL_Agree pins them together.
func (r *InMemAssessmentRepo) ListVisibleToLearner(ctx context.Context, tenantID, learnerGCID string, pageSize int, _ string) ([]*Assessment, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if pageSize <= 0 {
		pageSize = 20
	}
	out := make([]*Assessment, 0)
	for _, a := range r.data {
		if a.TenantID != tenantID {
			continue
		}
		facts, err := ResolveCohortFacts(ctx, r.roster, a, tenantID, learnerGCID)
		if err != nil {
			return nil, "", err // a roster outage refuses; it never lists
		}
		if !a.IsLearnerVisible(learnerGCID, facts) {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > pageSize {
		out = out[:pageSize]
	}
	return out, "", nil
}

// MonitorCounts walks the submissions repo via the linked InMem submissions
// repo (set via SetSubmissionLink).
func (r *InMemAssessmentRepo) MonitorCounts(ctx context.Context, tenantID, assessmentID string) (AssessmentMonitorCounts, error) {
	r.mu.RLock()
	a, ok := r.data[assessmentID]
	r.mu.RUnlock()
	if !ok || a == nil || a.TenantID != tenantID {
		return AssessmentMonitorCounts{}, nil
	}
	out := AssessmentMonitorCounts{TotalInvited: len(a.InvitedGCIDs)}
	if r.linkedSubs == nil {
		return out, nil
	}
	subs, _, _ := r.linkedSubs.ListByAssessment(ctx, tenantID, assessmentID, 1000, "")
	scoreSum := 0.0
	scoreCount := 0
	for _, s := range subs {
		switch s.State {
		case SubmissionStateStarted, SubmissionStateInProgress:
			out.InProgressCount++
			out.TotalStarted++
		case SubmissionStateSubmitted, SubmissionStatePendingOEGrading:
			out.TotalStarted++
			out.TotalSubmitted++
		case SubmissionStateGradedPendingRelease:
			out.TotalStarted++
			out.TotalSubmitted++
			out.TotalGraded++
			scoreSum += s.ScorePercent()
			scoreCount++
		case SubmissionStateReleased:
			out.TotalStarted++
			out.TotalSubmitted++
			out.TotalGraded++
			out.TotalReleased++
			scoreSum += s.ScorePercent()
			scoreCount++
		}
	}
	if scoreCount > 0 {
		out.AverageScorePct = scoreSum / float64(scoreCount)
		out.HasGradedSamples = true
	}
	return out, nil
}

// linkedSubs lets MonitorCounts walk submissions without a tight import cycle.
type submissionLister interface {
	ListByAssessment(ctx context.Context, tenantID, assessmentID string, pageSize int, pageToken string) ([]*Submission, string, error)
}

// linkedSubs is a (possibly-nil) reference to the in-mem submissions repo.
//
//nolint:unused
var _ submissionLister = (*InMemSubmissionRepo)(nil)

// SetSubmissionLink attaches an in-mem submissions repo so MonitorCounts can
// aggregate counts. Production uses real SQL JOIN-based aggregation in the
// pg adapter.
func (r *InMemAssessmentRepo) SetSubmissionLink(s submissionLister) {
	r.linkedSubs = s
}

// SetRosterLink swaps in the cohort roster used by ListVisibleToLearner.
func (r *InMemAssessmentRepo) SetRosterLink(roster CohortRoster) {
	r.roster = roster
}

// -----------------------------------------------------------------------------
// InMemSubmissionRepo
// -----------------------------------------------------------------------------

// InMemSubmissionRepo is an in-memory SubmissionRepo for tests + local dev.
type InMemSubmissionRepo struct {
	mu        sync.RWMutex
	data      map[string]*Submission     // key: id
	overrides map[string][]GradeOverride // key: submission_id (HITL audit, ADR-172)
}

// NewInMemSubmissionRepo returns a fresh repo.
func NewInMemSubmissionRepo() *InMemSubmissionRepo {
	return &InMemSubmissionRepo{data: make(map[string]*Submission)}
}

// Save stores the submission.
func (r *InMemSubmissionRepo) Save(_ context.Context, s *Submission) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s == nil || s.ID == "" {
		return ErrSubmissionAssessmentRequired
	}
	r.data[s.ID] = s
	return nil
}

// Get returns the submission.
func (r *InMemSubmissionRepo) Get(_ context.Context, tenantID, id string) (*Submission, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.data[id]
	if !ok || s.TenantID != tenantID || s.DeletedAt != nil {
		return nil, false, nil
	}
	return s, true, nil
}

// GetInProgressByLearner returns the learner's existing IN_PROGRESS sub.
func (r *InMemSubmissionRepo) GetInProgressByLearner(_ context.Context, tenantID, assessmentID, learnerGCID string) (*Submission, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.data {
		if s.TenantID != tenantID || s.AssessmentID != assessmentID || s.LearnerGCID != learnerGCID {
			continue
		}
		if s.State == SubmissionStateStarted || s.State == SubmissionStateInProgress {
			return s, true, nil
		}
	}
	return nil, false, nil
}

// ListByAssessment returns submissions for an assessment.
func (r *InMemSubmissionRepo) ListByAssessment(_ context.Context, tenantID, assessmentID string, pageSize int, _ string) ([]*Submission, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if pageSize <= 0 {
		pageSize = 20
	}
	out := make([]*Submission, 0)
	for _, s := range r.data {
		if s.TenantID != tenantID || s.AssessmentID != assessmentID {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > pageSize {
		out = out[:pageSize]
	}
	return out, "", nil
}

// ListByLearner returns the learner's submissions.
func (r *InMemSubmissionRepo) ListByLearner(_ context.Context, tenantID, learnerGCID string) ([]*Submission, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Submission, 0)
	for _, s := range r.data {
		if s.TenantID != tenantID || s.LearnerGCID != learnerGCID {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// CountAttemptsByLearner returns the count.
func (r *InMemSubmissionRepo) CountAttemptsByLearner(_ context.Context, tenantID, assessmentID, learnerGCID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, s := range r.data {
		if s.TenantID == tenantID && s.AssessmentID == assessmentID && s.LearnerGCID == learnerGCID {
			n++
		}
	}
	return n, nil
}

// FindLatestByLearner returns the learner's most-recent submission by
// AttemptNumber. (nil, false, nil) when no attempts exist.
func (r *InMemSubmissionRepo) FindLatestByLearner(_ context.Context, tenantID, assessmentID, learnerGCID string) (*Submission, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var latest *Submission
	for _, s := range r.data {
		if s.TenantID != tenantID || s.AssessmentID != assessmentID || s.LearnerGCID != learnerGCID {
			continue
		}
		if s.DeletedAt != nil {
			continue
		}
		if latest == nil || s.AttemptNumber > latest.AttemptNumber {
			latest = s
		}
	}
	if latest == nil {
		return nil, false, nil
	}
	return latest, true, nil
}

// ListReleasedByLearner returns the learner's RELEASED submissions, newest
// GradedAt first (nil GradedAt last, ID desc tiebreak), windowed by (limit,
// offset). Mirrors pg.SubmissionRepo.ListReleasedByLearner — see the port
// doc for the CHO-2040 visibility rule.
func (r *InMemSubmissionRepo) ListReleasedByLearner(_ context.Context, tenantID, learnerGCID string, limit, offset int) ([]*Submission, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	out := make([]*Submission, 0)
	for _, s := range r.data {
		if s.TenantID != tenantID || s.LearnerGCID != learnerGCID {
			continue
		}
		if s.State != SubmissionStateReleased {
			continue
		}
		if s.DeletedAt != nil {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		gi, gj := out[i].GradedAt, out[j].GradedAt
		switch {
		case gi == nil && gj == nil:
			return out[i].ID > out[j].ID
		case gi == nil:
			return false // nil GradedAt sorts last (NULLS LAST)
		case gj == nil:
			return true
		case gi.Equal(*gj):
			return out[i].ID > out[j].ID
		default:
			return gi.After(*gj)
		}
	})
	if offset >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// gradeOverrideAudit is the in-mem audit sink (tests may inspect r.overrides).
func (r *InMemSubmissionRepo) AppendGradeOverrides(_ context.Context, _, submissionID string, overrides []GradeOverride) error {
	if len(overrides) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overrides == nil {
		r.overrides = map[string][]GradeOverride{}
	}
	r.overrides[submissionID] = append(r.overrides[submissionID], overrides...)
	return nil
}

// -----------------------------------------------------------------------------
// ReleaseAllSubmissions (in-mem variant) — delegates via linked submission
// repo set on the assessment repo.
// -----------------------------------------------------------------------------

// ReleaseAllSubmissions iterates the linked submissions repo + flips every
// GRADED_PENDING_RELEASE for the assessment to RELEASED. Returns ids.
func (r *InMemAssessmentRepo) ReleaseAllSubmissions(ctx context.Context, tenantID, assessmentID string) ([]string, error) {
	if r.linkedSubs == nil {
		return nil, nil
	}
	subs, _, err := r.linkedSubs.ListByAssessment(ctx, tenantID, assessmentID, 10000, "")
	if err != nil {
		return nil, err
	}
	released := []string{}
	for _, s := range subs {
		// ADR-172 §D6 — MarkReleased is gated on review_status; only count
		// submissions that actually transitioned to RELEASED.
		if s.State == SubmissionStateGradedPendingRelease {
			s.MarkReleased(s.UpdatedAt)
			if s.State == SubmissionStateReleased {
				released = append(released, s.ID)
			}
		}
	}
	return released, nil
}

// Compile-time port conformance.
var (
	_ AssessmentRepo = (*InMemAssessmentRepo)(nil)
	_ SubmissionRepo = (*InMemSubmissionRepo)(nil)
)

// TrimGCID is a tiny convenience for case-insensitive GCID equality used by
// the cohort eligibility upstream check.
//
//nolint:unused
func TrimGCID(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
