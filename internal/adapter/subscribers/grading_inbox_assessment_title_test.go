// grading_inbox_assessment_title_test.go — WS-6 REMAINING #1 (CHO-1958): the
// OE-batch grading path must also snapshot the assessment title onto
// submission.graded.v1 so chora-consumption's derived Growth-Edge projector can
// key an edge on it (the MCQ fast path already does via the loaded Assessment).
// The subscriber gains an optional assessment-title reader (nil-safe: without
// one, the event still emits — just without a title, the prior behaviour).
package subscribers_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// fakeAssessReader satisfies subscribers.AssessmentTitleReader (structurally the
// same Get as domain.AssessmentRepo) returning a fixed title for one id.
type fakeAssessReader struct {
	tenant, id, title string
	err               error
}

func (f fakeAssessReader) Get(_ context.Context, tenantID, id string) (*domain.Assessment, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	if tenantID == f.tenant && id == f.id {
		return &domain.Assessment{Title: f.title}, true, nil
	}
	return nil, false, nil
}

func gradedOEBatch(sub *domain.Submission) subscribers.OEBatchCompletedPayload {
	return subscribers.OEBatchCompletedPayload{
		OEBatchID:    "batch-title-1",
		SubmissionID: sub.ID,
		AssessmentID: sub.AssessmentID,
		BatchOutcome: "ok",
		CompletedAt:  time.Now().UTC(),
		TenantID:     gradingTenant,
		LearnerGCID:  gradingLearner,
		Results: []subscribers.OEBatchGradedAnswer{
			{SubmissionID: sub.ID, TestSetQuestionID: "oe-1", QuestionID: "oe-q-1", PointsEarned: 40, PointsPossible: 50},
		},
	}
}

func TestGradingInbox_OEBatch_EmitsAssessmentTitle(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	reader := fakeAssessReader{tenant: gradingTenant, id: gradingAssessID, title: "Algebra Midterm"}
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore()).
		WithAssessmentReader(reader)

	env := events.EventEnvelope{EventID: "evt-title-1", TenantID: gradingTenant, GCID: gradingLearner}
	if err := s.Handle(context.Background(), env, gradedOEBatch(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	emitted := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")
	if len(emitted) != 1 {
		t.Fatalf("submission.graded.v1: want 1 emitted, got %d", len(emitted))
	}
	if got := emitted[0].Payload["assessment_title"]; got != "Algebra Midterm" {
		t.Errorf("assessment_title = %v, want %q", got, "Algebra Midterm")
	}
}

func TestGradingInbox_OEBatch_NoReader_OmitsTitle(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	// No WithAssessmentReader — prior behaviour: event emits, title absent.
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())

	env := events.EventEnvelope{EventID: "evt-title-2", TenantID: gradingTenant, GCID: gradingLearner}
	if err := s.Handle(context.Background(), env, gradedOEBatch(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	emitted := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")
	if len(emitted) != 1 {
		t.Fatalf("submission.graded.v1: want 1 emitted, got %d", len(emitted))
	}
	if _, present := emitted[0].Payload["assessment_title"]; present {
		t.Errorf("assessment_title must be absent without a reader, got %v", emitted[0].Payload["assessment_title"])
	}
}

func TestGradingInbox_OEBatch_ReaderError_OmitsTitle_StillEmits(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	reader := fakeAssessReader{err: context.DeadlineExceeded}
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore()).
		WithAssessmentReader(reader)

	env := events.EventEnvelope{EventID: "evt-title-3", TenantID: gradingTenant, GCID: gradingLearner}
	// A title-lookup failure must NOT block the grade event (fail-soft: the
	// title is a secondary enrichment; the grade state-transition is primary).
	if err := s.Handle(context.Background(), env, gradedOEBatch(sub)); err != nil {
		t.Fatalf("Handle must not NACK on a title-lookup failure: %v", err)
	}
	emitted := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")
	if len(emitted) != 1 {
		t.Fatalf("submission.graded.v1: want 1 emitted, got %d", len(emitted))
	}
	if _, present := emitted[0].Payload["assessment_title"]; present {
		t.Error("assessment_title must be absent when the lookup errored")
	}
}
