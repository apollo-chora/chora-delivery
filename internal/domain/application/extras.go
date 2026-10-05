// Package application — S6.1 expansion: full course-application aggregate
// (Stripe + Singpass + invoice + persistence layers wire on top).
//
// Per docs/design/ux_course_application.md and skill domain-content-delivery,
// the Application aggregate carries: applicant identity references (GCID),
// course + class refs, state machine (defined in application.go), payment
// intent ID (Stripe), invoice ID (PDF reference), Singpass session for audit,
// funding-line skeleton (SkillsFuture deferred), and append-only state
// history.
//
// Hexagonal: this file stays inside the domain (no SDK / DB / HTTP imports).
// Adapters wire Stripe / Singpass / GCS / Postgres above this layer.
package application

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// FundingLine — SkillsFuture / Mid-Career / WSG ETSS skeleton
// -----------------------------------------------------------------------------

// FundingType is the funding source declared in the application form.
type FundingType string

const (
	FundingTypeSkillsFutureCredits FundingType = "skillsfuture_credits"
	FundingTypeMidCareer           FundingType = "mid_career_enhanced_subsidy"
	FundingTypeWSGETSS             FundingType = "wsg_etss"
)

// FundingLine is one funding entry on the form schema. Currently a skeleton
// (full SSG integration deferred to M17 SG-gov) but the shape is fixed so the
// invoice + form schema agree on field names.
type FundingLine struct {
	Type              FundingType `json:"type"`
	MaxAmountSGDCents int64       `json:"max_amount_sgd_cents"`
	Eligible          bool        `json:"eligible"`
}

// -----------------------------------------------------------------------------
// HistoryEntry — append-only state-transition record
// -----------------------------------------------------------------------------

