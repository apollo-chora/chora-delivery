// payments_subscriber_test.go — unit tests for the chora.payments.* event
// subscribers landed at Stage C of ADR-164.
//
// Tests cover the 4 canonical events chora-delivery cares about:
//  1. chora.payments.course_purchase.payment_captured.v1
//  2. chora.payments.course_purchase.refunded.v1
//  3. chora.payments.application_payment.payment_captured.v1
//  4. chora.payments.application_payment.refunded.v1
//
// Each event gets:
//   - happy path (state transition + downstream emit)
//   - idempotent replay (second delivery is a no-op)
//   - envelope validation (missing event_id → error)
//
// Hexagonal: ADAPTER test. Tests live alongside the implementation in
// package events.
package events

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/payments/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func newTestEnvelope(eventID, tenantID, gcid string) *commonv1.EventEnvelope {
	now := timestamppb.Now()
	return &commonv1.EventEnvelope{
		EventId:        eventID,
		IdempotencyKey: eventID,
		TenantId:       tenantID,
		Gcid:           gcid,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-payments",
		SchemaVersion:  1,
	}
}

func newPaymentsSubscriberForTest(t *testing.T) (*PaymentsSubscriber, *domain.EnrollmentRegistry, *repoinmem.ApplicationRepo, *InMemoryPublisher) {
	t.Helper()
	enrollments := domain.NewEnrollmentRegistry()
	applications := repoinmem.NewApplicationRepo()
	pub := NewInMemoryPublisher("chora-489812", "chora-delivery")
	tracker := NewEnrollmentTracker(domain.NewInMemEnrollmentStoreFrom(enrollments))
	sub := NewPaymentsSubscriber(PaymentsSubscriberDeps{
		Enrollments:  tracker,
		Applications: applications,
		Publisher:    pub,
		Inbox:        idempotent.NewMemoryStore(),
	})
	// 2nd return is the underlying registry — tests seed + assert via its
	// ctx-free API; the tracker wraps it as an EnrollmentPort.
	return sub, enrollments, applications, pub
}

// -----------------------------------------------------------------------------
// course_purchase.payment_captured.v1 — happy path + idempotent replay
// -----------------------------------------------------------------------------

func TestPaymentsSubscriber_CoursePurchasePaymentCaptured_RegistersEnrollment(t *testing.T) {
	t.Parallel()
	sub, tracker, _, pub := newPaymentsSubscriberForTest(t)
	ev := &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope:              newTestEnvelope("evt-1", "tenant-1", "learner-1"),
		PurchaseId:            "purchase-1",
		LearnerGcid:           "learner-1",
		CourseId:              "course-1",
		StripeSessionId:       "cs_test_1",
		StripePaymentIntentId: "pi_test_1",
		StripeChargeId:        "ch_test_1",
		AmountCentsPaid:       19900,
		Currency:              "SGD",
	}
	if err := sub.HandleCoursePurchasePaymentCaptured(context.Background(), ev); err != nil {
		t.Fatalf("first delivery returned error: %v", err)
	}
	enr, ok := tracker.GetByCourseAndGCID("tenant-1", "course-1", "learner-1")
	if !ok {
		t.Fatal("enrollment not registered")
	}
	if enr.CourseID != "course-1" || enr.GCID != "learner-1" {
		t.Errorf("enrollment fields wrong: course=%q gcid=%q", enr.CourseID, enr.GCID)
	}
	// Downstream enrollment.created.v1 event emitted.
	hist := pub.History()
	found := false
	for _, e := range hist {
		if e.Topic == TopicEnrollmentCreated {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %s in publisher history, got %v", TopicEnrollmentCreated, hist)
	}
}

func TestPaymentsSubscriber_CoursePurchasePaymentCaptured_IdempotentReplay(t *testing.T) {
	t.Parallel()
	sub, _, _, pub := newPaymentsSubscriberForTest(t)
	ev := &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope:        newTestEnvelope("evt-1", "tenant-1", "learner-1"),
		PurchaseId:      "purchase-1",
		LearnerGcid:     "learner-1",
		CourseId:        "course-1",
		AmountCentsPaid: 19900,
		Currency:        "SGD",
	}
	// First delivery — should succeed.
	if err := sub.HandleCoursePurchasePaymentCaptured(context.Background(), ev); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	beforeReplayCount := len(pub.History())
	// Second delivery (same event_id) — should be a no-op.
	if err := sub.HandleCoursePurchasePaymentCaptured(context.Background(), ev); err != nil {
		t.Fatalf("replay delivery: %v", err)
	}
	afterReplayCount := len(pub.History())
	if beforeReplayCount != afterReplayCount {
		t.Errorf("idempotent replay emitted duplicate downstream event(s); before=%d after=%d",
			beforeReplayCount, afterReplayCount)
	}
}

