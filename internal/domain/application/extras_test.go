// Package application — S6.1 expansion tests (TDD strict).
//
// Drive the additions promoted from S4.3 stub:
//
//  1. Application carries: ClassID, FundingLines, SingpassSessionID,
//     OfferExpiresAt, AcceptedAt, PaidAt, EnrolledAt, WithdrawnAt,
//     RejectedReason, StripePaymentIntentID, InvoiceID.
//  2. Form schema query.
//  3. State history append on every transition.
//  4. NewIdempotencyKey + canonical event-payload helper.
//  5. Constructor accepts ClassID and validates UUID-shaped inputs.
//
// Per .claude/rules/development-execution.md: TDD strict. RED before GREEN.
package application_test

import (
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

const (
	classA = "01970000-0000-7000-8000-AAAAAAAAAAAA"
)

// -----------------------------------------------------------------------------
// Constructor — class + extra fields
// -----------------------------------------------------------------------------

func TestNewApplication_AcceptsClassID(t *testing.T) {
	t.Parallel()
	app, err := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA,
		CourseID: courseA,
		GCID:     gcidA,
		ClassID:  classA,
	})
	if err != nil {
		t.Fatalf("NewApplication: %v", err)
	}
	if app.ClassID != classA {
		t.Fatalf("class_id not stored: got %q", app.ClassID)
	}
}

func TestNewApplication_AllowsBlankClassID(t *testing.T) {
	t.Parallel()
	app, err := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA,
		CourseID: courseA,
		GCID:     gcidA,
	})
	if err != nil {
		t.Fatalf("NewApplication blank class: %v", err)
	}
	if app.ClassID != "" {
		t.Fatalf("class_id should default empty: got %q", app.ClassID)
	}
}

// -----------------------------------------------------------------------------
// IdempotencyKey for event publishing
// -----------------------------------------------------------------------------

func TestApplication_IdempotencyKey_Deterministic(t *testing.T) {
	t.Parallel()
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	k1 := application.IdempotencyKey(app, application.StatusSubmitted)
	k2 := application.IdempotencyKey(app, application.StatusSubmitted)
	if k1 != k2 {
		t.Fatalf("idempotency key not deterministic: %q vs %q", k1, k2)
	}
	if !strings.Contains(k1, app.ID) || !strings.Contains(k1, "submitted") {
		t.Fatalf("idempotency key should embed app_id+status: got %q", k1)
	}
}

// -----------------------------------------------------------------------------
// History append + SetExpectations on transition
// -----------------------------------------------------------------------------

func TestApplication_TransitionAppendsHistory(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusSubmitted)
	if got := len(app.History()); got != 1 {
		t.Fatalf("expected 1 history row after submit; got %d", got)
	}
	if app.History()[0].To != application.StatusSubmitted {
		t.Fatalf("history.to mismatch: got %q", app.History()[0].To)
	}
	if app.History()[0].From != application.StatusDraft {
		t.Fatalf("history.from mismatch: got %q", app.History()[0].From)
	}
	if app.History()[0].At.IsZero() {
		t.Fatalf("history.at must be set")
	}
}

func TestApplication_TransitionRejected_RecordsReason(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusUnderReview)
	if err := app.TransitionWithReason(application.StatusRejected, "missing prerequisites"); err != nil {
		t.Fatalf("reject with reason: %v", err)
	}
	if app.RejectedReason != "missing prerequisites" {
		t.Fatalf("rejected_reason not stored: got %q", app.RejectedReason)
	}
}

// -----------------------------------------------------------------------------
// Offer expiry + payment intent + invoice
// -----------------------------------------------------------------------------

func TestApplication_SetOfferExpiry(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusOfferMade)
	exp := time.Now().UTC().Add(72 * time.Hour)
	app.SetOfferExpiresAt(exp)
	if !app.OfferExpiresAt.Equal(exp) {
		t.Fatalf("offer_expires_at not set: got %v", app.OfferExpiresAt)
	}
}

func TestApplication_AttachPaymentIntent(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusAccepted)
	app.AttachPaymentIntent("pi_test_abc123")
	if app.StripePaymentIntentID != "pi_test_abc123" {
		t.Fatalf("payment_intent not attached: got %q", app.StripePaymentIntentID)
	}
}

func TestApplication_AttachInvoice(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusPaid)
	app.AttachInvoice("inv-uuid-123")
	if app.InvoiceID != "inv-uuid-123" {
		t.Fatalf("invoice_id not attached: got %q", app.InvoiceID)
	}
}

