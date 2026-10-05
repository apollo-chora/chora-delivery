// Package subscribers — Pub/Sub event subscriber handlers, S6.1.
//
// chora-delivery subscribes to:
//   - chora.tenancy.payment.captured.v1 (from A-Billing webhook)
//     → if metadata.application_id present, transition Application → Paid → Enrolled
//   - chora.identity.kyc.verified.v1 (from A-Singpass)
//     → unblock Application UnderReview → OfferMade for KYC-required courses
//
// Both handlers MUST be idempotent (re-firing the same event yields the same
// result; no duplicate state transitions).
package subscribers_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	courseA = "01970000-0000-7000-8000-000000000099"
	gcidA   = "01970000-0000-7000-9000-000000000001"
)

// -----------------------------------------------------------------------------
// HandlePaymentCaptured — chora.tenancy.payment.captured.v1
// -----------------------------------------------------------------------------

func TestHandlePaymentCaptured_TransitionsAcceptedToPaidEnrolled(t *testing.T) {
	t.Parallel()
	repo := repoinmem.NewApplicationRepo()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	h := subscribers.NewHandler(repo, pub, nil)

	app, _, _ := repo.SubmitOrGet(context.Background(), application.SubmitInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	_ = app.Transition(application.StatusUnderReview)
	_ = app.Transition(application.StatusOfferMade)
	_ = app.Transition(application.StatusAccepted)
	_ = repo.Save(context.Background(), app)

	err := h.HandlePaymentCaptured(context.Background(), subscribers.PaymentCaptured{
		TenantID:      tenantA,
		ApplicationID: app.ID,
		PaymentIntent: "pi_test",
		AmountCents:   200000,
		Currency:      "SGD",
	})
	if err != nil {
		t.Fatalf("HandlePaymentCaptured: %v", err)
	}

	got, _, _ := repo.Get(context.Background(), tenantA, app.ID)
	if got.Status != application.StatusEnrolled {
		t.Fatalf("expected Enrolled (auto-walk Paid→Enrolled); got %s", got.Status)
	}
	// Both paid + enrolled events should be published.
	hist := pub.History()
	hasPaid := false
	hasEnrolled := false
	for _, h := range hist {
		switch h.Topic {
		case "chora.delivery.application.paid.v1":
			hasPaid = true
		case "chora.delivery.application.enrolled.v1":
			hasEnrolled = true
		}
	}
	if !hasPaid {
		t.Fatalf("expected paid event")
	}
	if !hasEnrolled {
		t.Fatalf("expected enrolled event")
	}
}

func TestHandlePaymentCaptured_Idempotent_NotInAccepted(t *testing.T) {
	t.Parallel()
	repo := repoinmem.NewApplicationRepo()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	h := subscribers.NewHandler(repo, pub, nil)

	// Application already at Enrolled (e.g., webhook re-delivered).
	app, _, _ := repo.SubmitOrGet(context.Background(), application.SubmitInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	for _, s := range []application.Status{
		application.StatusUnderReview, application.StatusOfferMade,
		application.StatusAccepted, application.StatusPaid, application.StatusEnrolled,
	} {
		_ = app.Transition(s)
	}
	_ = repo.Save(context.Background(), app)

	err := h.HandlePaymentCaptured(context.Background(), subscribers.PaymentCaptured{
		TenantID:      tenantA,
		ApplicationID: app.ID,
		PaymentIntent: "pi_test",
	})
	if err != nil {
		t.Fatalf("idempotent re-fire: %v", err)
	}
	got, _, _ := repo.Get(context.Background(), tenantA, app.ID)
	if got.Status != application.StatusEnrolled {
		t.Fatalf("idempotent: status changed to %s", got.Status)
	}
}

func TestHandlePaymentCaptured_UnknownApplication_NoOp(t *testing.T) {
	t.Parallel()
	repo := repoinmem.NewApplicationRepo()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	h := subscribers.NewHandler(repo, pub, nil)
	err := h.HandlePaymentCaptured(context.Background(), subscribers.PaymentCaptured{
		TenantID:      tenantA,
		ApplicationID: "does-not-exist",
	})
	if err == nil {
		t.Fatalf("expected error for unknown application")
	}
}

// -----------------------------------------------------------------------------
// HandleKYCVerified — chora.identity.kyc.verified.v1
// -----------------------------------------------------------------------------

func TestHandleKYCVerified_AdvancesUnderReviewToOfferMade(t *testing.T) {
	t.Parallel()
	repo := repoinmem.NewApplicationRepo()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	h := subscribers.NewHandler(repo, pub, nil)

	app, _, _ := repo.SubmitOrGet(context.Background(), application.SubmitInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	_ = app.Transition(application.StatusUnderReview)
	_ = repo.Save(context.Background(), app)

	err := h.HandleKYCVerified(context.Background(), subscribers.KYCVerified{
		TenantID: tenantA,
		GCID:     gcidA,
	})
	if err != nil {
		t.Fatalf("HandleKYCVerified: %v", err)
	}
	got, _, _ := repo.Get(context.Background(), tenantA, app.ID)
	if got.Status != application.StatusOfferMade {
		t.Fatalf("expected OfferMade post-KYC; got %s", got.Status)
	}
}

func TestHandleKYCVerified_NoMatchingApps_NoOp(t *testing.T) {
	t.Parallel()
	repo := repoinmem.NewApplicationRepo()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	h := subscribers.NewHandler(repo, pub, nil)
	err := h.HandleKYCVerified(context.Background(), subscribers.KYCVerified{
		TenantID: tenantA, GCID: gcidA,
	})
	if err != nil {
		t.Fatalf("no apps should be no-op: %v", err)
	}
}

func TestHandleKYCVerified_BlanksRejected(t *testing.T) {
	t.Parallel()
	h := subscribers.NewHandler(repoinmem.NewApplicationRepo(), events.NewInMemoryPublisher("p", "s"), nil)
	if err := h.HandleKYCVerified(context.Background(), subscribers.KYCVerified{}); err == nil {
		t.Fatalf("expected error for blank input")
	}
}
