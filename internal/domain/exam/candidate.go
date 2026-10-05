// candidate.go owns the Candidate aggregate — an identity-verified allocation
// of a real learner GCID to a proctored Exam sitting (ADR-190 D2).
//
// Per ADR-190 D2: "Candidate = a real GCID, never fire-and-forget." An existing
// chora-main user is reused (resolve) else a full user is registered (register)
// — "resolve-or-register". "Candidate" is a STATE of an exam allocation, NOT a
// new identity type. Admission is gated by an Identity-owned verification claim
// (reuses Singpass/KYC). This OVERRIDES the retired throwaway exam-only-GCID +
// ~90-day-cleanup design.
//
// The learner GCID that arrives here is treated as an already-resolved, real,
// opaque learner GCID (UUIDv7 — no tenant context embedded). The upstream
// resolve-or-register of a NEW chora-main user is an Identity-side concern
// (ADR-193 federated identity), out of scope for this aggregate.
//
// State machine (FSM):
//
//	ALLOCATED   → ID_VERIFIED via MarkVerified (Identity claim resolved VERIFIED)
//	ID_VERIFIED → ADMITTED    via Admit        (GATED: VerificationStatus==VERIFIED)
//	ALLOCATED   → REJECTED     via Reject       (admission denied)
//	ID_VERIFIED → REJECTED     via Reject
//	ALLOCATED   → WITHDRAWN     via Withdraw     (candidate/admin pulls the allocation)
//	ID_VERIFIED → WITHDRAWN     via Withdraw
//
// THE ADMISSION GATE (compliance invariant): Admit() MUST fail with the
// sentinel ErrCandidateNotVerified unless VerificationStatus == VERIFIED. A
// candidate is never admitted to a high-stakes sitting without a resolved
// identity claim.
//
// Cross-aggregate references (exam_id) + cross-domain references (gcid) travel
// as opaque UUIDs — no Go-level FK, per .claude/rules/ddd-enforcement.md.
//
// Candidate is a SEPARATE aggregate keyed by (tenant_id, exam_id, gcid); it
// does NOT touch Exam.EnrolledCount (Brick 1 owns that). Migrating
// EnrolledCount → a candidate-count projection is a follow-up.
//
// This file contains the domain only — NO HTTP, NO persistence imports. The
// UUIDv7 generator (newUUIDv7) is shared with exam.go in this package.
package exam