// -----------------------------------------------------------------------------
// Funding lines (form-schema skeleton)
// -----------------------------------------------------------------------------

func TestApplication_SetFundingLines(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusSubmitted)
	lines := []application.FundingLine{
		{Type: application.FundingTypeSkillsFutureCredits, MaxAmountSGDCents: 50000, Eligible: true},
		{Type: application.FundingTypeMidCareer, MaxAmountSGDCents: 42000, Eligible: false},
	}
	app.SetFundingLines(lines)
	if got := len(app.FundingLines); got != 2 {
		t.Fatalf("funding lines: got %d", got)
	}
	if app.FundingLines[0].Type != application.FundingTypeSkillsFutureCredits {
		t.Fatalf("first funding line type: got %q", app.FundingLines[0].Type)
	}
}

// -----------------------------------------------------------------------------
// Singpass session
// -----------------------------------------------------------------------------

func TestApplication_AttachSingpass(t *testing.T) {
	t.Parallel()
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	app.AttachSingpassSession("singpass-session-uuid")
	if app.SingpassSessionID != "singpass-session-uuid" {
		t.Fatalf("singpass_session_id not stored: got %q", app.SingpassSessionID)
	}
}

// -----------------------------------------------------------------------------
// FormSchema computation
// -----------------------------------------------------------------------------

func TestFormSchema_FreeCourse_DoesNotRequireFunding(t *testing.T) {
	t.Parallel()
	schema := application.FormSchemaFor(application.FormSchemaInput{
		CourseTitle:       "CSM Prep",
		PriceSGDCents:     0,
		SFEligible:        false,
		SingpassPrefill:   true,
		TuitionGrossCents: 0,
	})
	if !schema.SingpassPrefill {
		t.Fatalf("expected singpass_prefill=true")
	}
	if schema.NetAmountCents != 0 {
		t.Fatalf("free course net=0; got %d", schema.NetAmountCents)
	}
	if schema.GSTAmountCents != 0 {
		t.Fatalf("free course GST=0; got %d", schema.GSTAmountCents)
	}
}

func TestFormSchema_PaidCourse_GST9Pct(t *testing.T) {
	t.Parallel()
	// Tuition gross 100000 cents = SGD 1000, GST 9% = 9000 cents.
	schema := application.FormSchemaFor(application.FormSchemaInput{
		CourseTitle:       "PMP",
		PriceSGDCents:     100000,
		TuitionGrossCents: 100000,
		SingpassPrefill:   false,
	})
	if schema.GSTAmountCents != 9000 {
		t.Fatalf("expected GST 9000 (9pct); got %d", schema.GSTAmountCents)
	}
	if schema.NetAmountCents != 109000 {
		t.Fatalf("expected net 109000 (gross+GST); got %d", schema.NetAmountCents)
	}
}

func TestFormSchema_SkillsFutureFunding_AppliedToNet(t *testing.T) {
	t.Parallel()
	schema := application.FormSchemaFor(application.FormSchemaInput{
		CourseTitle:       "ACSM",
		PriceSGDCents:     210000,
		TuitionGrossCents: 210000,
		FundingLines: []application.FundingLine{
			{Type: application.FundingTypeSkillsFutureCredits, MaxAmountSGDCents: 50000, Eligible: true},
		},
	})
	// Gross 210000 + GST 18900 = 228900; funding 50000 → net = 178900.
	wantNet := int64(228900 - 50000)
	if schema.NetAmountCents != wantNet {
		t.Fatalf("net with funding: got %d want %d", schema.NetAmountCents, wantNet)
	}
}

// -----------------------------------------------------------------------------
// CanonicalEventPayload — used by event publisher
// -----------------------------------------------------------------------------

func TestCanonicalEventPayload_HasCourseAndStatus(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusSubmitted)
	payload := application.CanonicalEventPayload(app)
	if payload["application_id"] != app.ID {
		t.Fatalf("payload missing application_id")
	}
	if payload["course_id"] != app.CourseID {
		t.Fatalf("payload missing course_id")
	}
	if payload["status"] != string(app.Status) {
		t.Fatalf("payload missing status")
	}
	if payload["learner_gcid"] != app.GCID {
		t.Fatalf("payload missing learner_gcid")
	}
}

// -----------------------------------------------------------------------------
// EventTopicFor — maps status → topic
// -----------------------------------------------------------------------------

