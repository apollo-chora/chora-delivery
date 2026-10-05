// Package application is the Course Application aggregate (S6 prep).
//
// Per docs/design/ux_course_application.md the Course Application UX is
// the apply→Stripe→tax-invoice flow that drives a paid course enrolment.
// At S4.3 we ship the **state machine ONLY**:
//
//	Draft → Submitted → UnderReview → OfferMade → Accepted → Paid → Enrolled
//	                                            \→ Withdrawn (any post-Submitted state)
//	                                            \→ Rejected   (UnderReview only)
//
// S6 will layer on:
//   - Stripe Elements payment intent + webhook handler
//   - Singpass MyInfo retrieve callback
//   - tax-invoice PDF generation
//   - per-stage event emission (chora.delivery.application.*.v1 — already
//     declared in chora-contracts/asyncapi/delivery/application.*)
//   - Postgres adapter (chora_delivery DB)
//
// HARD INVARIANT (per .claude/rules/ddd-enforcement.md): this package must
// NEVER import a Stripe SDK, an HTTP client, or a DB driver. The state
// machine is pure domain logic. S6 wires adapters.
package application

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	// ErrInvalidArgument signals a guard-clause failure inside a constructor.
	ErrInvalidArgument = errors.New("invalid argument")

	// ErrIllegalTransition is returned when Transition() is asked to follow
	// a path that the state machine does not allow.
	ErrIllegalTransition = errors.New("illegal application status transition")
)

// -----------------------------------------------------------------------------
// Status enum + transition table
// -----------------------------------------------------------------------------

// Status is the lifecycle state of a Course Application.
type Status string

const (
	StatusDraft       Status = "draft"
	StatusSubmitted   Status = "submitted"
	StatusUnderReview Status = "under_review"
	StatusOfferMade   Status = "offer_made"
	StatusAccepted    Status = "accepted"
	StatusPaid        Status = "paid"
	StatusEnrolled    Status = "enrolled"
	StatusWithdrawn   Status = "withdrawn" // terminal
	StatusRejected    Status = "rejected"  // terminal
)