import (
	"errors"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// State + VerificationStatus enums
// -----------------------------------------------------------------------------

// CandidateState models the exam-allocation lifecycle FSM.
type CandidateState string

const (
	// CandidateStateAllocated — a real learner GCID is allocated to the sitting;
	// identity not yet verified.
	CandidateStateAllocated CandidateState = "ALLOCATED"
	// CandidateStateIDVerified — the Identity verification claim resolved
	// VERIFIED; the candidate is admissible.
	CandidateStateIDVerified CandidateState = "ID_VERIFIED"
	// CandidateStateAdmitted — admitted into the sitting (terminal).
	CandidateStateAdmitted CandidateState = "ADMITTED"
	// CandidateStateRejected — admission denied, e.g. verification failed
	// (terminal).
	CandidateStateRejected CandidateState = "REJECTED"
	// CandidateStateWithdrawn — allocation pulled by candidate/admin (terminal).
	CandidateStateWithdrawn CandidateState = "WITHDRAWN"
)

// IsValid reports whether s is one of the canonical Candidate states.
func (s CandidateState) IsValid() bool {
	switch s {
	case CandidateStateAllocated, CandidateStateIDVerified, CandidateStateAdmitted,
		CandidateStateRejected, CandidateStateWithdrawn:
		return true
	}
	return false
}

// VerificationStatus records whether the Identity-owned verification claim has
// resolved for this candidate.
type VerificationStatus string

const (
	// VerificationStatusUnverified — no resolved VERIFIED claim yet.
	VerificationStatusUnverified VerificationStatus = "UNVERIFIED"
	// VerificationStatusVerified — the Identity claim resolved VERIFIED.
	VerificationStatusVerified VerificationStatus = "VERIFIED"
)

// IsValid reports whether vs is one of the canonical verification statuses.
func (vs VerificationStatus) IsValid() bool {
	switch vs {
	case VerificationStatusUnverified, VerificationStatusVerified:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Errors (sentinels)
// -----------------------------------------------------------------------------

var (
	// ErrCandidateTenantRequired — tenant_id must be non-empty.
	ErrCandidateTenantRequired = errors.New("candidate: tenant_id required")
	// ErrCandidateExamRequired — exam_id must be non-empty.
	ErrCandidateExamRequired = errors.New("candidate: exam_id required")
	// ErrCandidateGCIDRequired — gcid (real learner GCID) must be non-empty.
	ErrCandidateGCIDRequired = errors.New("candidate: gcid required")

	// ErrCandidateNotVerified — THE ADMISSION GATE. Admit refuses unless
	// VerificationStatus == VERIFIED.
	ErrCandidateNotVerified = errors.New("candidate: admission refused — identity verification claim not VERIFIED")
	// ErrCandidateNotAllocated — MarkVerified requires ALLOCATED state.
	ErrCandidateNotAllocated = errors.New("candidate: not in ALLOCATED state")
	// ErrCandidateNotAdmissible — Admit requires ID_VERIFIED state (e.g. already
	// ADMITTED, or verified-then-withdrawn).
	ErrCandidateNotAdmissible = errors.New("candidate: not in ID_VERIFIED state (not admissible)")
	// ErrCandidateTerminal — transition attempted from a terminal state
	// (ADMITTED / REJECTED / WITHDRAWN).
	ErrCandidateTerminal = errors.New("candidate: state is terminal")
)

// -----------------------------------------------------------------------------
// Aggregate
// -----------------------------------------------------------------------------

// Candidate is the identity-verified exam-allocation aggregate root.
//
// GCID is a real, opaque learner GCID (UUIDv7, no tenant embedded) — NEVER a
// throwaway exam-only id. ExamID is an opaque cross-aggregate UUID (no FK).
// Soft delete via DeletedAt; transitions advance UpdatedAt.
type Candidate struct {
	ID                 string
	TenantID           string
	ExamID             string
	GCID               string
	State              CandidateState
	VerificationStatus VerificationStatus
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

// NewCandidateInput is the constructor input bag.
type NewCandidateInput struct {
	TenantID string
	ExamID   string
	GCID     string
}

// NewCandidate constructs an ALLOCATED / UNVERIFIED Candidate shell.
//
// Validation guards (rejected with a specific sentinel):
//   - tenant_id trim-non-empty
//   - exam_id   trim-non-empty
//   - gcid      trim-non-empty (a real learner GCID)
//
// The aggregate ID is a freshly generated UUIDv7; timestamps are current UTC.
func NewCandidate(in NewCandidateInput) (*Candidate, error) {
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		return nil, ErrCandidateTenantRequired
	}
	examID := strings.TrimSpace(in.ExamID)
	if examID == "" {
		return nil, ErrCandidateExamRequired
	}
	gcid := strings.TrimSpace(in.GCID)
	if gcid == "" {
		return nil, ErrCandidateGCIDRequired
	}
	now := time.Now().UTC()
	return &Candidate{
		ID:                 newUUIDv7(),
		TenantID:           tenantID,
		ExamID:             examID,
		GCID:               gcid,
		State:              CandidateStateAllocated,
		VerificationStatus: VerificationStatusUnverified,
		CreatedAt:          now,
		UpdatedAt:          now,
	}, nil
}

// -----------------------------------------------------------------------------
// State transitions
// -----------------------------------------------------------------------------

// isTerminal reports whether the candidate is in a terminal state.
func (c *Candidate) isTerminal() bool {
	switch c.State {
	case CandidateStateAdmitted, CandidateStateRejected, CandidateStateWithdrawn:
		return true
	}
	return false
}

// touch advances UpdatedAt to now (UTC).
func (c *Candidate) touch() { c.UpdatedAt = time.Now().UTC() }

// OccupiesSeat reports whether this candidate currently occupies an exam seat —
// an ACTIVE allocation (ALLOCATED / ID_VERIFIED / ADMITTED). REJECTED and
// WITHDRAWN release the seat and do NOT count toward capacity usage.
//
// The Exam candidate-count projection (CHO-2105) counts only seat-occupying
// candidates so the R+ Overview capacity figure reflects real allocation —
// distinct from the self-enrolment Exam.EnrolledCount.
func (c *Candidate) OccupiesSeat() bool {
	switch c.State {
	case CandidateStateAllocated, CandidateStateIDVerified, CandidateStateAdmitted:
		return true
	}
	return false
}

// MarkVerified transitions ALLOCATED → ID_VERIFIED and records the resolved
// Identity claim (VerificationStatus = VERIFIED). Requires ALLOCATED state.
//
// The caller (admission use-case) is responsible for resolving the claim via
// the VerificationClaimReader port BEFORE calling MarkVerified — the aggregate
// never performs I/O.
func (c *Candidate) MarkVerified() error {
	if c.isTerminal() {
		return ErrCandidateTerminal
	}
	if c.State != CandidateStateAllocated {
		return ErrCandidateNotAllocated
	}
	c.State = CandidateStateIDVerified
	c.VerificationStatus = VerificationStatusVerified
	c.touch()
	return nil
}

// Admit transitions ID_VERIFIED → ADMITTED. THE ADMISSION GATE: it refuses
// with ErrCandidateNotVerified unless VerificationStatus == VERIFIED, then
// with ErrCandidateNotAdmissible unless the candidate is in ID_VERIFIED state.
func (c *Candidate) Admit() error {
	if c.VerificationStatus != VerificationStatusVerified {
		return ErrCandidateNotVerified
	}
	if c.State != CandidateStateIDVerified {
		return ErrCandidateNotAdmissible
	}
	c.State = CandidateStateAdmitted
	c.touch()
	return nil
}

// Reject transitions ALLOCATED | ID_VERIFIED → REJECTED (admission denied).
func (c *Candidate) Reject() error {
	if c.isTerminal() {
		return ErrCandidateTerminal
	}
	c.State = CandidateStateRejected
	c.touch()
	return nil
}

// Withdraw transitions ALLOCATED | ID_VERIFIED → WITHDRAWN (allocation pulled).
func (c *Candidate) Withdraw() error {
	if c.isTerminal() {
		return ErrCandidateTerminal
	}
	c.State = CandidateStateWithdrawn
	c.touch()
	return nil
}