func TestPaymentsSubscriber_CoursePurchasePaymentCaptured_MissingEnvelope(t *testing.T) {
	t.Parallel()
	sub, _, _, _ := newPaymentsSubscriberForTest(t)
	ev := &paymentsv1.CoursePurchasePaymentCaptured{
		PurchaseId:  "purchase-1",
		LearnerGcid: "learner-1",
		CourseId:    "course-1",
	}
	err := sub.HandleCoursePurchasePaymentCaptured(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error for missing envelope, got nil")
	}
}

func TestPaymentsSubscriber_CoursePurchasePaymentCaptured_MissingEventID(t *testing.T) {
	t.Parallel()
	sub, _, _, _ := newPaymentsSubscriberForTest(t)
	env := newTestEnvelope("", "tenant-1", "learner-1")
	ev := &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope:    env,
		PurchaseId:  "purchase-1",
		LearnerGcid: "learner-1",
		CourseId:    "course-1",
	}
	err := sub.HandleCoursePurchasePaymentCaptured(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error for empty event_id, got nil")
	}
}

// -----------------------------------------------------------------------------
// course_purchase.refunded.v1 — happy path + idempotent
// -----------------------------------------------------------------------------

func TestPaymentsSubscriber_CoursePurchaseRefunded_RevokesEnrollment(t *testing.T) {
	t.Parallel()
	sub, tracker, _, pub := newPaymentsSubscriberForTest(t)
	// Seed the enrollment first (mimics earlier captured event).
	_, _ = tracker.Register("tenant-1", "course-1", "learner-1")

	ev := &paymentsv1.CoursePurchaseRefunded{
		Envelope:            newTestEnvelope("evt-refund-1", "tenant-1", "learner-1"),
		PurchaseId:          "purchase-1",
		LearnerGcid:         "learner-1",
		CourseId:            "course-1",
		StripeChargeId:      "ch_test_1",
		StripeRefundId:      "re_test_1",
		AmountCentsRefunded: 19900,
		Currency:            "SGD",
	}
	if err := sub.HandleCoursePurchaseRefunded(context.Background(), ev); err != nil {
		t.Fatalf("refund delivery: %v", err)
	}
	if _, ok := tracker.GetByCourseAndGCID("tenant-1", "course-1", "learner-1"); ok {
		t.Error("enrollment not revoked — still active after refund")
	}
	// Downstream enrollment.cancelled.v1 event emitted.
	found := false
	for _, e := range pub.History() {
		if e.Topic == TopicEnrollmentCancelled {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %s in publisher history", TopicEnrollmentCancelled)
	}
}

func TestPaymentsSubscriber_CoursePurchaseRefunded_IdempotentReplay(t *testing.T) {
	t.Parallel()
	sub, tracker, _, pub := newPaymentsSubscriberForTest(t)
	_, _ = tracker.Register("tenant-1", "course-1", "learner-1")
	ev := &paymentsv1.CoursePurchaseRefunded{
		Envelope:    newTestEnvelope("evt-refund-1", "tenant-1", "learner-1"),
		PurchaseId:  "purchase-1",
		LearnerGcid: "learner-1",
		CourseId:    "course-1",
	}
	if err := sub.HandleCoursePurchaseRefunded(context.Background(), ev); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	before := len(pub.History())
	if err := sub.HandleCoursePurchaseRefunded(context.Background(), ev); err != nil {
		t.Fatalf("replay delivery: %v", err)
	}
	after := len(pub.History())
	if before != after {
		t.Errorf("idempotent replay emitted duplicate downstream events; before=%d after=%d", before, after)
	}
}

// -----------------------------------------------------------------------------
// application_payment.payment_captured.v1 — advances Application to PAID + ENROLLED
// -----------------------------------------------------------------------------

func TestPaymentsSubscriber_ApplicationPaymentCaptured_AdvancesApplication(t *testing.T) {
	t.Parallel()
	sub, _, apps, pub := newPaymentsSubscriberForTest(t)

	// Seed an application in Accepted state ready for payment capture.
	app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusAccepted)

	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{
		Envelope:              newTestEnvelope("evt-app-1", "tenant-1", "learner-1"),
		PurchaseId:            "purchase-2",
		LearnerGcid:           "learner-1",
		ApplicationId:         app.ID,
		CourseId:              "course-1",
		StripePaymentIntentId: "pi_app_1",
		AmountCentsPaid:       100000,
		Currency:              "SGD",
	}
	if err := sub.HandleApplicationPaymentCaptured(context.Background(), ev); err != nil {
		t.Fatalf("delivery: %v", err)
	}
	got, ok, err := apps.Get(context.Background(), "tenant-1", app.ID)
	if err != nil || !ok {
		t.Fatalf("application gone: err=%v ok=%v", err, ok)
	}
	if got.Status != application.StatusEnrolled {
		t.Errorf("application status = %s, want enrolled", got.Status)
	}
	// Downstream events emitted (one or more application.* events).
	if len(pub.History()) == 0 {
		t.Error("no downstream events emitted from payment-captured handler")
	}
}

