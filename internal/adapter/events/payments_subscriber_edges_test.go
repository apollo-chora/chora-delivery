// payments_subscriber_edges_test.go — in-package coverage for the payments
// subscriber error + edge branches the happy-path tests do not reach:
// registration/revoke failures, no-op replay-without-emit paths, terminal-FSM
// guards, and envelope validation gaps (missing tenant_id).
package events

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/idempotent"
	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/payments/v1"

	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// Failing test doubles
// -----------------------------------------------------------------------------

// failEnrollmentStore is an EnrollmentPort with optional per-call failures and
// a "seeded row exists" switch so the !created / !revoked caller branches are
// reachable without the real registry.
type failEnrollmentStore struct {
	peekErr   error
	regErr    error
	cancelErr error
	seedReg   bool
	enr       *domain.Enrollment
}

func (f *failEnrollmentStore) Register(_ context.Context, _, _, _ string) (*domain.Enrollment, error) {
	if f.regErr != nil {
		return nil, f.regErr
	}
	return f.enr, nil
}
func (f *failEnrollmentStore) Get(_ context.Context, _ string) (*domain.Enrollment, bool, error) {
	return nil, false, nil
}
func (f *failEnrollmentStore) GetByCourseAndGCID(_ context.Context, _, _, _ string) (*domain.Enrollment, bool, error) {
	if f.peekErr != nil {
		return nil, false, f.peekErr
	}
	if f.seedReg {
		return f.enr, true, nil
	}
	return nil, false, nil
}
func (f *failEnrollmentStore) ListByGCID(_ context.Context, _, _ string) ([]*domain.Enrollment, error) {
	return nil, nil
}
func (f *failEnrollmentStore) CountByCourse(_ context.Context, _, _ string) (int, error) {
	return 0, nil
}
func (f *failEnrollmentStore) Cancel(_ context.Context, _, _ string) error {
	return f.cancelErr
}

var _ domain.EnrollmentPort = (*failEnrollmentStore)(nil)

// failAppStore is an ApplicationPort with optional Get/Save failures.
type failAppStore struct {
	getErr  error
	saveErr error
	app     *application.Application
	ok      bool
}

func (f *failAppStore) SubmitOrGet(_ context.Context, _ application.SubmitInput) (*application.Application, bool, error) {
	return nil, false, nil
}
func (f *failAppStore) Get(_ context.Context, _, _ string) (*application.Application, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	return f.app, f.ok, nil
}
func (f *failAppStore) ListByGCID(_ context.Context, _, _ string, _, _ int) ([]*application.Application, int, error) {
	return nil, 0, nil
}
func (f *failAppStore) ListByTenant(_ context.Context, _ application.ListByTenantInput) ([]*application.Application, int, error) {
	return nil, 0, nil
}
func (f *failAppStore) Save(_ context.Context, _ *application.Application) error {
	return f.saveErr
}

var _ application.ApplicationPort = (*failAppStore)(nil)

// failEnrollmentCreatedPub returns an error from PublishEnrollmentCreated (all
// other publisher methods delegate to the in-memory publisher).
type failEnrollmentCreatedPub struct {
	*InMemoryPublisher
}

func (f *failEnrollmentCreatedPub) PublishEnrollmentCreated(EnrollmentCreated) (PublishedEvent, error) {
	return PublishedEvent{}, errors.New("emit boom")
}

// failEnrollmentCancelledPub returns an error from PublishEnrollmentCancelled.
type failEnrollmentCancelledPub struct {
	*InMemoryPublisher
}

func (f *failEnrollmentCancelledPub) PublishEnrollmentCancelled(EnrollmentCancelled) (PublishedEvent, error) {
	return PublishedEvent{}, errors.New("emit boom")
}

func newEdgeSubscriber(tracker *EnrollmentTracker, apps application.ApplicationPort, pub Publisher) *PaymentsSubscriber {
	return NewPaymentsSubscriber(PaymentsSubscriberDeps{
		Enrollments:  tracker,
		Applications: apps,
		Publisher:    pub,
		Inbox:        idempotent.NewMemoryStore(),
	})
}

// -----------------------------------------------------------------------------
// EnrollmentTracker — Register / Revoke edge branches
// -----------------------------------------------------------------------------

