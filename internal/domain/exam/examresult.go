// examresult.go — the ExamResult aggregate + the pure cut-score → PASS/FAIL
// computation, the #1 highest-risk path of the four-mode delivery refactor
// (docs/R-PLUS-FOUR-MODE-DELIVERY-REFACTOR-2026-06-24.md §8 #1).
//
// An ExamResult is the durable, source-of-truth record of a candidate's
// outcome against a specific (revision-pinned, exposure-locked) ExamForm.
// CandidateRef is an opaque UUID — the Candidate aggregate is owned elsewhere
// (ADR-190 D2 "Candidate = a real GCID"); this BC only references it by id, no
// FK, validated over events.
//
// ExamResultReleased is the domain EVENT VALUE the finalize path returns for
// the future outcome-spine / certificate seam (ADR-190 D1 "one event-fed
// seam out of Delivery"). NB: NO Pub/Sub topic is provisioned by this Brick —
// the durable ExamResult row is the source of truth; wiring a publisher +
// topic (chora.delivery.exam_result.released.v1) + schema + Terraform is a
// parent/owner follow-up.
//
// Pure domain — NO http, NO persistence imports.
package exam

import (
	"errors"
	"strings"
	"time"
)

// Outcome is the terminal pass/fail verdict of an ExamResult.
type Outcome string

const (
	// OutcomePass — the candidate met or exceeded the cut.
	OutcomePass Outcome = "PASS"
	// OutcomeFail — the candidate fell below the cut.
	OutcomeFail Outcome = "FAIL"
)

// IsValid reports whether o is one of the canonical outcomes.
func (o Outcome) IsValid() bool {
	switch o {
	case OutcomePass, OutcomeFail:
		return true
	}
	return false
}

var (
	// ErrExamResultTenantRequired — tenant_id must be non-empty.
	ErrExamResultTenantRequired = errors.New("exam_result: tenant_id required")
	// ErrExamResultExamRequired — exam_id must be non-empty.
	ErrExamResultExamRequired = errors.New("exam_result: exam_id required")
	// ErrExamResultFormRequired — exam_form_id must be non-empty.
	ErrExamResultFormRequired = errors.New("exam_result: exam_form_id required")
	// ErrExamResultCandidateRequired — candidate_ref must be non-empty.
	ErrExamResultCandidateRequired = errors.New("exam_result: candidate_ref required")
	// ErrExamResultRawInvalid — raw_score outside [0,max_score].
	ErrExamResultRawInvalid = errors.New("exam_result: raw_score out of range [0,max]")
)

// ExamResult is the durable per-candidate outcome record (source of truth).
//
// Cross-domain references (ExamID, ExamFormID, CandidateRef) travel as opaque
// UUIDs — no Go-level FK. Soft delete via DeletedAt (never hard-delete).
type ExamResult struct {
	ID           string
	TenantID     string
	ExamID       string
	ExamFormID   string
	CandidateRef string
	RawScore     int
	MaxScore     int
	Outcome      Outcome
	ScoredAt     time.Time
	CreatedAt    time.Time
	DeletedAt    *time.Time
}

// NewExamResultInput is the constructor input bag for NewExamResult.
type NewExamResultInput struct {
	TenantID     string
	ExamID       string
	ExamFormID   string
	CandidateRef string
	Cut          CutScore
	RawScore     int
}

// NewExamResult is the pure cut-score → PASS/FAIL computation.
//
// This is the compliance-critical scoring primitive: given a validated
// CutScore and a raw score, PASS iff raw >= cut, else FAIL. It guards the
// required references and the raw-score range (a raw > max or raw < 0 is a
// grading bug and is refused loudly, never scored). MaxScore is taken from the
// cut so the durable row records the exact scale the verdict was computed on.
func NewExamResult(in NewExamResultInput) (*ExamResult, error) {
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		return nil, ErrExamResultTenantRequired
	}
	examID := strings.TrimSpace(in.ExamID)
	if examID == "" {
		return nil, ErrExamResultExamRequired
	}
	formID := strings.TrimSpace(in.ExamFormID)
	if formID == "" {
		return nil, ErrExamResultFormRequired
	}
	candidate := strings.TrimSpace(in.CandidateRef)
	if candidate == "" {
		return nil, ErrExamResultCandidateRequired
	}
	if err := in.Cut.Validate(); err != nil {
		return nil, err
	}
	passed, err := in.Cut.Passes(in.RawScore)
	if err != nil {
		// Normalise the cut's raw-range sentinel to the ExamResult vocabulary so
		// callers map a single error to 400.
		if errors.Is(err, ErrCutScoreRawInvalid) {
			return nil, ErrExamResultRawInvalid
		}
		return nil, err
	}
	outcome := OutcomeFail
	if passed {
		outcome = OutcomePass
	}
	now := time.Now().UTC()
	return &ExamResult{
		ID:           newUUIDv7(),
		TenantID:     tenantID,
		ExamID:       examID,
		ExamFormID:   formID,
		CandidateRef: candidate,
		RawScore:     in.RawScore,
		MaxScore:     in.Cut.MaxScore,
		Outcome:      outcome,
		ScoredAt:     now,
		CreatedAt:    now,
	}, nil
}

// ExamResultReleased is the domain event VALUE emitted when a result is
// finalised — the outcome-spine / certificate seam (ADR-190 D1). It carries
// exactly the facts a downstream cert-issuer or per-learner transcript needs;
// the publishing adapter maps it onto the mandatory event envelope + provisions
// the topic (deferred — see the file header).
type ExamResultReleased struct {
	ResultID     string
	TenantID     string
	ExamID       string
	ExamFormID   string
	CandidateRef string
	RawScore     int
	MaxScore     int
	PassMark     int
	Outcome      Outcome
	OccurredAt   time.Time
}
