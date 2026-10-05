// Package skillsfutures models the SkillsFuturesClaim aggregate for the
// chora-delivery service (R+ Rhythm+ surface).
//
// Domain context: SkillsFutures Singapore (SSG) is the Singapore-government
// training-funding scheme. A SkillsFuturesClaim represents a single
// SG-citizen learner's request to draw from their SSG credit allowance to
// pay for a course enrolment. The training-admin (Rhythm+ console) reviews
// each claim and either approves it (with an approved amount ≤ the
// requested amount) or rejects it (with an actionable rejection_reason).
// Approved claims later flip to DISBURSED once SSG settles the payout.
//
// FSM:
//
//	PENDING → APPROVED → DISBURSED          (happy path)
//	PENDING → REJECTED                       (admin rejects)
//
// Per ddd-enforcement.md aggregate invariants:
//   - Append-only audit (rejection_reason / decided_by_gcid / decided_at
//     set once at decision time, never cleared).
//   - Soft delete left as forward work — claims live on for SSG audit.
//   - Cross-domain references (course_id, gcid) are bare UUIDs — validated
//     out-of-band via the chora_delivery DB / chora_identity Pub/Sub events.
//
// Per multi-tenant-rls.skill: tenant_id is REQUIRED at construction. RLS on
// the eventual chora_delivery.skillsfutures_claims table will scope reads
// to the caller's tenant; the in-memory adapter filters in-process.
//
// Privacy: NRIC (Singapore National Registration ID, S-prefix) is NEVER
// stored raw. The FE submits sha256(NRIC) only; the raw value travels at
// most through Cloud Armor → gateway hashing middleware and is dropped.
package skillsfutures

import (
	"crypto/rand"
	"errors"
	"strings"
	"time"
)

// ClaimState models the SkillsFuturesClaim lifecycle FSM.
type ClaimState string

const (
	// ClaimStatePending — learner submitted, training-admin has not decided.
	ClaimStatePending ClaimState = "PENDING"
	// ClaimStateApproved — training-admin approved (with approved amount).
	ClaimStateApproved ClaimState = "APPROVED"
	// ClaimStateRejected — training-admin rejected (with rejection reason).
	ClaimStateRejected ClaimState = "REJECTED"
	// ClaimStateDisbursed — SSG settled the payout (terminal happy state).
	ClaimStateDisbursed ClaimState = "DISBURSED"
)

// IsValid reports whether s is one of the canonical claim states.
func (s ClaimState) IsValid() bool {
	switch s {
	case ClaimStatePending, ClaimStateApproved,
		ClaimStateRejected, ClaimStateDisbursed:
		return true
	}
	return false
}

// SkillsFuturesClaim domain errors.
var (
	// ErrTenantRequired — construction without tenant_id.
	ErrTenantRequired = errors.New("skillsfutures: tenant_id required")
	// ErrGCIDRequired — construction without learner gcid.
	ErrGCIDRequired = errors.New("skillsfutures: gcid required")
	// ErrCourseIDRequired — construction without course_id.
	ErrCourseIDRequired = errors.New("skillsfutures: course_id required")
	// ErrNRICHashRequired — construction without nric_hash.
	ErrNRICHashRequired = errors.New("skillsfutures: nric_hash required")
	// ErrAmountPositive — requested_amount_sgd_cents must be > 0.
	ErrAmountPositive = errors.New("skillsfutures: requested_amount_sgd_cents must be > 0")
	// ErrAmountNonNegative — approved_amount_sgd_cents must be ≥ 0.
	ErrAmountNonNegative = errors.New("skillsfutures: approved_amount_sgd_cents must be ≥ 0")
	// ErrApprovedExceedsRequested — approved > requested.
	ErrApprovedExceedsRequested = errors.New("skillsfutures: approved_amount_sgd_cents must not exceed requested_amount_sgd_cents")
	// ErrNotPending — Approve/Reject called when state != PENDING.
	ErrNotPending = errors.New("skillsfutures: claim not in PENDING state")
	// ErrNotApproved — Disburse called when state != APPROVED.
	ErrNotApproved = errors.New("skillsfutures: claim not in APPROVED state")
	// ErrRejectionReasonRequired — Reject called with empty reason.
	ErrRejectionReasonRequired = errors.New("skillsfutures: rejection_reason required")
	// ErrDeciderRequired — Approve/Reject called without decided_by_gcid.
	ErrDeciderRequired = errors.New("skillsfutures: decided_by_gcid required")
)

// SkillsFuturesClaim is the aggregate root for a SSG funding request.
type SkillsFuturesClaim struct {
	// ID — UUIDv7 (carries unix_ts_ms so monotonic-spread).
	ID string
	// TenantID — owning tenant; RLS-scope key.
	TenantID string
	// GCID — learner's global Chora ID.
	GCID string
	// CourseID — course the funding applies to (cross-domain, no FK).
	CourseID string
	// NRICHash — sha256 of the Singapore NRIC (S-prefix); never raw.
	NRICHash string
	// RequestedAmountCents — what the learner asked SSG to pay (positive cents).
	RequestedAmountCents int64
	// ApprovedAmountCents — what training-admin authorised; 0 until approved.
	ApprovedAmountCents int64
	// State — lifecycle FSM position.
	State ClaimState
	// SubmittedAt — when the learner submitted the claim.
	SubmittedAt time.Time
	// DecidedAt — when the training-admin decided; nil while PENDING.
	DecidedAt *time.Time
	// DecidedByGCID — training-admin's GCID; empty while PENDING.
	DecidedByGCID string
	// RejectionReason — required when REJECTED; empty otherwise.
	RejectionReason string
}