func TestPaymentsSubscriber_ApplicationPaymentCaptured_IdempotentReplay(t *testing.T) {
	t.Parallel()
	sub, _, apps, pub := newPaymentsSubscriberForTest(t)
	app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusAccepted)

	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{
		Envelope:      newTestEnvelope("evt-app-1", "tenant-1", "learner-1"),
		PurchaseId:    "purchase-2",
		LearnerGcid:   "learner-1",
		ApplicationId: app.ID,
		CourseId:      "course-1",
	}
	if err := sub.HandleApplicationPaymentCaptured(context.Background(), ev); err != nil {
		t.Fatalf("first: %v", err)
	}
	before := len(pub.History())
	if err := sub.HandleApplicationPaymentCaptured(context.Background(), ev); err != nil {
		t.Fatalf("replay: %v", err)
	}
	after := len(pub.History())
	if before != after {
		t.Errorf("duplicate downstream events on replay; before=%d after=%d", before, after)
	}
}

func TestPaymentsSubscriber_ApplicationPaymentCaptured_MissingEnvelope(t *testing.T) {
	t.Parallel()
	sub, _, _, _ := newPaymentsSubscriberForTest(t)
	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{
		PurchaseId:    "purchase-2",
		ApplicationId: "app-1",
	}
	err := sub.HandleApplicationPaymentCaptured(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error for missing envelope")
	}
}

// -----------------------------------------------------------------------------
// application_payment.refunded.v1 — cancels application + revokes enrollment
// -----------------------------------------------------------------------------

func TestPaymentsSubscriber_ApplicationPaymentRefunded_RevertsApplicationAndEnrollment(t *testing.T) {
	t.Parallel()
	sub, tracker, apps, pub := newPaymentsSubscriberForTest(t)
	// Application sits at Accepted (post offer-acceptance, pre-payment is
	// the typical refund-cancel window). Per the FSM: Accepted → Withdrawn
	// is a valid edge; Enrolled is terminal so refund-cancel from there
	// only revokes the enrollment and audits via the downstream event.
	app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusAccepted)
	_, _ = tracker.Register("tenant-1", "course-1", "learner-1")

	ev := &paymentsv1.ApplicationPaymentRefunded{
		Envelope:            newTestEnvelope("evt-app-refund-1", "tenant-1", "learner-1"),
		PurchaseId:          "purchase-2",
		LearnerGcid:         "learner-1",
		ApplicationId:       app.ID,
		StripeChargeId:      "ch_app_1",
		StripeRefundId:      "re_app_1",
		AmountCentsRefunded: 100000,
		Currency:            "SGD",
	}
	if err := sub.HandleApplicationPaymentRefunded(context.Background(), ev); err != nil {
		t.Fatalf("delivery: %v", err)
	}
	// Enrollment should be revoked.
	if _, ok := tracker.GetByCourseAndGCID("tenant-1", "course-1", "learner-1"); ok {
		t.Error("enrollment still active after application refund")
	}
	// Application should be withdrawn (Accepted → Withdrawn FSM edge).
	got, ok, _ := apps.Get(context.Background(), "tenant-1", app.ID)
	if !ok {
		t.Fatal("application gone")
	}
	if got.Status != application.StatusWithdrawn {
		t.Errorf("application status = %s, want withdrawn", got.Status)
	}
	if len(pub.History()) == 0 {
		t.Error("no downstream events emitted from application refund")
	}
}

