// Application-event publisher tests — S6.1.
//
// Publishes per state transition: chora.delivery.application.{state}.v1
// with full envelope + IMDA evidence (D1 accountability for most states,
// D2 transparency for paid + enrolled per ADR-141).
package events_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

const (
	srcProj = "chora-489812"
	srcSvc  = "chora-delivery"
)

func TestPublishApplicationStateChanged_Submitted_FullEnvelope(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher(srcProj, srcSvc)
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: "01970000-0000-7000-8000-000000000001",
		CourseID: "01970000-0000-7000-8000-000000000099",
		GCID:     "01970000-0000-7000-9000-000000000001",
	})
	if err := app.Transition(application.StatusSubmitted); err != nil {
		t.Fatalf("transition: %v", err)
	}
	ev, err := pub.PublishApplicationStateChanged(app, "")
	if err != nil {
		t.Fatalf("PublishApplicationStateChanged: %v", err)
	}
	if ev.Topic != "chora.delivery.application.submitted.v1" {
		t.Fatalf("topic: got %q", ev.Topic)
	}
	if ev.Envelope.TenantID != app.TenantID {
		t.Fatalf("tenant_id mismatch")
	}
	if ev.Envelope.GCID != app.GCID {
		t.Fatalf("gcid mismatch")
	}
	if ev.Envelope.IdempotencyKey != "application:"+app.ID+":submitted" {
		t.Fatalf("idempotency key: got %q", ev.Envelope.IdempotencyKey)
	}
	if ev.Envelope.SourceProject != srcProj {
		t.Fatalf("source_project: got %q", ev.Envelope.SourceProject)
	}
	if ev.Envelope.SourceService != srcSvc {
		t.Fatalf("source_service: got %q", ev.Envelope.SourceService)
	}
	if ev.Envelope.SchemaVersion < 1 {
		t.Fatalf("schema_version: got %d", ev.Envelope.SchemaVersion)
	}
	if ev.Envelope.Traceparent == "" {
		t.Fatalf("traceparent must be present")
	}
	if ev.Envelope.OccurredAt.IsZero() || ev.Envelope.PublishedAt.IsZero() {
		t.Fatalf("envelope timestamps must be set")
	}
}

func TestPublishApplicationStateChanged_PaidEvent_TransparencyDimension(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher(srcProj, srcSvc)
	app := buildAppAtStatus(t, application.StatusPaid)
	ev, err := pub.PublishApplicationStateChanged(app, "")
	if err != nil {
		t.Fatalf("PublishApplicationStateChanged: %v", err)
	}
	if ev.Topic != "chora.delivery.application.paid.v1" {
		t.Fatalf("topic: got %q", ev.Topic)
	}
	dim, ok := ev.Payload["chora_imda_dimension"].(string)
	if !ok || dim != "transparency" {
		t.Fatalf("paid event must carry chora_imda_dimension=transparency; got %v", ev.Payload["chora_imda_dimension"])
	}
}

func TestPublishApplicationStateChanged_AllStatuses_HaveIMDAField(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher(srcProj, srcSvc)
	statuses := []application.Status{
		application.StatusSubmitted,
		application.StatusUnderReview,
		application.StatusOfferMade,
		application.StatusAccepted,
		application.StatusPaid,
		application.StatusEnrolled,
	}
	for _, s := range statuses {
		s := s
		t.Run(string(s), func(t *testing.T) {
			t.Parallel()
			app := buildAppAtStatus(t, s)
			ev, err := pub.PublishApplicationStateChanged(app, "")
			if err != nil {
				t.Fatalf("publish %s: %v", s, err)
			}
			if dim, ok := ev.Payload["chora_imda_dimension"]; !ok || dim == "" {
				t.Fatalf("status %s must carry chora_imda_dimension", s)
			}
		})
	}
}

func TestPublishApplicationStateChanged_Withdrawn_AllowsTransition(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher(srcProj, srcSvc)
	app := buildAppAtStatus(t, application.StatusOfferMade)
	if err := app.TransitionWithReason(application.StatusWithdrawn, "user changed mind"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	ev, err := pub.PublishApplicationStateChanged(app, "")
	if err != nil {
		t.Fatalf("publish withdrawn: %v", err)
	}
	if ev.Topic != "chora.delivery.application.withdrawn.v1" {
		t.Fatalf("topic: got %q", ev.Topic)
	}
	if reason, _ := ev.Payload["withdrawn_reason"].(string); reason != "user changed mind" {
		t.Fatalf("withdrawn_reason missing: got %v", ev.Payload["withdrawn_reason"])
	}
}

func TestPublishApplicationStateChanged_DraftRejected(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher(srcProj, srcSvc)
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: "01970000-0000-7000-8000-000000000001",
		CourseID: "01970000-0000-7000-8000-000000000099",
		GCID:     "01970000-0000-7000-9000-000000000001",
	})
	// Draft has no event topic — publishing should fail/skip.
	_, err := pub.PublishApplicationStateChanged(app, "")
	if err == nil {
		t.Fatalf("draft state must not publish (no topic)")
	}
}

func TestPublishApplicationStateChanged_BlankTenant(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher(srcProj, srcSvc)
	app := &application.Application{Status: application.StatusSubmitted}
	_, err := pub.PublishApplicationStateChanged(app, "")
	if err == nil {
		t.Fatalf("expected error for blank tenant")
	}
	if !strings.Contains(err.Error(), "tenant_id") {
		t.Fatalf("expected tenant_id error; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Helper — copy of the domain test helper (canonical 7-state walk)
// -----------------------------------------------------------------------------

func buildAppAtStatus(t *testing.T, target application.Status) *application.Application {
	t.Helper()
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: "01970000-0000-7000-8000-000000000001",
		CourseID: "01970000-0000-7000-8000-000000000099",
		GCID:     "01970000-0000-7000-9000-000000000001",
	})
	walk := map[application.Status][]application.Status{
		application.StatusSubmitted:   {application.StatusSubmitted},
		application.StatusUnderReview: {application.StatusSubmitted, application.StatusUnderReview},
		application.StatusOfferMade:   {application.StatusSubmitted, application.StatusUnderReview, application.StatusOfferMade},
		application.StatusAccepted:    {application.StatusSubmitted, application.StatusUnderReview, application.StatusOfferMade, application.StatusAccepted},
		application.StatusPaid:        {application.StatusSubmitted, application.StatusUnderReview, application.StatusOfferMade, application.StatusAccepted, application.StatusPaid},
		application.StatusEnrolled:    {application.StatusSubmitted, application.StatusUnderReview, application.StatusOfferMade, application.StatusAccepted, application.StatusPaid, application.StatusEnrolled},
	}
	for _, step := range walk[target] {
		if err := app.Transition(step); err != nil {
			t.Fatalf("walk to %s: %v", target, err)
		}
	}
	return app
}
