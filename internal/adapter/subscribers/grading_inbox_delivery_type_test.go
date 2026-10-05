// grading_inbox_delivery_type_test.go — CHO-2224 (§10.6 capstone criterion 1).
//
// The OE-batch grading path is a SECOND, independent publisher of
// chora.delivery.submission.graded.v1 (the HTTP funnel in adapter/http is the
// other). Stamping the mode at only one of them would leave essay/OE-graded
// submissions permanently mode-blind on the transcript, so this path resolves
// delivery_type too, via the same domain resolver the funnel uses.
//
// Fail-soft throughout, mirroring the assessment_title precedent directly above
// it: no offerings port, a freestanding assessment, or a dead read all omit the
// mode and STILL emit the grade. A missing mode is a nicety; a missing grade is
// a lost academic outcome.
package subscribers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-common/idempotent"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// fakeAssessReaderWithOffering returns an Assessment carrying an OfferingID, so
// the delivery_type walk (Assessment.OfferingID -> Offering.DeliveryType) has
// something to follow.
type fakeAssessReaderWithOffering struct {
	tenant, id, title, offeringID string
	err                           error
}

func (f fakeAssessReaderWithOffering) Get(_ context.Context, tenantID, id string) (*domain.Assessment, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	if tenantID == f.tenant && id == f.id {
		return &domain.Assessment{Title: f.title, OfferingID: f.offeringID}, true, nil
	}
	return nil, false, nil
}

// fakeOfferingsPort satisfies domain.OfferingPort; only Get is exercised.
type fakeOfferingsPort struct {
	offering *domain.Offering
	ok       bool
	err      error
	calls    int
}

func (f *fakeOfferingsPort) Get(_ context.Context, _ string) (*domain.Offering, bool, error) {
	f.calls++
	return f.offering, f.ok, f.err
}
func (f *fakeOfferingsPort) Save(context.Context, *domain.Offering) error { return nil }
func (f *fakeOfferingsPort) ListByTenant(context.Context, string) ([]*domain.Offering, error) {
	return nil, nil
}
func (f *fakeOfferingsPort) Search(context.Context, domain.OfferingQuery) (*domain.OfferingSearchPage, error) {
	return nil, nil
}