func TestPaymentsSubscriber_ApplicationPaymentRefunded_IdempotentReplay(t *testing.T) {
	t.Parallel()
	sub, tracker, apps, pub := newPaymentsSubscriberForTest(t)
	app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusAccepted)
	_, _ = tracker.Register("tenant-1", "course-1", "learner-1")
	ev := &paymentsv1.ApplicationPaymentRefunded{
		Envelope:      newTestEnvelope("evt-app-refund-1", "tenant-1", "learner-1"),
		PurchaseId:    "purchase-2",
		LearnerGcid:   "learner-1",
		ApplicationId: app.ID,
	}
	if err := sub.HandleApplicationPaymentRefunded(context.Background(), ev); err != nil {
		t.Fatalf("first: %v", err)
	}
	before := len(pub.History())
	if err := sub.HandleApplicationPaymentRefunded(context.Background(), ev); err != nil {
		t.Fatalf("replay: %v", err)
	}
	after := len(pub.History())
	if before != after {
		t.Errorf("replay emitted duplicate downstream events; before=%d after=%d", before, after)
	}
}

// -----------------------------------------------------------------------------
// Subscription naming convention — chora-delivery-payments-{aggregate}-{event_type}
// -----------------------------------------------------------------------------

func TestPaymentsSubscriber_SubscriptionNames(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		TopicCoursePurchasePaymentCaptured:     "chora-delivery-payments-course_purchase-payment_captured",
		TopicCoursePurchaseRefunded:            "chora-delivery-payments-course_purchase-refunded",
		TopicApplicationPaymentPaymentCaptured: "chora-delivery-payments-application_payment-payment_captured",
		TopicApplicationPaymentRefunded:        "chora-delivery-payments-application_payment-refunded",
	}
	for topic, expected := range want {
		got := PaymentsSubscriptionName(topic)
		if got != expected {
			t.Errorf("PaymentsSubscriptionName(%q) = %q, want %q", topic, got, expected)
		}
	}
}

// -----------------------------------------------------------------------------
// mustSeedApplication: helper to seed an Application at a target Status.
// -----------------------------------------------------------------------------