// IsValid reports whether s is a recognised status.
func (s Status) IsValid() bool {
	switch s {
	case StatusDraft, StatusSubmitted, StatusUnderReview, StatusOfferMade,
		StatusAccepted, StatusPaid, StatusEnrolled, StatusWithdrawn, StatusRejected:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether s admits no outgoing transitions.
func (s Status) IsTerminal() bool {
	return s == StatusEnrolled || s == StatusWithdrawn || s == StatusRejected
}

// allowedTransitions is the directed edge map from each Status to its
// admissible successors. Edges that are NOT in this map are rejected.
var allowedTransitions = map[Status][]Status{
	StatusDraft:       {StatusSubmitted},
	StatusSubmitted:   {StatusUnderReview, StatusWithdrawn},
	StatusUnderReview: {StatusOfferMade, StatusRejected, StatusWithdrawn},
	StatusOfferMade:   {StatusAccepted, StatusWithdrawn},
	StatusAccepted:    {StatusPaid, StatusWithdrawn},
	StatusPaid:        {StatusEnrolled},
	// terminal — no outgoing edges
	StatusEnrolled:  {},
	StatusWithdrawn: {},
	StatusRejected:  {},
}

// -----------------------------------------------------------------------------
// Aggregate
// -----------------------------------------------------------------------------

// Application is a learner's course-application aggregate.
//
// Carries identity references (TenantID, CourseID, ClassID, GCID), state +
// timestamps for each terminal transition, payment + invoice attachments,
// Singpass session, append-only state history, and form-derived funding
// lines. The S6.1 expansion adds everything beyond the original 7-field
// stub from S4.3.
type Application struct {
	ID        string
	TenantID  string
	CourseID  string
	ClassID   string // optional — chosen class instance
	GCID      string // applicant's GCID
	Status    Status
	CreatedAt time.Time
	UpdatedAt time.Time

	// Lifecycle timestamps — stamped on TransitionWithReason when the state
	// machine reaches each terminal-or-near-terminal step.
	OfferExpiresAt time.Time
	AcceptedAt     time.Time
	PaidAt         time.Time
	EnrolledAt     time.Time
	WithdrawnAt    time.Time

	// External integrations.
	StripePaymentIntentID string // Stripe PaymentIntent ID after CreatePaymentIntent.
	InvoiceID             string // Tax-invoice aggregate ID (append-only post-payment).
	SingpassSessionID     string // Audit trail for Singpass MyInfo retrieval.

	// Decision metadata.
	RejectedReason  string // Set when TransitionWithReason(StatusRejected, …) called.
	WithdrawnReason string // Set when TransitionWithReason(StatusWithdrawn, …) called.

	// Funding skeleton — full SkillsFutures integration deferred to M17.
	FundingLines []FundingLine

	// State history — append-only, set by Transition.
	history []HistoryEntry

	// historyPersisted tracks how many leading history[] entries are already
	// durably written. Set by ReplaceHistory (on pg Get rehydration) +
	// MarkHistoryPersisted (after a successful pg write). PendingHistory()
	// returns history[historyPersisted:], so the pg adapter writes ONLY
	// un-persisted transitions — a Get that rehydrates the full trail does NOT
	// make the next Save re-insert it (the duplication that blocked Get-side
	// history hydration in Wave 2). The in-memory adapter never touches this
	// (it keeps the full history in-process across requests).
	historyPersisted int

	mu sync.Mutex
}

// NewApplicationInput is the value-bag for NewApplication.
type NewApplicationInput struct {
	TenantID string
	CourseID string
	ClassID  string // optional
	GCID     string
}

// NewApplication constructs a Draft application with a UUIDv7 ID.
func NewApplication(in NewApplicationInput) (*Application, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.CourseID) == "" {
		return nil, fmt.Errorf("%w: course_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.GCID) == "" {
		return nil, fmt.Errorf("%w: gcid required", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &Application{
		ID:        newUUIDv7(),
		TenantID:  in.TenantID,
		CourseID:  in.CourseID,
		ClassID:   strings.TrimSpace(in.ClassID),
		GCID:      in.GCID,
		Status:    StatusDraft,
		CreatedAt: now,
		UpdatedAt: now,
		history:   []HistoryEntry{},
	}, nil
}

// Transition advances the application state along an edge in the
// allowed-transition map.
func (a *Application) Transition(next Status) error {
	if !next.IsValid() {
		return fmt.Errorf("%w: unknown status %q", ErrIllegalTransition, next)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Status.IsTerminal() {
		return fmt.Errorf("%w: %s is terminal", ErrIllegalTransition, a.Status)
	}
	allowed, ok := allowedTransitions[a.Status]
	if !ok {
		return fmt.Errorf("%w: no transitions from %s", ErrIllegalTransition, a.Status)
	}
	for _, candidate := range allowed {
		if candidate == next {
			from := a.Status
			a.Status = next
			a.UpdatedAt = time.Now().UTC()
			a.history = append(a.history, HistoryEntry{
				From: from,
				To:   next,
				At:   a.UpdatedAt,
			})
			// Stamp lifecycle timestamps for terminal-or-near-terminal steps
			// (TransitionWithReason re-stamps when invoked, which is benign).
			switch next {
			case StatusAccepted:
				a.AcceptedAt = a.UpdatedAt
			case StatusPaid:
				a.PaidAt = a.UpdatedAt
			case StatusEnrolled:
				a.EnrolledAt = a.UpdatedAt
			case StatusWithdrawn:
				a.WithdrawnAt = a.UpdatedAt
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %s -> %s not allowed", ErrIllegalTransition, a.Status, next)
}

// -----------------------------------------------------------------------------
// UUIDv7 — local generator (kept package-local to avoid a delivery import)
// -----------------------------------------------------------------------------

// NewUUIDv7 returns a fresh UUIDv7 — exposed so adapters (e.g. pg history rows)
// can mint identifiers without re-importing a UUID library.
func NewUUIDv7() string { return newUUIDv7() }

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
	b[6] = (b[6] & 0x0F) | 0x70
	b[8] = (b[8] & 0x3F) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