func TestEventTopicFor_AllStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s    application.Status
		want string
	}{
		{application.StatusSubmitted, "chora.delivery.application.submitted.v1"},
		{application.StatusUnderReview, "chora.delivery.application.under_review.v1"},
		{application.StatusOfferMade, "chora.delivery.application.offer_made.v1"},
		{application.StatusAccepted, "chora.delivery.application.accepted.v1"},
		{application.StatusWithdrawn, "chora.delivery.application.withdrawn.v1"},
		{application.StatusPaid, "chora.delivery.application.paid.v1"},
		{application.StatusEnrolled, "chora.delivery.application.enrolled.v1"},
		{application.StatusRejected, "chora.delivery.application.rejected.v1"},
	}
	for _, c := range cases {
		if got := application.EventTopicFor(c.s); got != c.want {
			t.Fatalf("EventTopicFor(%s): got %q want %q", c.s, got, c.want)
		}
	}
}

func TestEventTopicFor_DraftReturnsEmpty(t *testing.T) {
	t.Parallel()
	if got := application.EventTopicFor(application.StatusDraft); got != "" {
		t.Fatalf("draft has no topic; got %q", got)
	}
}

// -----------------------------------------------------------------------------
// IMDADimensionFor — ADR-141 mapping
// -----------------------------------------------------------------------------

func TestIMDADimensionFor_PaidEnrolled_Transparency(t *testing.T) {
	t.Parallel()
	cases := []application.Status{application.StatusPaid, application.StatusEnrolled}
	for _, s := range cases {
		if got := application.IMDADimensionFor(s); got != "transparency" {
			t.Fatalf("IMDADimensionFor(%s)=%q want transparency", s, got)
		}
	}
}

func TestIMDADimensionFor_OtherStatuses_Accountability(t *testing.T) {
	t.Parallel()
	cases := []application.Status{
		application.StatusSubmitted,
		application.StatusUnderReview,
		application.StatusOfferMade,
		application.StatusAccepted,
		application.StatusWithdrawn,
		application.StatusRejected,
	}
	for _, s := range cases {
		if got := application.IMDADimensionFor(s); got != "accountability" {
			t.Fatalf("IMDADimensionFor(%s)=%q want accountability", s, got)
		}
	}
}

// -----------------------------------------------------------------------------
// Withdraw with reason
// -----------------------------------------------------------------------------

func TestApplication_TransitionWithdraw_RecordsReason(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusOfferMade)
	if err := app.TransitionWithReason(application.StatusWithdrawn, "changed mind"); err != nil {
		t.Fatalf("withdraw with reason: %v", err)
	}
	if app.WithdrawnReason != "changed mind" {
		t.Fatalf("withdrawn_reason: got %q", app.WithdrawnReason)
	}
}

func TestApplication_TransitionWithReason_IllegalEdge(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusOfferMade)
	if err := app.TransitionWithReason(application.StatusDraft, "any"); err == nil {
		t.Fatalf("expected error on illegal transition with reason")
	}
}

// -----------------------------------------------------------------------------
// CanonicalEventPayload — exercise optional fields
// -----------------------------------------------------------------------------

func TestCanonicalEventPayload_IncludesPaymentIntent(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusAccepted)
	app.AttachPaymentIntent("pi_test_xyz")
	app.AttachSingpassSession("sp-sess-1")
	app.SetOfferExpiresAt(time.Now().UTC().Add(48 * time.Hour))
	app.AttachInvoice("inv-1")
	if err := app.TransitionWithReason(application.StatusWithdrawn, "the user gave up"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	payload := application.CanonicalEventPayload(app)
	if payload["stripe_payment_intent_id"] != "pi_test_xyz" {
		t.Fatalf("payload missing payment_intent")
	}
	if payload["singpass_session_id"] != "sp-sess-1" {
		t.Fatalf("payload missing singpass_session_id")
	}
	if payload["invoice_id"] != "inv-1" {
		t.Fatalf("payload missing invoice_id")
	}
	if payload["withdrawn_reason"] != "the user gave up" {
		t.Fatalf("payload missing withdrawn_reason")
	}
	if payload["offer_expires_at"] == nil {
		t.Fatalf("payload missing offer_expires_at")
	}
}

func TestCanonicalEventPayload_IncludesClassID(t *testing.T) {
	t.Parallel()
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA, ClassID: classA,
	})
	payload := application.CanonicalEventPayload(app)
	if payload["class_id"] != classA {
		t.Fatalf("payload missing class_id")
	}
}

func TestCanonicalEventPayload_IncludesRejectedReason(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusUnderReview)
	if err := app.TransitionWithReason(application.StatusRejected, "no certs"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	payload := application.CanonicalEventPayload(app)
	if payload["rejected_reason"] != "no certs" {
		t.Fatalf("rejected_reason missing")
	}
}