// NewClaimInput captures the fields required to construct a fresh PENDING
// SkillsFuturesClaim. Strings are trimmed before validation.
type NewClaimInput struct {
	TenantID             string
	GCID                 string
	CourseID             string
	NRICHash             string
	RequestedAmountCents int64
}

// NewClaim constructs a PENDING-state SkillsFuturesClaim with a freshly
// generated UUIDv7 ID. All string fields are TrimSpace'd before validation;
// every validation failure returns one of the sentinel ErrXxx errors so
// the HTTP layer can map the result to a 400 with a stable code.
func NewClaim(in NewClaimInput) (*SkillsFuturesClaim, error) {
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	gcid := strings.TrimSpace(in.GCID)
	if gcid == "" {
		return nil, ErrGCIDRequired
	}
	courseID := strings.TrimSpace(in.CourseID)
	if courseID == "" {
		return nil, ErrCourseIDRequired
	}
	nricHash := strings.TrimSpace(in.NRICHash)
	if nricHash == "" {
		return nil, ErrNRICHashRequired
	}
	if in.RequestedAmountCents <= 0 {
		return nil, ErrAmountPositive
	}
	now := time.Now().UTC()
	return &SkillsFuturesClaim{
		ID:                   newUUIDv7(),
		TenantID:             tenantID,
		GCID:                 gcid,
		CourseID:             courseID,
		NRICHash:             nricHash,
		RequestedAmountCents: in.RequestedAmountCents,
		State:                ClaimStatePending,
		SubmittedAt:          now,
	}, nil
}

// Approve transitions PENDING → APPROVED. The approvedAmountCents must be in
// [0, RequestedAmountCents]; the deciderGCID identifies the training-admin
// who approved the claim. RBAC enforcement lives at the HTTP layer.
func (c *SkillsFuturesClaim) Approve(deciderGCID string, approvedAmountCents int64) error {
	if c.State != ClaimStatePending {
		return ErrNotPending
	}
	decider := strings.TrimSpace(deciderGCID)
	if decider == "" {
		return ErrDeciderRequired
	}
	if approvedAmountCents < 0 {
		return ErrAmountNonNegative
	}
	if approvedAmountCents > c.RequestedAmountCents {
		return ErrApprovedExceedsRequested
	}
	now := time.Now().UTC()
	c.State = ClaimStateApproved
	c.ApprovedAmountCents = approvedAmountCents
	c.DecidedByGCID = decider
	c.DecidedAt = &now
	return nil
}

// Reject transitions PENDING → REJECTED. The reason is trimmed; an empty
// (or whitespace-only) reason returns ErrRejectionReasonRequired.
func (c *SkillsFuturesClaim) Reject(deciderGCID, reason string) error {
	if c.State != ClaimStatePending {
		return ErrNotPending
	}
	decider := strings.TrimSpace(deciderGCID)
	if decider == "" {
		return ErrDeciderRequired
	}
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return ErrRejectionReasonRequired
	}
	now := time.Now().UTC()
	c.State = ClaimStateRejected
	c.RejectionReason = trimmed
	c.DecidedByGCID = decider
	c.DecidedAt = &now
	return nil
}

// Disburse transitions APPROVED → DISBURSED. Idempotency / external SSG
// settlement-id capture is forward work; this method models the FSM gate only.
func (c *SkillsFuturesClaim) Disburse() error {
	if c.State != ClaimStateApproved {
		return ErrNotApproved
	}
	c.State = ClaimStateDisbursed
	return nil
}

// -----------------------------------------------------------------------------
// UUIDv7 — minimal local generator mirroring delivery.NewUUIDv7. Kept local
// to avoid a cross-package import from a sibling domain package (the
// `delivery` package is the only existing UUIDv7 generator; replicating the
// 30-line helper is cheaper than a new shared helper for a 1-domain feature).
// -----------------------------------------------------------------------------

func newUUIDv7() string {
	const buflen = 16
	var b [buflen]byte
	now := uint64(time.Now().UnixMilli())
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	if _, err := rand.Read(b[6:]); err != nil {
		for i := 6; i < buflen; i++ {
			b[i] = byte(now >> uint(8*(i-6)))
		}
	}
	// version 7 in the high nibble of byte 6
	b[6] = (b[6] & 0x0F) | 0x70
	// IETF variant in the high bits of byte 8
	b[8] = (b[8] & 0x3F) | 0x80
	return formatUUID(b[:])
}

func formatUUID(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 36)
	pos := 0
	for i, v := range b {
		switch i {
		case 4, 6, 8, 10:
			out[pos] = '-'
			pos++
		}
		out[pos] = hex[v>>4]
		out[pos+1] = hex[v&0x0F]
		pos += 2
	}
	return string(out)
}
