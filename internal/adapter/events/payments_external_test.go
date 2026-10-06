// payments_external_test.go — external-package payments subscriber test for
// the emitCustomEvent capability-assertion branch (an inner Publisher WITHOUT
// PublishCustom must silently skip the audit emit while the FSM emit still
// records through the delegating publisher).
package events_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-common/idempotent"
	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/payments/v1"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func TestPaymentsSubscriber_ApplicationRefunded_NonCustomPublisherSkipsAudit(t *testing.T) {
	// Seed an Accepted application through the same FSM walk the in-package
	// helper uses (external tests cannot share that helper).
	app, err := application.NewApplication(application.NewApplicationInput{
		TenantID: "tenant-1", CourseID: "course-1", GCID: "learner-1",
	})
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	for _, s := range []application.Status{
		application.StatusSubmitted,
		application.StatusUnderReview,
		application.StatusOfferMade,
		application.StatusAccepted,
	} {
		if err := app.Transition(s); err != nil {
			t.Fatalf("walk to %s: %v", s, err)
		}
	}
	apps := repoinmem.NewApplicationRepo()
	if err := apps.Save(context.Background(), app); err != nil {
		t.Fatalf("save: %v", err)
	}

	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	sub := events.NewPaymentsSubscriber(events.PaymentsSubscriberDeps{
		Enrollments:  events.NewEnrollmentTracker(domain.NewInMemEnrollmentStoreFrom(domain.NewEnrollmentRegistry())),
		Applications: apps,
		Publisher:    &noCustomPublisher{inner: inner},
		Inbox:        idempotent.NewMemoryStore(),
	})

	ev := &paymentsv1.ApplicationPaymentRefunded{
		Envelope:       &commonv1.EventEnvelope{EventId: "evt-external", TenantId: "tenant-1"},
		ApplicationId:  app.ID,
		LearnerGcid:    "learner-1",
		StripeRefundId: "re_external_1",
	}
	if err := sub.HandleApplicationPaymentRefunded(context.Background(), ev); err != nil {
		t.Fatalf("refund: %v", err)
	}

	// The FSM emit (PublishApplicationStateChanged via the delegating stub) is
	// recorded; the custom audit emit MUST be skipped for a non-PublishCustom
	// publisher.
	hist := inner.History()
	if len(hist) != 1 {
		t.Fatalf("expected exactly 1 FSM event (audit skipped), got %d: %v", len(hist), hist)
	}
	if hist[0].Topic != "chora.delivery.application.withdrawn.v1" {
		t.Fatalf("expected withdrawn FSM event, got %q", hist[0].Topic)
	}
}

// noCustomPublisher implements events.Publisher WITHOUT the optional
// PublishCustom capability (the assertion in CourseContentPublisher /
// ExamResultPublisher depends on its absence). Plain struct — an embedded
// *events.InMemoryPublisher would promote PublishCustom.
type noCustomPublisher struct {
	inner *events.InMemoryPublisher
}

func (n *noCustomPublisher) PublishCourseCreated(in events.CourseCreated) (events.PublishedEvent, error) {
	return n.inner.PublishCourseCreated(in)
}
func (n *noCustomPublisher) PublishCoursePublished(in events.CoursePublished) (events.PublishedEvent, error) {
	return n.inner.PublishCoursePublished(in)
}
func (n *noCustomPublisher) PublishEnrollmentCreated(in events.EnrollmentCreated) (events.PublishedEvent, error) {
	return n.inner.PublishEnrollmentCreated(in)
}
func (n *noCustomPublisher) PublishEnrollmentCancelled(in events.EnrollmentCancelled) (events.PublishedEvent, error) {
	return n.inner.PublishEnrollmentCancelled(in)
}
func (n *noCustomPublisher) PublishEnrollmentCompleted(in events.EnrollmentCompleted) (events.PublishedEvent, error) {
	return n.inner.PublishEnrollmentCompleted(in)
}
func (n *noCustomPublisher) PublishBookingConfirmed(in events.BookingConfirmed) (events.PublishedEvent, error) {
	return n.inner.PublishBookingConfirmed(in)
}
func (n *noCustomPublisher) PublishCertificationIssued(in events.CertificationIssued) (events.PublishedEvent, error) {
	return n.inner.PublishCertificationIssued(in)
}
func (n *noCustomPublisher) PublishApplicationStateChanged(app *application.Application, traceparent string) (events.PublishedEvent, error) {
	return n.inner.PublishApplicationStateChanged(app, traceparent)
}
func (n *noCustomPublisher) PublishTestSetCreated(in events.TestSetCreated) (events.PublishedEvent, error) {
	return n.inner.PublishTestSetCreated(in)
}
func (n *noCustomPublisher) PublishTestSetQuestionAdded(in events.TestSetQuestionAdded) (events.PublishedEvent, error) {
	return n.inner.PublishTestSetQuestionAdded(in)
}
func (n *noCustomPublisher) PublishTestSetQuestionUpdated(in events.TestSetQuestionUpdated) (events.PublishedEvent, error) {
	return n.inner.PublishTestSetQuestionUpdated(in)
}
func (n *noCustomPublisher) PublishTestSetQuestionRemoved(in events.TestSetQuestionRemoved) (events.PublishedEvent, error) {
	return n.inner.PublishTestSetQuestionRemoved(in)
}
func (n *noCustomPublisher) PublishTestSetPublished(in events.TestSetPublished) (events.PublishedEvent, error) {
	return n.inner.PublishTestSetPublished(in)
}
func (n *noCustomPublisher) PublishLiveQuizScoreAwarded(in events.LiveQuizScoreAwarded) (events.PublishedEvent, error) {
	return n.inner.PublishLiveQuizScoreAwarded(in)
}
func (n *noCustomPublisher) PublishLiveQuizSessionStarted(in events.LiveQuizSessionStarted) (events.PublishedEvent, error) {
	return n.inner.PublishLiveQuizSessionStarted(in)
}
func (n *noCustomPublisher) PublishLiveQuizSessionEnded(in events.LiveQuizSessionEnded) (events.PublishedEvent, error) {
	return n.inner.PublishLiveQuizSessionEnded(in)
}