// HistoryEntry is one state-transition record. The repository persists this
// in the `application_state_history` table.
type HistoryEntry struct {
	From   Status    `json:"from"`
	To     Status    `json:"to"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
}

// -----------------------------------------------------------------------------
// Mutators — payment / invoice / Singpass / funding
// -----------------------------------------------------------------------------

// SetOfferExpiresAt records the offer expiry (set by the admin / R+ side
// when transitioning to OfferMade). Persisted so A-CRS-4's countdown reads
// the same value across reloads.
func (a *Application) SetOfferExpiresAt(t time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.OfferExpiresAt = t.UTC()
	a.UpdatedAt = time.Now().UTC()
}

// AttachPaymentIntent stores the Stripe PaymentIntent ID for the application.
// Called from the Stripe adapter after CreatePaymentIntent succeeds.
func (a *Application) AttachPaymentIntent(piID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.StripePaymentIntentID = strings.TrimSpace(piID)
	a.UpdatedAt = time.Now().UTC()
}

// AttachInvoice stores the tax-invoice ID after PDF generation.
// Append-only: invoice never changes post-issuance (regulatory).
func (a *Application) AttachInvoice(invoiceID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.InvoiceID = strings.TrimSpace(invoiceID)
	a.UpdatedAt = time.Now().UTC()
}

// AttachSingpassSession stores the Singpass session ID for audit.
// Audit-only: payload retrieved via the Singpass adapter is NOT cached
// here (out of domain) — it's persisted on the application form sub-records.
func (a *Application) AttachSingpassSession(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SingpassSessionID = strings.TrimSpace(sessionID)
	a.UpdatedAt = time.Now().UTC()
}

// SetFundingLines stores the form-schema-derived funding lines.
func (a *Application) SetFundingLines(lines []FundingLine) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.FundingLines = make([]FundingLine, len(lines))
	copy(a.FundingLines, lines)
	a.UpdatedAt = time.Now().UTC()
}

// History returns a snapshot copy of state transitions.
func (a *Application) History() []HistoryEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]HistoryEntry, len(a.history))
	copy(out, a.history)
	return out
}

// ReplaceHistory overwrites the append-only history slice with a defensive
// copy and marks ALL of it persisted. Used ONLY by the pg adapter to rehydrate
// the trail loaded from application_state_history on Get — so the detail-view
// DTO shows the same transitions the in-memory adapter keeps in-process, while
// PendingHistory() reports nothing new (the rehydrated rows are already
// durable). NOT a domain mutation: no validation, no state change, no
// UpdatedAt bump.
func (a *Application) ReplaceHistory(h []HistoryEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = append([]HistoryEntry(nil), h...)
	a.historyPersisted = len(a.history)
}

// PendingHistory returns a copy of the history entries appended since the last
// load/persist (history[historyPersisted:]) — the transitions a pg Save must
// insert into application_state_history. Returns empty when nothing is pending.
func (a *Application) PendingHistory() []HistoryEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.historyPersisted >= len(a.history) {
		return nil
	}
	pending := a.history[a.historyPersisted:]
	out := make([]HistoryEntry, len(pending))
	copy(out, pending)
	return out
}

// MarkHistoryPersisted records that every current history entry is durably
// written. The pg adapter calls this AFTER its write transaction commits, so a
// failed commit leaves the entries pending for the next Save (no silent loss).
func (a *Application) MarkHistoryPersisted() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.historyPersisted = len(a.history)
}

// TransitionWithReason is Transition() with an extra `reason` string the
// state-history row records. For Status==Rejected it also stamps
// app.RejectedReason for fast read-side access.
func (a *Application) TransitionWithReason(next Status, reason string) error {
	from := a.Status
	if err := a.Transition(next); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if next == StatusRejected {
		a.RejectedReason = reason
	}
	if next == StatusWithdrawn {
		a.WithdrawnReason = reason
	}
	// Re-stamp the most recent history entry with the reason.
	if n := len(a.history); n > 0 {
		a.history[n-1].Reason = reason
		a.history[n-1].From = from
	}
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

// -----------------------------------------------------------------------------
// IdempotencyKey + EventTopicFor + CanonicalEventPayload
// -----------------------------------------------------------------------------

// IdempotencyKey returns the deterministic Pub/Sub idempotency key for an
// application transition. Format: application:{app_id}:{status}.
//
// Re-firing publish with the same (app_id, status) at the bus deduplicates
// to a single delivery.
func IdempotencyKey(a *Application, s Status) string {
	return fmt.Sprintf("application:%s:%s", a.ID, s)
}

// EventTopicFor maps a status to the canonical Pub/Sub topic name. Returns
// empty for the Draft state (Draft is private to the learner; no event).
func EventTopicFor(s Status) string {
	switch s {
	case StatusSubmitted:
		return "chora.delivery.application.submitted.v1"
	case StatusUnderReview:
		return "chora.delivery.application.under_review.v1"
	case StatusOfferMade:
		return "chora.delivery.application.offer_made.v1"
	case StatusAccepted:
		return "chora.delivery.application.accepted.v1"
	case StatusWithdrawn:
		return "chora.delivery.application.withdrawn.v1"
	case StatusRejected:
		return "chora.delivery.application.rejected.v1"
	case StatusPaid:
		return "chora.delivery.application.paid.v1"
	case StatusEnrolled:
		return "chora.delivery.application.enrolled.v1"
	default:
		return ""
	}
}

// CanonicalEventPayload returns the canonical JSON-friendly map representing
// an application — used by the event publisher to populate the event payload.
//
// IMDA dimension is decided by the publisher per ADR-141 mapping:
//   - StatusSubmitted/UnderReview/OfferMade/Accepted/Withdrawn/Rejected → accountability
//   - StatusPaid/Enrolled → transparency (audit trail)
func CanonicalEventPayload(a *Application) map[string]interface{} {
	out := map[string]interface{}{
		"application_id": a.ID,
		"tenant_id":      a.TenantID,
		"course_id":      a.CourseID,
		"learner_gcid":   a.GCID,
		"status":         string(a.Status),
		"created_at":     a.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":     a.UpdatedAt.Format(time.RFC3339Nano),
	}
	if a.ClassID != "" {
		out["class_id"] = a.ClassID
	}
	if !a.OfferExpiresAt.IsZero() {
		out["offer_expires_at"] = a.OfferExpiresAt.Format(time.RFC3339Nano)
	}
	if a.StripePaymentIntentID != "" {
		out["stripe_payment_intent_id"] = a.StripePaymentIntentID
	}
	if a.InvoiceID != "" {
		out["invoice_id"] = a.InvoiceID
	}
	if a.SingpassSessionID != "" {
		out["singpass_session_id"] = a.SingpassSessionID
	}
	if a.RejectedReason != "" {
		out["rejected_reason"] = a.RejectedReason
	}
	if a.WithdrawnReason != "" {
		out["withdrawn_reason"] = a.WithdrawnReason
	}
	return out
}

// IMDADimensionFor returns the canonical ADR-141 IMDA dimension for the
// given status. Per ADR-141 every state transition is D1 accountability;
// Paid + Enrolled additionally carry D2 transparency for the audit trail.
func IMDADimensionFor(s Status) string {
	switch s {
	case StatusPaid, StatusEnrolled:
		return "transparency"
	default:
		return "accountability"
	}
}

// -----------------------------------------------------------------------------
// FormSchema — what GET /v1/courses/{id}/application-form returns
// -----------------------------------------------------------------------------

// FormSchemaInput is the value-bag for FormSchemaFor.
type FormSchemaInput struct {
	CourseTitle       string
	PriceSGDCents     int64
	TuitionGrossCents int64
	SFEligible        bool
	SingpassPrefill   bool
	FundingLines      []FundingLine
}

// FormSchema is the response shape for the GET application-form endpoint.
// Contains the displayed pricing breakdown so the front end never has to
// recompute it.
type FormSchema struct {
	CourseTitle        string        `json:"course_title"`
	SingpassPrefill    bool          `json:"singpass_prefill"`
	TuitionGrossCents  int64         `json:"tuition_gross_cents"`
	GSTAmountCents     int64         `json:"gst_amount_cents"` // SG GST 9%
	FundingLines       []FundingLine `json:"funding_lines"`
	NetAmountCents     int64         `json:"net_amount_cents"` // gross + GST − funding
	Currency           string        `json:"currency"`         // SGD
	GSTRateBasisPoints int           `json:"gst_rate_basis_points"`
}

// gstBasisPointsSGD is Singapore IRAS GST rate (9%) expressed in basis points.
const gstBasisPointsSGD = 900 // 9% = 900 / 10000

// FormSchemaFor computes the form-schema for a course.
//
// Math:
//
//	gst    = round(gross * 0.09)
//	funded = sum(eligible funding lines)
//	net    = max(0, gross + gst − funded)
//
// Free courses (gross == 0) bypass GST + funding entirely.
func FormSchemaFor(in FormSchemaInput) FormSchema {
	gross := in.TuitionGrossCents
	if gross == 0 {
		return FormSchema{
			CourseTitle:        in.CourseTitle,
			SingpassPrefill:    in.SingpassPrefill,
			TuitionGrossCents:  0,
			GSTAmountCents:     0,
			FundingLines:       []FundingLine{},
			NetAmountCents:     0,
			Currency:           "SGD",
			GSTRateBasisPoints: gstBasisPointsSGD,
		}
	}
	gst := (gross * int64(gstBasisPointsSGD)) / 10000
	var funded int64
	for _, line := range in.FundingLines {
		if line.Eligible {
			funded += line.MaxAmountSGDCents
		}
	}
	net := gross + gst - funded
	if net < 0 {
		net = 0
	}
	lines := make([]FundingLine, len(in.FundingLines))
	copy(lines, in.FundingLines)
	return FormSchema{
		CourseTitle:        in.CourseTitle,
		SingpassPrefill:    in.SingpassPrefill,
		TuitionGrossCents:  gross,
		GSTAmountCents:     gst,
		FundingLines:       lines,
		NetAmountCents:     net,
		Currency:           "SGD",
		GSTRateBasisPoints: gstBasisPointsSGD,
	}
}

// -----------------------------------------------------------------------------
// Compile-time check: keep mu inheritance from the original aggregate.
// -----------------------------------------------------------------------------

var _ sync.Locker = (*sync.Mutex)(nil)