func TestEnrollmentTracker_Register_NilGuardAndPeekFailure(t *testing.T) {
	if _, _, err := (*EnrollmentTracker)(nil).Register(context.Background(), "t", "c", "g"); err == nil {
		t.Fatal("nil tracker must error")
	}
	if _, _, err := NewEnrollmentTracker(nil).Register(context.Background(), "t", "c", "g"); err == nil {
		t.Fatal("nil store must error")
	}
	store := &failEnrollmentStore{peekErr: errors.New("peek boom")}
	if _, _, err := NewEnrollmentTracker(store).Register(context.Background(), "t", "c", "g"); err == nil ||
		!strings.Contains(err.Error(), "peek") {
		t.Fatalf("want peek error, got %v", err)
	}
}

func TestEnrollmentTracker_Register_StoreFailure(t *testing.T) {
	store := &failEnrollmentStore{regErr: errors.New("insert boom")}
	if _, _, err := NewEnrollmentTracker(store).Register(context.Background(), "t", "c", "g"); err == nil {
		t.Fatal("want register error")
	}
}

func TestEnrollmentTracker_Revoke_EdgeBranches(t *testing.T) {
	if _, _, err := (*EnrollmentTracker)(nil).Revoke(context.Background(), "t", "c", "g"); err == nil {
		t.Fatal("nil tracker must error")
	}
	if _, _, err := NewEnrollmentTracker(nil).Revoke(context.Background(), "t", "c", "g"); err == nil {
		t.Fatal("nil store must error")
	}
	// No row -> (nil, false, nil) no-op.
	enr, revoked, err := NewEnrollmentTracker(&failEnrollmentStore{}).Revoke(context.Background(), "t", "c", "g")
	if err != nil || revoked || enr != nil {
		t.Fatalf("no-op revoke = (%v, %v, %v), want (nil, false, nil)", enr, revoked, err)
	}
	// Peek failure -> wrapped peek error.
	if _, _, err := NewEnrollmentTracker(&failEnrollmentStore{peekErr: errors.New("peek boom")}).Revoke(context.Background(), "t", "c", "g"); err == nil ||
		!strings.Contains(err.Error(), "peek") {
		t.Fatalf("want peek error, got %v", err)
	}
	// Cancel failure -> wrapped cancel error.
	seeded, err := domain.NewEnrollment("t", "c", "g")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := NewEnrollmentTracker(&failEnrollmentStore{seedReg: true, enr: seeded, cancelErr: errors.New("cancel boom")}).Revoke(context.Background(), "t", "c", "g"); err == nil ||
		!strings.Contains(err.Error(), "cancel") {
		t.Fatalf("want cancel error, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// course_purchase.payment_captured — nil/missing-field + failure branches
// -----------------------------------------------------------------------------

func TestPaymentsSubscriber_CoursePurchasePaymentCaptured_NilReceiverAndEvent(t *testing.T) {
	sub, _, _, _ := newPaymentsSubscriberForTest(t)
	if err := sub.HandleCoursePurchasePaymentCaptured(context.Background(), nil); err == nil {
		t.Fatal("nil event must error")
	}
	if err := (*PaymentsSubscriber)(nil).HandleCoursePurchasePaymentCaptured(context.Background(), &paymentsv1.CoursePurchasePaymentCaptured{}); err == nil {
		t.Fatal("nil receiver must error")
	}
}

func TestPaymentsSubscriber_CoursePurchasePaymentCaptured_MissingFields(t *testing.T) {
	sub, _, _, _ := newPaymentsSubscriberForTest(t)
	ctx := context.Background()

	// Missing tenant_id — the validateEnvelope branch no other test reaches.
	envNoTenant := newTestEnvelope("evt-x", "", "learner-1")
	if err := sub.HandleCoursePurchasePaymentCaptured(ctx, &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope: envNoTenant, LearnerGcid: "l", CourseId: "c",
	}); err == nil || !strings.Contains(err.Error(), "tenant_id") {
		t.Fatalf("want envelope tenant_id error, got %v", err)
	}

	env := newTestEnvelope("evt-1", "tenant-1", "learner-1")
	if err := sub.HandleCoursePurchasePaymentCaptured(ctx, &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope: env, LearnerGcid: "l",
	}); err == nil || !strings.Contains(err.Error(), "course_id") {
		t.Fatalf("want course_id error, got %v", err)
	}
	if err := sub.HandleCoursePurchasePaymentCaptured(ctx, &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope: env, CourseId: "c",
	}); err == nil || !strings.Contains(err.Error(), "learner_gcid") {
		t.Fatalf("want learner_gcid error, got %v", err)
	}
}