func mustSeedApplication(t *testing.T, apps *repoinmem.ApplicationRepo, tenantID, courseID, gcid string, target application.Status) *application.Application {
	t.Helper()
	app, _, err := apps.SubmitOrGet(context.Background(), application.SubmitInput{
		TenantID: tenantID,
		CourseID: courseID,
		GCID:     gcid,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Walk the FSM up to target. Note: terminal state Enrolled requires
	// passing through every intermediate state.
	walk := []application.Status{
		application.StatusUnderReview,
		application.StatusOfferMade,
		application.StatusAccepted,
		application.StatusPaid,
		application.StatusEnrolled,
	}
	for _, next := range walk {
		if app.Status == target {
			break
		}
		if err := app.Transition(next); err != nil {
			t.Fatalf("seed transition to %s: %v", next, err)
		}
	}
	if app.Status != target {
		t.Fatalf("seed reached %s, want %s", app.Status, target)
	}
	if err := apps.Save(context.Background(), app); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	_ = time.Now // silence unused if test file shrinks
	return app
}

// -----------------------------------------------------------------------------
// Refund un-enrol PERSISTENCE — the tracker must drive EnrollmentPort.Cancel,
// not merely SoftDelete() the loaded aggregate (a silent no-op on Postgres,
// where GetByCourseAndGCID materialises a throw-away struct that is never
// written back). Proven with a spy port that returns a COPY on read — so an
// in-struct SoftDelete cannot persist; only a Cancel() call mutates the
// backing row. Mirrors the WS-B cancelSpyStore pattern in v1_handlers_test.go.
// -----------------------------------------------------------------------------

// persistSpyStore is an EnrollmentPort that mimics the pg adapter's
// materialise-fresh semantics: reads return a COPY of the stored row. The old
// Revoke path (enr.SoftDelete() on that copy) therefore leaves the backing row
// active, so this spy distinguishes the silent no-op from the Cancel-persist
// fix — which the in-memory store could not (its reads share the live pointer).
type persistSpyStore struct {
	byKey        map[string]*domain.Enrollment // tenant|course|gcid
	byID         map[string]*domain.Enrollment
	cancelCalls  int
	lastTenant   string
	lastEnrollID string
}

func newPersistSpyStore() *persistSpyStore {
	return &persistSpyStore{
		byKey: map[string]*domain.Enrollment{},
		byID:  map[string]*domain.Enrollment{},
	}
}

func (s *persistSpyStore) seed(tenantID, courseID, gcid string) *domain.Enrollment {
	e, err := domain.NewEnrollment(tenantID, courseID, gcid)
	if err != nil {
		panic(err)
	}
	s.byKey[tenantID+"|"+courseID+"|"+gcid] = e
	s.byID[e.ID] = e
	return e
}

func (s *persistSpyStore) Register(_ context.Context, tenantID, courseID, gcid string) (*domain.Enrollment, error) {
	k := tenantID + "|" + courseID + "|" + gcid
	if e, ok := s.byKey[k]; ok && e.DeletedAt == nil {
		cp := *e
		return &cp, nil
	}
	e := s.seed(tenantID, courseID, gcid)
	cp := *e
	return &cp, nil
}

func (s *persistSpyStore) Get(_ context.Context, id string) (*domain.Enrollment, bool, error) {
	e, ok := s.byID[id]
	if !ok || e.DeletedAt != nil {
		return nil, false, nil
	}
	cp := *e
	return &cp, true, nil
}

func (s *persistSpyStore) GetByCourseAndGCID(_ context.Context, tenantID, courseID, gcid string) (*domain.Enrollment, bool, error) {
	e, ok := s.byKey[tenantID+"|"+courseID+"|"+gcid]
	if !ok || e.DeletedAt != nil {
		return nil, false, nil
	}
	cp := *e // COPY — an in-struct SoftDelete by the caller must NOT persist.
	return &cp, true, nil
}

func (s *persistSpyStore) ListByGCID(_ context.Context, _, _ string) ([]*domain.Enrollment, error) {
	return nil, nil
}

func (s *persistSpyStore) CountByCourse(_ context.Context, _, _ string) (int, error) {
	return 0, nil
}

func (s *persistSpyStore) Cancel(_ context.Context, tenantID, enrollmentID string) error {
	s.cancelCalls++
	s.lastTenant = tenantID
	s.lastEnrollID = enrollmentID
	if e, ok := s.byID[enrollmentID]; ok && e.TenantID == tenantID {
		e.SoftDelete() // mutate the BACKING row — this is the persist.
	}
	return nil
}

var _ domain.EnrollmentPort = (*persistSpyStore)(nil)

func TestPaymentsSubscriber_CoursePurchaseRefunded_PersistsCancelOnStore(t *testing.T) {
	t.Parallel()
	spy := newPersistSpyStore()
	seeded := spy.seed("tenant-1", "course-1", "learner-1")
	pub := NewInMemoryPublisher("chora-489812", "chora-delivery")
	sub := NewPaymentsSubscriber(PaymentsSubscriberDeps{
		Enrollments:  NewEnrollmentTracker(spy),
		Applications: repoinmem.NewApplicationRepo(),
		Publisher:    pub,
		Inbox:        idempotent.NewMemoryStore(),
	})

	ev := &paymentsv1.CoursePurchaseRefunded{
		Envelope:       newTestEnvelope("evt-refund-persist-1", "tenant-1", "learner-1"),
		PurchaseId:     "purchase-1",
		LearnerGcid:    "learner-1",
		CourseId:       "course-1",
		StripeRefundId: "re_persist_1",
	}
	if err := sub.HandleCoursePurchaseRefunded(context.Background(), ev); err != nil {
		t.Fatalf("refund delivery: %v", err)
	}

	// The fix: the refund handler MUST drive the port's Cancel persist path.
	if spy.cancelCalls != 1 {
		t.Fatalf("expected port.Cancel called exactly once (persist); got %d — refund un-enrol is a silent no-op on Postgres", spy.cancelCalls)
	}
	if spy.lastEnrollID != seeded.ID {
		t.Fatalf("Cancel called with wrong enrollment_id: want %q got %q", seeded.ID, spy.lastEnrollID)
	}
	if spy.lastTenant != "tenant-1" {
		t.Fatalf("Cancel called with wrong tenant_id: want tenant-1 got %q", spy.lastTenant)
	}
	// Persistence proof: the enrolment is gone from the BACKING store, not just
	// mutated in a throw-away struct.
	if _, ok, _ := spy.GetByCourseAndGCID(context.Background(), "tenant-1", "course-1", "learner-1"); ok {
		t.Error("enrolment still active in backing store after refund — cancel did not persist")
	}
}