func TestGradingInbox_OEBatch_EmitsDeliveryType(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	reader := fakeAssessReaderWithOffering{
		tenant: gradingTenant, id: gradingAssessID, title: "Algebra Midterm", offeringID: "off-1",
	}
	offerings := &fakeOfferingsPort{
		offering: &domain.Offering{ID: "off-1", DeliveryType: domain.DeliveryTypeGraduate}, ok: true,
	}
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore()).
		WithAssessmentReader(reader).
		WithOfferings(offerings)

	env := events.EventEnvelope{EventID: "evt-dt-1", TenantID: gradingTenant, GCID: gradingLearner}
	if err := s.Handle(context.Background(), env, gradedOEBatch(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	emitted := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")
	if len(emitted) != 1 {
		t.Fatalf("submission.graded.v1: want 1 emitted, got %d", len(emitted))
	}
	if got := emitted[0].Payload["delivery_type"]; got != "graduate" {
		t.Errorf("delivery_type = %v, want %q", got, "graduate")
	}
	// The title must survive the change: both enrichments ride the same lookup.
	if got := emitted[0].Payload["assessment_title"]; got != "Algebra Midterm" {
		t.Errorf("assessment_title = %v, want %q (delivery_type must not displace it)", got, "Algebra Midterm")
	}
}

// The other half of the ">=2 modes" proof.
func TestGradingInbox_OEBatch_EmitsShortDeliveryType(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	reader := fakeAssessReaderWithOffering{
		tenant: gradingTenant, id: gradingAssessID, title: "Intro Workshop", offeringID: "off-2",
	}
	offerings := &fakeOfferingsPort{
		offering: &domain.Offering{ID: "off-2", DeliveryType: domain.DeliveryTypeShort}, ok: true,
	}
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore()).
		WithAssessmentReader(reader).
		WithOfferings(offerings)

	env := events.EventEnvelope{EventID: "evt-dt-2", TenantID: gradingTenant, GCID: gradingLearner}
	if err := s.Handle(context.Background(), env, gradedOEBatch(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	emitted := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")
	if len(emitted) != 1 {
		t.Fatalf("want 1 emitted, got %d", len(emitted))
	}
	if got := emitted[0].Payload["delivery_type"]; got != "short" {
		t.Errorf("delivery_type = %v, want %q", got, "short")
	}
}

// TestGradingInbox_OEBatch_NoOfferings_OmitsDeliveryType — an unwired port is
// the pre-deploy state. The grade must still emit, mode-less.
func TestGradingInbox_OEBatch_NoOfferings_OmitsDeliveryType(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	reader := fakeAssessReaderWithOffering{
		tenant: gradingTenant, id: gradingAssessID, title: "Algebra Midterm", offeringID: "off-1",
	}
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore()).
		WithAssessmentReader(reader)

	env := events.EventEnvelope{EventID: "evt-dt-3", TenantID: gradingTenant, GCID: gradingLearner}
	if err := s.Handle(context.Background(), env, gradedOEBatch(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	emitted := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")
	if len(emitted) != 1 {
		t.Fatalf("want 1 emitted, got %d", len(emitted))
	}
	if _, present := emitted[0].Payload["delivery_type"]; present {
		t.Errorf("delivery_type must be absent without an offerings port, got %v", emitted[0].Payload["delivery_type"])
	}
}

// TestGradingInbox_OEBatch_FreestandingAssessment_OmitsDeliveryType — offering_id
// is NULL (legal per migration 0033). No mode exists; none may be invented.
func TestGradingInbox_OEBatch_FreestandingAssessment_OmitsDeliveryType(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	reader := fakeAssessReaderWithOffering{
		tenant: gradingTenant, id: gradingAssessID, title: "Standalone Quiz", offeringID: "",
	}
	offerings := &fakeOfferingsPort{
		offering: &domain.Offering{ID: "off-1", DeliveryType: domain.DeliveryTypeGraduate}, ok: true,
	}
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore()).
		WithAssessmentReader(reader).
		WithOfferings(offerings)

	env := events.EventEnvelope{EventID: "evt-dt-4", TenantID: gradingTenant, GCID: gradingLearner}
	if err := s.Handle(context.Background(), env, gradedOEBatch(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	emitted := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")
	if len(emitted) != 1 {
		t.Fatalf("want 1 emitted, got %d", len(emitted))
	}
	if _, present := emitted[0].Payload["delivery_type"]; present {
		t.Errorf("delivery_type must be absent for a freestanding assessment, got %v", emitted[0].Payload["delivery_type"])
	}
	if offerings.calls != 0 {
		t.Errorf("offerings.Get called %d times for a blank offering_id; want 0", offerings.calls)
	}
}

// TestGradingInbox_OEBatch_OfferingLookupError_StillEmits — a dead offering read
// must never NACK a grade.
func TestGradingInbox_OEBatch_OfferingLookupError_StillEmits(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	reader := fakeAssessReaderWithOffering{
		tenant: gradingTenant, id: gradingAssessID, title: "Algebra Midterm", offeringID: "off-1",
	}
	offerings := &fakeOfferingsPort{err: errors.New("offering read failed")}
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore()).
		WithAssessmentReader(reader).
		WithOfferings(offerings)

	env := events.EventEnvelope{EventID: "evt-dt-5", TenantID: gradingTenant, GCID: gradingLearner}
	if err := s.Handle(context.Background(), env, gradedOEBatch(sub)); err != nil {
		t.Fatalf("Handle must not NACK on an offering-lookup failure: %v", err)
	}
	emitted := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")
	if len(emitted) != 1 {
		t.Fatalf("want 1 emitted, got %d", len(emitted))
	}
	if _, present := emitted[0].Payload["delivery_type"]; present {
		t.Error("delivery_type must be absent when the offering lookup errored")
	}
}