func TestPaymentsSubscriber_CoursePurchasePaymentCaptured_RegisterError(t *testing.T) {
	sub := newEdgeSubscriber(NewEnrollmentTracker(&failEnrollmentStore{regErr: errors.New("insert boom")}), repoinmem.NewApplicationRepo(), NewInMemoryPublisher("p", "s"))
	ev := &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope:    newTestEnvelope("evt-1", "tenant-1", "learner-1"),
		LearnerGcid: "learner-1",
		CourseId:    "course-1",
	}
	err := sub.HandleCoursePurchasePaymentCaptured(context.Background(), ev)
	if err == nil || !strings.Contains(err.Error(), "enrollment register") {
		t.Fatalf("want register error, got %v", err)
	}
}

func TestPaymentsSubscriber_CoursePurchasePaymentCaptured_ExistingRowSkipsEmit(t *testing.T) {
	seeded, _ := domain.NewEnrollment("t", "c", "g")
	pub := NewInMemoryPublisher("p", "s")
	sub := newEdgeSubscriber(NewEnrollmentTracker(&failEnrollmentStore{seedReg: true, enr: seeded}), repoinmem.NewApplicationRepo(), pub)
	ev := &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope:    newTestEnvelope("evt-1", "tenant-1", "learner-1"),
		LearnerGcid: "learner-1",
		CourseId:    "course-1",
	}
	if err := sub.HandleCoursePurchasePaymentCaptured(context.Background(), ev); err != nil {
		t.Fatalf("existing-row replay must not error: %v", err)
	}
	if len(pub.History()) != 0 {
		t.Fatalf("no downstream emit expected when row already existed; got %d", len(pub.History()))
	}
}

func TestPaymentsSubscriber_CoursePurchasePaymentCaptured_NilPublisherSkipsEmit(t *testing.T) {
	sub := newEdgeSubscriber(NewEnrollmentTracker(domain.NewInMemEnrollmentStoreFrom(domain.NewEnrollmentRegistry())), repoinmem.NewApplicationRepo(), nil)
	ev := &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope:    newTestEnvelope("evt-1", "tenant-1", "learner-1"),
		LearnerGcid: "learner-1",
		CourseId:    "course-1",
	}
	if err := sub.HandleCoursePurchasePaymentCaptured(context.Background(), ev); err != nil {
		t.Fatalf("nil publisher must still register: %v", err)
	}
}