// -----------------------------------------------------------------------------
// FormSchemaFor — net amount cannot go negative
// -----------------------------------------------------------------------------

func TestFormSchema_FundingExceedsNet_ClampsToZero(t *testing.T) {
	t.Parallel()
	schema := application.FormSchemaFor(application.FormSchemaInput{
		CourseTitle:       "Test",
		PriceSGDCents:     5000,
		TuitionGrossCents: 5000,
		FundingLines: []application.FundingLine{
			{Type: application.FundingTypeSkillsFutureCredits, MaxAmountSGDCents: 50000, Eligible: true},
		},
	})
	if schema.NetAmountCents != 0 {
		t.Fatalf("net should clamp to 0 when funding > gross+gst; got %d", schema.NetAmountCents)
	}
}

func TestFormSchema_IneligibleFundingLine_NotApplied(t *testing.T) {
	t.Parallel()
	schema := application.FormSchemaFor(application.FormSchemaInput{
		CourseTitle:       "Test",
		PriceSGDCents:     100000,
		TuitionGrossCents: 100000,
		FundingLines: []application.FundingLine{
			{Type: application.FundingTypeSkillsFutureCredits, MaxAmountSGDCents: 50000, Eligible: false},
		},
	})
	// gross 100000 + gst 9000 = 109000; 0 funding (ineligible).
	if schema.NetAmountCents != 109000 {
		t.Fatalf("ineligible funding should not apply; got %d", schema.NetAmountCents)
	}
}

// TestApplication_HistoryPendingTracking exercises the Wave-2 follow-up
// pending-tracking that lets a pg adapter rehydrate the full trail on Get
// while Save inserts only newly-appended transitions (no duplication).
func TestApplication_HistoryPendingTracking(t *testing.T) {
	t.Parallel()
	a, err := application.NewApplication(application.NewApplicationInput{
		TenantID: "t-1", CourseID: "c-1", GCID: "g-1",
	})
	if err != nil {
		t.Fatalf("NewApplication: %v", err)
	}
	// Fresh aggregate: nothing transitioned, nothing pending.
	if got := a.PendingHistory(); len(got) != 0 {
		t.Fatalf("fresh aggregate pending=%d want 0", len(got))
	}
	// Two transitions → two pending.
	if err := a.Transition(application.StatusSubmitted); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := a.Transition(application.StatusUnderReview); err != nil {
		t.Fatalf("under_review: %v", err)
	}
	if got := a.PendingHistory(); len(got) != 2 {
		t.Fatalf("pending after 2 transitions=%d want 2", len(got))
	}
	// Persist → nothing pending; History() still shows the full trail.
	a.MarkHistoryPersisted()
	if got := a.PendingHistory(); len(got) != 0 {
		t.Fatalf("pending after MarkHistoryPersisted=%d want 0", len(got))
	}
	if got := a.History(); len(got) != 2 {
		t.Fatalf("History after persist=%d want 2 (trail retained)", len(got))
	}
	// A further transition is pending again (only the new one).
	if err := a.Transition(application.StatusOfferMade); err != nil {
		t.Fatalf("offer_made: %v", err)
	}
	if got := a.PendingHistory(); len(got) != 1 || got[0].To != application.StatusOfferMade {
		t.Fatalf("pending after 3rd transition=%+v want 1 (offer_made)", got)
	}

	// ReplaceHistory (pg Get rehydration) marks the loaded trail persisted.
	b, _ := application.NewApplication(application.NewApplicationInput{TenantID: "t-1", CourseID: "c-1", GCID: "g-2"})
	b.ReplaceHistory([]application.HistoryEntry{
		{From: application.StatusDraft, To: application.StatusSubmitted, At: time.Now().UTC()},
	})
	if got := b.PendingHistory(); len(got) != 0 {
		t.Fatalf("rehydrated pending=%d want 0 (all loaded rows are durable)", len(got))
	}
	if got := b.History(); len(got) != 1 {
		t.Fatalf("rehydrated History=%d want 1", len(got))
	}
	// A new transition after rehydration is the only pending row.
	if err := b.Transition(application.StatusSubmitted); err != nil {
		t.Fatalf("submit (b): %v", err)
	}
	if got := b.PendingHistory(); len(got) != 1 {
		t.Fatalf("pending after rehydrate+transition=%d want 1 (not the rehydrated row)", len(got))
	}
}
