// invigilator.go owns the ExamInvigilator aggregate — a per-sitting proctor
// assignment carrying a RANK (ADR-191 D1 + design reference
// docs/design/exam-administration-domain.md §4.6).
//
// ADR-191 splits the vocabulary: the platform ROLE is `PROCTOR` (a canonical
// tenant-scoped, add-on-gated role asserted on the JWT / x-mesh-user-roles);
// the per-sitting RANK enum here — {chief_invigilator, invigilator,
// technical_support, observer} — is the in-context operational authority level.
// The binding row is ExamInvigilator(tenant_id, sitting_id, invigilator_gcid).
//
// COMPLIANCE INVARIANT (ADR-191 O1): at most ONE chief_invigilator per sitting
// (the chief may pause/void a sitting + view detailed scores). Enforced at TWO
// layers: (1) the pure-domain guard EnsureSingleChief below, called by the
// use-case after listing the sitting's active invigilators; (2) a partial-
// unique index in migration 0048 (the concurrency backstop — a racing double-
// assign fails loud instead of forking a second chief).
//
// invigilator_gcid is a REAL, opaque learner/staff GCID (UUIDv7); sitting_id is
// an opaque cross-aggregate UUID (no FK). Soft delete (unassign) via DeletedAt.
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
// Rank enum (ADR-191 D1 — lowercase snake tokens)
// -----------------------------------------------------------------------------

// InvigilatorRank is the per-sitting operational authority level.
type InvigilatorRank string

const (
	// InvigilatorRankChief — full sitting control: pause/void, resolve incidents,
	// manage other invigilators. At most ONE per sitting (ADR-191 O1).
	InvigilatorRankChief InvigilatorRank = "chief_invigilator"
	// InvigilatorRankInvigilator — check in candidates, verify identity, monitor
	// the room, log incidents. The least-privilege default.
	InvigilatorRankInvigilator InvigilatorRank = "invigilator"
	// InvigilatorRankTechnicalSupport — IT issues only; no exam-control authority.
	InvigilatorRankTechnicalSupport InvigilatorRank = "technical_support"
	// InvigilatorRankObserver — view-only; cannot check in or log incidents.
	InvigilatorRankObserver InvigilatorRank = "observer"
)

// IsValid reports whether r is one of the canonical ranks.
func (r InvigilatorRank) IsValid() bool {
	switch r {
	case InvigilatorRankChief, InvigilatorRankInvigilator,
		InvigilatorRankTechnicalSupport, InvigilatorRankObserver:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Errors (sentinels)
// -----------------------------------------------------------------------------

var (
	// ErrInvigilatorTenantRequired — tenant_id must be non-empty.
	ErrInvigilatorTenantRequired = errors.New("invigilator: tenant_id required")
	// ErrInvigilatorSittingRequired — sitting_id must be non-empty.
	ErrInvigilatorSittingRequired = errors.New("invigilator: sitting_id required")
	// ErrInvigilatorGCIDRequired — invigilator_gcid must be non-empty.
	ErrInvigilatorGCIDRequired = errors.New("invigilator: invigilator_gcid required")
	// ErrInvigilatorRankInvalid — a non-empty rank must be one of the canonical
	// tokens.
	ErrInvigilatorRankInvalid = errors.New("invigilator: rank invalid")
	// ErrInvigilatorChiefExists — THE single-chief invariant: the sitting already
	// has an active chief_invigilator (ADR-191 O1).
	ErrInvigilatorChiefExists = errors.New("invigilator: sitting already has a chief_invigilator")
)

// -----------------------------------------------------------------------------
// Aggregate
// -----------------------------------------------------------------------------

// ExamInvigilator is a per-sitting proctor assignment.
//
// SittingID + InvigilatorGCID are opaque cross-aggregate/cross-domain UUID
// references (no FK). Soft delete (unassign) via DeletedAt.
type ExamInvigilator struct {
	ID              string
	TenantID        string
	SittingID       string
	InvigilatorGCID string
	Rank            InvigilatorRank
	AssignedAt      time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// NewExamInvigilatorInput is the constructor input bag.
type NewExamInvigilatorInput struct {
	TenantID        string
	SittingID       string
	InvigilatorGCID string
	Rank            InvigilatorRank
}

// NewExamInvigilator constructs an active ExamInvigilator assignment.
//
// Validation guards (rejected with a specific sentinel):
//   - tenant_id        trim-non-empty
//   - sitting_id       trim-non-empty
//   - invigilator_gcid trim-non-empty
//   - rank either empty (defaults to the least-privilege `invigilator`) or one
//     of the canonical ranks
//
// The single-chief invariant is enforced by the caller via EnsureSingleChief
// (this constructor validates one assignment in isolation). The aggregate ID is
// a freshly generated UUIDv7; timestamps are current UTC.
func NewExamInvigilator(in NewExamInvigilatorInput) (*ExamInvigilator, error) {
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		return nil, ErrInvigilatorTenantRequired
	}
	sittingID := strings.TrimSpace(in.SittingID)
	if sittingID == "" {
		return nil, ErrInvigilatorSittingRequired
	}
	gcid := strings.TrimSpace(in.InvigilatorGCID)
	if gcid == "" {
		return nil, ErrInvigilatorGCIDRequired
	}
	rank := in.Rank
	if rank == "" {
		rank = InvigilatorRankInvigilator
	}
	if !rank.IsValid() {
		return nil, ErrInvigilatorRankInvalid
	}
	now := time.Now().UTC()
	return &ExamInvigilator{
		ID:              newUUIDv7(),
		TenantID:        tenantID,
		SittingID:       sittingID,
		InvigilatorGCID: gcid,
		Rank:            rank,
		AssignedAt:      now,
		UpdatedAt:       now,
	}, nil
}

// IsChief reports whether this assignment carries the chief_invigilator rank.
func (iv *ExamInvigilator) IsChief() bool { return iv.Rank == InvigilatorRankChief }

// isActive reports whether the assignment is not soft-deleted.
func (iv *ExamInvigilator) isActive() bool { return iv.DeletedAt == nil }

// Unassign soft-deletes the assignment (idempotent — keeps the original stamp).
// NEVER a hard delete, per .claude/rules/ddd-enforcement.md Invariant #4.
func (iv *ExamInvigilator) Unassign() error {
	if iv.DeletedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	iv.DeletedAt = &now
	iv.UpdatedAt = now
	return nil
}

// EnsureSingleChief enforces the ADR-191 O1 invariant: at most one active
// chief_invigilator per sitting. If `incoming` carries the chief rank and any
// ACTIVE (non-soft-deleted) invigilator in `existing` — other than `incoming`
// itself — is already a chief, it returns ErrInvigilatorChiefExists. A non-chief
// `incoming`, or a first chief, is always allowed.
//
// The caller (assign use-case) passes the sitting's current active invigilators
// (already tenant+sitting-scoped by the store). This is pure domain logic — no
// I/O — so it is unit-testable; the DB partial-unique index is the concurrency
// backstop.
func EnsureSingleChief(existing []*ExamInvigilator, incoming *ExamInvigilator) error {
	if incoming == nil || !incoming.IsChief() {
		return nil
	}
	for _, iv := range existing {
		if iv == nil || iv.ID == incoming.ID {
			continue
		}
		if iv.isActive() && iv.IsChief() {
			return ErrInvigilatorChiefExists
		}
	}
	return nil
}