func TestPaymentsSubscriber_CoursePurchasePaymentCaptured_PublishError(t *testing.T) {
	pub := &failEnrollmentCreatedPub{InMemoryPublisher: NewInMemoryPublisher("p", "s")}
	sub := newEdgeSubscriber(NewEnrollmentTracker(domain.NewInMemEnrollmentStoreFrom(domain.NewEnrollmentRegistry())), repoinmem.NewApplicationRepo(), pub)
	ev := &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope:    newTestEnvelope("evt-1", "tenant-1", "learner-1"),
		LearnerGcid: "learner-1",
		CourseId:    "course-1",
	}
	err := sub.HandleCoursePurchasePaymentCaptured(context.Background(), ev)
	if err == nil || !strings.Contains(err.Error(), "emit enrollment.created") {
		t.Fatalf("want emit error, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// course_purchase.refunded — failure + no-op branches
// -----------------------------------------------------------------------------

func TestPaymentsSubscriber_CoursePurchaseRefunded_NilEventAndMissingFields(t *testing.T) {
	sub, _, _, _ := newPaymentsSubscriberForTest(t)
	ctx := context.Background()
	if err := sub.HandleCoursePurchaseRefunded(ctx, nil); err == nil {
		t.Fatal("nil event must error")
	}
	env := newTestEnvelope("evt-r", "tenant-1", "learner-1")
	if err := sub.HandleCoursePurchaseRefunded(ctx, &paymentsv1.CoursePurchaseRefunded{Envelope: env, LearnerGcid: "l"}); err == nil ||
		!strings.Contains(err.Error(), "course_id") {
		t.Fatalf("want course_id error, got %v", err)
	}
	if err := sub.HandleCoursePurchaseRefunded(ctx, &paymentsv1.CoursePurchaseRefunded{Envelope: env, CourseId: "c"}); err == nil ||
		!strings.Contains(err.Error(), "learner_gcid") {
		t.Fatalf("want learner_gcid error, got %v", err)
	}
}

func TestPaymentsSubscriber_CoursePurchaseRefunded_NilReceiver(t *testing.T) {
	if err := (*PaymentsSubscriber)(nil).HandleCoursePurchaseRefunded(context.Background(), &paymentsv1.CoursePurchaseRefunded{}); err == nil {
		t.Fatal("nil receiver must error")
	}
}

func TestPaymentsSubscriber_CoursePurchaseRefunded_EnvelopeRejected(t *testing.T) {
	sub, _, _, _ := newPaymentsSubscriberForTest(t)
	// Missing tenant_id -> validateEnvelope's rejection return in this handler.
	ev := &paymentsv1.CoursePurchaseRefunded{
		Envelope:    newTestEnvelope("evt-r", "", "learner-1"),
		LearnerGcid: "learner-1",
		CourseId:    "course-1",
	}
	if err := sub.HandleCoursePurchaseRefunded(context.Background(), ev); err == nil {
		t.Fatal("want envelope tenant_id error")
	}
}

func TestPaymentsSubscriber_CoursePurchaseRefunded_RevokeError(t *testing.T) {
	seeded, _ := domain.NewEnrollment("t", "c", "g")
	sub := newEdgeSubscriber(NewEnrollmentTracker(&failEnrollmentStore{seedReg: true, enr: seeded, cancelErr: errors.New("cancel boom")}), repoinmem.NewApplicationRepo(), NewInMemoryPublisher("p", "s"))
	ev := &paymentsv1.CoursePurchaseRefunded{
		Envelope:    newTestEnvelope("evt-r", "tenant-1", "learner-1"),
		LearnerGcid: "learner-1",
		CourseId:    "course-1",
	}
	err := sub.HandleCoursePurchaseRefunded(context.Background(), ev)
	if err == nil || !strings.Contains(err.Error(), "enrollment revoke") {
		t.Fatalf("want revoke error, got %v", err)
	}
}

func TestPaymentsSubscriber_CoursePurchaseRefunded_NothingToRevokeSkipsEmit(t *testing.T) {
	pub := NewInMemoryPublisher("p", "s")
	sub := newEdgeSubscriber(NewEnrollmentTracker(&failEnrollmentStore{}), repoinmem.NewApplicationRepo(), pub)
	ev := &paymentsv1.CoursePurchaseRefunded{
		Envelope:       newTestEnvelope("evt-r", "tenant-1", "learner-1"),
		LearnerGcid:    "learner-1",
		CourseId:       "course-1",
		StripeRefundId: "re_1",
	}
	if err := sub.HandleCoursePurchaseRefunded(context.Background(), ev); err != nil {
		t.Fatalf("no-op refund must not error: %v", err)
	}
	if len(pub.History()) != 0 {
		t.Fatalf("no emit expected when nothing to revoke; got %d", len(pub.History()))
	}
}

func TestPaymentsSubscriber_CoursePurchaseRefunded_NilPublisher(t *testing.T) {
	seeded, _ := domain.NewEnrollment("t", "c", "g")
	sub := newEdgeSubscriber(NewEnrollmentTracker(&failEnrollmentStore{seedReg: true, enr: seeded}), repoinmem.NewApplicationRepo(), nil)
	ev := &paymentsv1.CoursePurchaseRefunded{
		Envelope:    newTestEnvelope("evt-r", "tenant-1", "learner-1"),
		LearnerGcid: "learner-1",
		CourseId:    "course-1",
	}
	if err := sub.HandleCoursePurchaseRefunded(context.Background(), ev); err != nil {
		t.Fatalf("nil publisher: %v", err)
	}
}

func TestPaymentsSubscriber_CoursePurchaseRefunded_PublishError(t *testing.T) {
	seeded, _ := domain.NewEnrollment("t", "c", "g")
	pub := &failEnrollmentCancelledPub{InMemoryPublisher: NewInMemoryPublisher("p", "s")}
	sub := newEdgeSubscriber(NewEnrollmentTracker(&failEnrollmentStore{seedReg: true, enr: seeded}), repoinmem.NewApplicationRepo(), pub)
	ev := &paymentsv1.CoursePurchaseRefunded{
		Envelope:       newTestEnvelope("evt-r", "tenant-1", "learner-1"),
		LearnerGcid:    "learner-1",
		CourseId:       "course-1",
		StripeRefundId: "re_1",
	}
	err := sub.HandleCoursePurchaseRefunded(context.Background(), ev)
	if err == nil || !strings.Contains(err.Error(), "emit enrollment.cancelled") {
		t.Fatalf("want emit error, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// application_payment.payment_captured — failure + FSM-guard branches
// -----------------------------------------------------------------------------

func TestPaymentsSubscriber_ApplicationPaymentCaptured_NilEventAndMissingAppID(t *testing.T) {
	sub, _, _, _ := newPaymentsSubscriberForTest(t)
	ctx := context.Background()
	if err := sub.HandleApplicationPaymentCaptured(ctx, nil); err == nil {
		t.Fatal("nil event must error")
	}
	env := newTestEnvelope("evt-a", "tenant-1", "learner-1")
	if err := sub.HandleApplicationPaymentCaptured(ctx, &paymentsv1.ApplicationPaymentPaymentCaptured{Envelope: env}); err == nil ||
		!strings.Contains(err.Error(), "application_id") {
		t.Fatalf("want application_id error, got %v", err)
	}
}

func TestPaymentsSubscriber_ApplicationPaymentCaptured_GetError(t *testing.T) {
	sub := newEdgeSubscriber(nil, &failAppStore{getErr: errors.New("db down")}, NewInMemoryPublisher("p", "s"))
	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{
		Envelope:      newTestEnvelope("evt-a", "tenant-1", "learner-1"),
		ApplicationId: "app-1",
	}
	err := sub.HandleApplicationPaymentCaptured(context.Background(), ev)
	if err == nil || !strings.Contains(err.Error(), "application get") {
		t.Fatalf("want application get error, got %v", err)
	}
}

func TestPaymentsSubscriber_ApplicationPaymentCaptured_NotFound(t *testing.T) {
	sub := newEdgeSubscriber(nil, &failAppStore{ok: false}, NewInMemoryPublisher("p", "s"))
	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{
		Envelope:      newTestEnvelope("evt-a", "tenant-1", "learner-1"),
		ApplicationId: "app-1",
	}
	err := sub.HandleApplicationPaymentCaptured(context.Background(), ev)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestPaymentsSubscriber_ApplicationPaymentCaptured_TerminalStateNoOp(t *testing.T) {
	t.Run("enrolled", func(t *testing.T) {
		sub, _, apps, pub := newPaymentsSubscriberForTest(t)
		app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusEnrolled)
		handleTerminalStateNoOp(t, sub, apps, pub, app.ID)
	})
	t.Run("paid", func(t *testing.T) {
		sub, _, apps, pub := newPaymentsSubscriberForTest(t)
		app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusPaid)
		handleTerminalStateNoOp(t, sub, apps, pub, app.ID)
	})
	t.Run("rejected", func(t *testing.T) {
		sub, _, apps, pub := newPaymentsSubscriberForTest(t)
		app := seedApplicationManually(t, apps, application.StatusRejected)
		handleTerminalStateNoOp(t, sub, apps, pub, app.ID)
	})
	t.Run("withdrawn", func(t *testing.T) {
		sub, _, apps, pub := newPaymentsSubscriberForTest(t)
		app := seedApplicationManually(t, apps, application.StatusWithdrawn)
		handleTerminalStateNoOp(t, sub, apps, pub, app.ID)
	})
}

// handleTerminalStateNoOp drives a payment-captured delivery against an app
// already in a terminal-ish state: the FSM guard must return nil without any
// downstream emit.
func handleTerminalStateNoOp(t *testing.T, sub *PaymentsSubscriber, apps application.ApplicationPort, pub *InMemoryPublisher, appID string) {
	t.Helper()
	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{
		Envelope:      newTestEnvelope("evt-a-"+appID, "tenant-1", "learner-1"),
		ApplicationId: appID,
	}
	if err := sub.HandleApplicationPaymentCaptured(context.Background(), ev); err != nil {
		t.Fatalf("terminal-state guard must no-op: %v", err)
	}
	if len(pub.History()) != 0 {
		t.Fatalf("no emits expected from terminal-state guard; got %d", len(pub.History()))
	}
}

// seedApplicationManually builds an application at a terminal-ish status that
// the fixed FSM walk in mustSeedApplication cannot reach (Rejected / Withdrawn
// require side transitions) and persists it via the repo.
func seedApplicationManually(t *testing.T, apps application.ApplicationPort, target application.Status) *application.Application {
	t.Helper()
	app, err := application.NewApplication(application.NewApplicationInput{
		TenantID: "tenant-1", CourseID: "course-1", GCID: "learner-1",
	})
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	switch target {
	case application.StatusRejected:
		for _, s := range []application.Status{application.StatusSubmitted, application.StatusUnderReview} {
			if err := app.Transition(s); err != nil {
				t.Fatalf("walk to under_review: %v", err)
			}
		}
		if err := app.Transition(application.StatusRejected); err != nil {
			t.Fatalf("reject: %v", err)
		}
	case application.StatusWithdrawn:
		if err := app.Transition(application.StatusSubmitted); err != nil {
			t.Fatalf("submit: %v", err)
		}
		if err := app.TransitionWithReason(application.StatusWithdrawn, "user-initiated"); err != nil {
			t.Fatalf("withdraw: %v", err)
		}
	default:
		t.Fatalf("unsupported manual target %s", target)
	}
	if err := apps.Save(context.Background(), app); err != nil {
		t.Fatalf("save: %v", err)
	}
	return app
}

func TestPaymentsSubscriber_ApplicationPaymentCaptured_UnexpectedState(t *testing.T) {
	sub, _, apps, _ := newPaymentsSubscriberForTest(t)
	app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusSubmitted)
	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{
		Envelope:      newTestEnvelope("evt-a", "tenant-1", "learner-1"),
		ApplicationId: app.ID,
	}
	err := sub.HandleApplicationPaymentCaptured(context.Background(), ev)
	if err == nil || !strings.Contains(err.Error(), "unexpected state") {
		t.Fatalf("want unexpected-state error, got %v", err)
	}
}

func TestPaymentsSubscriber_ApplicationPaymentCaptured_EmptyPaymentIntent(t *testing.T) {
	sub, _, apps, pub := newPaymentsSubscriberForTest(t)
	app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusAccepted)
	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{
		Envelope:      newTestEnvelope("evt-a", "tenant-1", "learner-1"),
		ApplicationId: app.ID,
		CourseId:      "course-1",
	}
	if err := sub.HandleApplicationPaymentCaptured(context.Background(), ev); err != nil {
		t.Fatalf("empty payment-intent path: %v", err)
	}
	got, _, _ := apps.Get(context.Background(), "tenant-1", app.ID)
	if got.Status != application.StatusEnrolled {
		t.Fatalf("status = %s, want enrolled", got.Status)
	}
	if len(pub.History()) == 0 {
		t.Fatal("expected downstream events")
	}
}

func TestPaymentsSubscriber_ApplicationPaymentCaptured_NilPublisherRunsFSM(t *testing.T) {
	sub, _, apps, _ := newPaymentsSubscriberForTest(t)
	app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusAccepted)
	sub.pub = nil // nil out the publisher -> emit guards skip
	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{
		Envelope:      newTestEnvelope("evt-a", "tenant-1", "learner-1"),
		ApplicationId: app.ID,
	}
	if err := sub.HandleApplicationPaymentCaptured(context.Background(), ev); err != nil {
		t.Fatalf("nil publisher: %v", err)
	}
	got, _, _ := apps.Get(context.Background(), "tenant-1", app.ID)
	if got.Status != application.StatusEnrolled {
		t.Fatalf("status = %s, want enrolled", got.Status)
	}
}

func TestPaymentsSubscriber_ApplicationPaymentCaptured_SaveError(t *testing.T) {
	app := mustSeedApplication(t, repoinmem.NewApplicationRepo(), "tenant-1", "course-1", "learner-1", application.StatusAccepted)
	sub := newEdgeSubscriber(nil, &failAppStore{app: app, ok: true, saveErr: errors.New("save boom")}, NewInMemoryPublisher("p", "s"))
	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{
		Envelope:      newTestEnvelope("evt-a", "tenant-1", "learner-1"),
		ApplicationId: app.ID,
	}
	if err := sub.HandleApplicationPaymentCaptured(context.Background(), ev); err == nil {
		t.Fatal("want save error")
	}
}

// -----------------------------------------------------------------------------
// application_payment.refunded — failure + Paid/Enrolled branch
// -----------------------------------------------------------------------------

func TestPaymentsSubscriber_ApplicationPaymentRefunded_NilEventAndMissingAppID(t *testing.T) {
	sub, _, _, _ := newPaymentsSubscriberForTest(t)
	ctx := context.Background()
	if err := sub.HandleApplicationPaymentRefunded(ctx, nil); err == nil {
		t.Fatal("nil event must error")
	}
	env := newTestEnvelope("evt-ar", "tenant-1", "learner-1")
	if err := sub.HandleApplicationPaymentRefunded(ctx, &paymentsv1.ApplicationPaymentRefunded{Envelope: env}); err == nil ||
		!strings.Contains(err.Error(), "application_id") {
		t.Fatalf("want application_id error, got %v", err)
	}
}

func TestPaymentsSubscriber_ApplicationPaymentRefunded_NilReceiverAndEnvelope(t *testing.T) {
	if err := (*PaymentsSubscriber)(nil).HandleApplicationPaymentRefunded(context.Background(), &paymentsv1.ApplicationPaymentRefunded{}); err == nil {
		t.Fatal("nil receiver must error")
	}
	sub, _, _, _ := newPaymentsSubscriberForTest(t)
	ev := &paymentsv1.ApplicationPaymentRefunded{
		Envelope:      newTestEnvelope("evt-ar", "", "learner-1"),
		ApplicationId: "app-1",
	}
	if err := sub.HandleApplicationPaymentRefunded(context.Background(), ev); err == nil {
		t.Fatal("want envelope tenant_id error")
	}
}

func TestPaymentsSubscriber_ApplicationPaymentCaptured_NilReceiver(t *testing.T) {
	if err := (*PaymentsSubscriber)(nil).HandleApplicationPaymentCaptured(context.Background(), &paymentsv1.ApplicationPaymentPaymentCaptured{}); err == nil {
		t.Fatal("nil receiver must error")
	}
}

func TestPaymentsSubscriber_ApplicationPaymentRefunded_GetError(t *testing.T) {
	sub := newEdgeSubscriber(nil, &failAppStore{getErr: errors.New("db down")}, NewInMemoryPublisher("p", "s"))
	ev := &paymentsv1.ApplicationPaymentRefunded{
		Envelope:      newTestEnvelope("evt-ar", "tenant-1", "learner-1"),
		ApplicationId: "app-1",
	}
	if err := sub.HandleApplicationPaymentRefunded(context.Background(), ev); err == nil ||
		!strings.Contains(err.Error(), "application get") {
		t.Fatalf("want application get error, got %v", err)
	}
}

func TestPaymentsSubscriber_ApplicationPaymentRefunded_NotFound(t *testing.T) {
	sub := newEdgeSubscriber(nil, &failAppStore{ok: false}, NewInMemoryPublisher("p", "s"))
	ev := &paymentsv1.ApplicationPaymentRefunded{
		Envelope:      newTestEnvelope("evt-ar", "tenant-1", "learner-1"),
		ApplicationId: "app-1",
	}
	if err := sub.HandleApplicationPaymentRefunded(context.Background(), ev); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestPaymentsSubscriber_ApplicationPaymentRefunded_TerminalPaidSkipsTransition(t *testing.T) {
	sub, tracker, apps, pub := newPaymentsSubscriberForTest(t)
	app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusPaid)
	_, _ = tracker.Register("tenant-1", "course-1", "learner-1")

	ev := &paymentsv1.ApplicationPaymentRefunded{
		Envelope:       newTestEnvelope("evt-ar", "tenant-1", "learner-1"),
		ApplicationId:  app.ID,
		LearnerGcid:    "learner-1",
		StripeRefundId: "re_1",
	}
	if err := sub.HandleApplicationPaymentRefunded(context.Background(), ev); err != nil {
		t.Fatalf("paid-state refund: %v", err)
	}
	got, _, _ := apps.Get(context.Background(), "tenant-1", app.ID)
	if got.Status != application.StatusPaid {
		t.Fatalf("status must stay %s (FSM forbids Withdrawn from Paid); got %s", application.StatusPaid, got.Status)
	}
	// Enrollment revoked + audit event still emitted.
	if _, ok := tracker.GetByCourseAndGCID("tenant-1", "course-1", "learner-1"); ok {
		t.Error("enrollment must be revoked on refund")
	}
	found := false
	for _, e := range pub.History() {
		if e.Topic == TopicApplicationRefunded {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %s audit event in history", TopicApplicationRefunded)
	}
}

func TestPaymentsSubscriber_ApplicationPaymentRefunded_SubmittedWithdraws(t *testing.T) {
	sub, _, apps, pub := newPaymentsSubscriberForTest(t)
	app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusSubmitted)

	ev := &paymentsv1.ApplicationPaymentRefunded{
		Envelope:       newTestEnvelope("evt-ar", "tenant-1", "learner-1"),
		ApplicationId:  app.ID,
		LearnerGcid:    "learner-1",
		StripeRefundId: "re_1",
	}
	if err := sub.HandleApplicationPaymentRefunded(context.Background(), ev); err != nil {
		t.Fatalf("submitted-state refund: %v", err)
	}
	got, _, _ := apps.Get(context.Background(), "tenant-1", app.ID)
	if got.Status != application.StatusWithdrawn {
		t.Fatalf("status = %s, want withdrawn", got.Status)
	}
	if len(pub.History()) == 0 {
		t.Fatal("expected audit event")
	}
}

func TestPaymentsSubscriber_ApplicationPaymentRefunded_SaveError(t *testing.T) {
	app := mustSeedApplication(t, repoinmem.NewApplicationRepo(), "tenant-1", "course-1", "learner-1", application.StatusSubmitted)
	sub := newEdgeSubscriber(nil, &failAppStore{app: app, ok: true, saveErr: errors.New("save boom")}, NewInMemoryPublisher("p", "s"))
	ev := &paymentsv1.ApplicationPaymentRefunded{
		Envelope:      newTestEnvelope("evt-ar", "tenant-1", "learner-1"),
		ApplicationId: app.ID,
	}
	if err := sub.HandleApplicationPaymentRefunded(context.Background(), ev); err == nil ||
		!strings.Contains(err.Error(), "save withdrawn") {
		t.Fatalf("want save-withdrawn error, got %v", err)
	}
}

func TestPaymentsSubscriber_ApplicationPaymentRefunded_NilPublisher(t *testing.T) {
	sub, _, apps, _ := newPaymentsSubscriberForTest(t)
	app := mustSeedApplication(t, apps, "tenant-1", "course-1", "learner-1", application.StatusSubmitted)
	sub.pub = nil
	ev := &paymentsv1.ApplicationPaymentRefunded{
		Envelope:      newTestEnvelope("evt-ar", "tenant-1", "learner-1"),
		ApplicationId: app.ID,
	}
	if err := sub.HandleApplicationPaymentRefunded(context.Background(), ev); err != nil {
		t.Fatalf("nil publisher: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Constructor defaults + subscription naming edge cases
// -----------------------------------------------------------------------------

func TestNewPaymentsSubscriber_Defaults(t *testing.T) {
	sub := NewPaymentsSubscriber(PaymentsSubscriberDeps{})
	if sub == nil {
		t.Fatal("nil subscriber")
	}
	if sub.inbox == nil {
		t.Fatal("nil inbox must default to MemoryStore")
	}
	if sub.ttl != PaymentsInboxTTL {
		t.Fatalf("ttl = %v, want default %v", sub.ttl, PaymentsInboxTTL)
	}
}

func TestPaymentsSubscriptionName_EdgeTopics(t *testing.T) {
	if got := PaymentsSubscriptionName("chora.analytics.something.v1"); got != "" {
		t.Fatalf("non-payments prefix must return empty; got %q", got)
	}
	if got := PaymentsSubscriptionName("chora.payments.a.b"); got != "" {
		t.Fatalf("too-few parts must return empty; got %q", got)
	}
	if got := PaymentsSubscriptionName("chora.payments.foo.bar.baz.v1"); got != "chora-delivery-payments-foo-bar_baz" {
		t.Fatalf("multi-part event_type join wrong: %q", got)
	}
}
