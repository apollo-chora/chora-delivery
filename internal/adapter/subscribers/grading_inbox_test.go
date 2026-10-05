// grading_inbox_test.go — adapter-level coverage of the OE batch completion
// subscriber. Domain semantics (ApplyOEGradingBatch) are covered in the
// domain package; this file verifies the SUBSCRIBER concerns:
//   - inbox dedupe on event_id
//   - tenant_id + submission_id validation
//   - submission load + save round-trip
//   - submission.graded.v1 emission on all-graded
//   - graceful no-op when the submission can't be located
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

const (
	gradingTenant   = "11111111-1111-7111-8111-111111111111"
	gradingLearner  = "00000000-0000-7000-8000-000000002999"
	gradingAssessID = "01985e7f-1234-7abc-8def-000000000a01"
)

func newPendingOESubmission(t *testing.T) (*domain.InMemSubmissionRepo, *domain.Submission) {
	t.Helper()
	repo := domain.NewInMemSubmissionRepo()
	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   gradingAssessID,
		TenantID:       gradingTenant,
		LearnerGCID:    gradingLearner,
		AttemptNumber:  1,
		OpensAt:        time.Now().Add(-1 * time.Minute),
		ClosesAt:       time.Now().Add(time.Hour),
		MaxScore:       100,
		PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	mcqCorrect := true
	sub.Answers = []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "mcq-1",
			QuestionID:        "mcq-q-1",
			QuestionType:      domain.QuestionTypeMCQ,
			MCQChoiceID:       "A",
			MCQCorrect:        &mcqCorrect,
			PointsEarned:      50,
			PointsPossible:    50,
		},
		{
			TestSetQuestionID: "oe-1",
			QuestionID:        "oe-q-1",
			QuestionType:      domain.QuestionTypeOE,
			OEResponseText:    "an essay",
			PointsPossible:    50,
		},
	}
	sub.State = domain.SubmissionStateSubmitted
	sub.MoveToOEPending(time.Now())
	if err := repo.Save(context.Background(), sub); err != nil {
		t.Fatalf("seed submission: %v", err)
	}
	return repo, sub
}

func TestGradingInboxSubscriber_AllGraded_AdvancesAndEmitsGraded(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())

	env := events.EventEnvelope{EventID: "evt-1", TenantID: gradingTenant, GCID: gradingLearner}
	p := subscribers.OEBatchCompletedPayload{
		OEBatchID:    "batch-1",
		SubmissionID: sub.ID,
		AssessmentID: sub.AssessmentID,
		BatchOutcome: "ok",
		CompletedAt:  time.Now().UTC(),
		TenantID:     gradingTenant,
		LearnerGCID:  gradingLearner,
		Results: []subscribers.OEBatchGradedAnswer{
			{
				SubmissionID:         sub.ID,
				TestSetQuestionID:    "oe-1",
				QuestionID:           "oe-q-1",
				PointsEarned:         40,
				PointsPossible:       50,
				LLMEvaluatorFeedback: "good",
				CriterionScoresJSON:  `{"clarity":4}`,
			},
		},
	}
	if err := s.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// Submission advanced.
	got, ok, _ := repo.Get(context.Background(), gradingTenant, sub.ID)
	if !ok {
		t.Fatalf("submission missing post-handle")
	}
	if got.State != domain.SubmissionStateGradedPendingRelease {
		t.Errorf("state: want GRADED_PENDING_RELEASE, got %s", got.State)
	}
	if got.TotalScore != 90 {
		t.Errorf("total: want 90, got %f", got.TotalScore)
	}
	// submission.graded.v1 emitted.
	emitted := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")
	if len(emitted) != 1 {
		t.Fatalf("submission.graded.v1: want 1 emitted, got %d", len(emitted))
	}
	if emitted[0].Envelope.TenantID != gradingTenant {
		t.Errorf("emitted tenant_id: want %s, got %s", gradingTenant, emitted[0].Envelope.TenantID)
	}
}

func TestGradingInboxSubscriber_PartialBatch_NoGradedEvent(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	// Add a second OE answer to force partial state.
	sub.Answers = append(sub.Answers, domain.SubmissionAnswer{
		TestSetQuestionID: "oe-2",
		QuestionID:        "oe-q-2",
		QuestionType:      domain.QuestionTypeOE,
		OEResponseText:    "essay 2",
		PointsPossible:    50,
	})
	_ = repo.Save(context.Background(), sub)

	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())

	env := events.EventEnvelope{EventID: "evt-partial", TenantID: gradingTenant, GCID: gradingLearner}
	p := subscribers.OEBatchCompletedPayload{
		SubmissionID: sub.ID,
		BatchOutcome: "ok",
		TenantID:     gradingTenant,
		Results: []subscribers.OEBatchGradedAnswer{
			{TestSetQuestionID: "oe-1", PointsEarned: 20, PointsPossible: 50},
		},
	}
	if err := s.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")) != 0 {
		t.Errorf("partial batch: expected NO submission.graded.v1 event")
	}
	got, _, _ := repo.Get(context.Background(), gradingTenant, sub.ID)
	if got.State != domain.SubmissionStatePendingOEGrading {
		t.Errorf("partial state: want PENDING_OE_GRADING, got %s", got.State)
	}
}

func TestGradingInboxSubscriber_DuplicateEventID_Dedupes(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())

	env := events.EventEnvelope{EventID: "evt-dup", TenantID: gradingTenant, GCID: gradingLearner}
	p := subscribers.OEBatchCompletedPayload{
		SubmissionID: sub.ID,
		BatchOutcome: "ok",
		TenantID:     gradingTenant,
		Results: []subscribers.OEBatchGradedAnswer{
			{TestSetQuestionID: "oe-1", PointsEarned: 40, PointsPossible: 50},
		},
	}
	if err := s.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := s.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("dup Handle: %v", err)
	}
	emitted := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")
	if len(emitted) != 1 {
		t.Errorf("dedupe: expected single submission.graded.v1, got %d", len(emitted))
	}
}

func TestGradingInboxSubscriber_FailedBatch_NoStateChange(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())

	env := events.EventEnvelope{EventID: "evt-fail", TenantID: gradingTenant, GCID: gradingLearner}
	p := subscribers.OEBatchCompletedPayload{
		OEBatchID:      "01985e7f-3333-7bat-0000-000000000001",
		SubmissionID:   sub.ID,
		AssessmentID:   sub.AssessmentID,
		BatchOutcome:   "failed",
		FailureMessage: "vertex 5xx",
		TenantID:       gradingTenant,
		LearnerGCID:    gradingLearner,
		Results:        nil,
	}
	if err := s.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("Handle (failed batch): %v", err)
	}
	got, _, _ := repo.Get(context.Background(), gradingTenant, sub.ID)
	if got.State != domain.SubmissionStatePendingOEGrading {
		t.Errorf("failed batch state: want PENDING_OE_GRADING, got %s", got.State)
	}
	if len(filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")) != 0 {
		t.Errorf("failed batch: expected NO submission.graded.v1 event")
	}
	// E2E-BE-8 — subscriber MUST emit chora.delivery.grading.failed.v1 on
	// terminal failure so O+ governance + R+ monitoring can react.
	failedEvents := filterByTopic(pub.History(), "chora.delivery.grading.failed.v1")
	if len(failedEvents) != 1 {
		t.Fatalf("failed batch: want 1 grading.failed.v1 event, got %d", len(failedEvents))
	}
	ev := failedEvents[0]
	if ev.Envelope.TenantID != gradingTenant {
		t.Errorf("failed event tenant_id: want %s, got %s", gradingTenant, ev.Envelope.TenantID)
	}
	if got, _ := ev.Payload["submission_id"].(string); got != sub.ID {
		t.Errorf("failed event submission_id: want %s, got %s", sub.ID, got)
	}
	if got, _ := ev.Payload["assessment_id"].(string); got != sub.AssessmentID {
		t.Errorf("failed event assessment_id: want %s, got %s", sub.AssessmentID, got)
	}
	if got, _ := ev.Payload["failure_message"].(string); got != "vertex 5xx" {
		t.Errorf("failed event failure_message: want 'vertex 5xx', got %q", got)
	}
	if got, _ := ev.Payload["oe_batch_id"].(string); got != "01985e7f-3333-7bat-0000-000000000001" {
		t.Errorf("failed event oe_batch_id: want batch id, got %q", got)
	}
	// status MUST be GRADING_JOB_STATUS_FAILED (= 8).
	if got, _ := ev.Payload["status"].(int32); got != 8 {
		t.Errorf("failed event status: want 8 (FAILED), got %d", got)
	}
}

// TestGradingInboxSubscriber_SkippedBatch_EmitsFailedEvent — when the
// orchestrator returns batch_outcome="skipped" (mana/guardrail veto) the
// terminal-failure event must still fire so O+ governance + R+ admin can
// react. The submission FSM stays in PENDING_OE_GRADING (no auto-recovery)
// but the failure marker is now visible.
func TestGradingInboxSubscriber_SkippedBatch_EmitsFailedEvent(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())

	env := events.EventEnvelope{EventID: "evt-skipped", TenantID: gradingTenant, GCID: gradingLearner}
	p := subscribers.OEBatchCompletedPayload{
		OEBatchID:      "01985e7f-3333-7bat-0000-000000000002",
		SubmissionID:   sub.ID,
		AssessmentID:   sub.AssessmentID,
		BatchOutcome:   "skipped",
		FailureMessage: "tenant mana pool exhausted",
		TenantID:       gradingTenant,
		LearnerGCID:    gradingLearner,
	}
	if err := s.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("Handle (skipped batch): %v", err)
	}
	failedEvents := filterByTopic(pub.History(), "chora.delivery.grading.failed.v1")
	if len(failedEvents) != 1 {
		t.Fatalf("skipped batch: want 1 grading.failed.v1 event, got %d", len(failedEvents))
	}
}

func TestGradingInboxSubscriber_RejectsMissingEventID(t *testing.T) {
	repo := domain.NewInMemSubmissionRepo()
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	err := s.Handle(context.Background(), events.EventEnvelope{TenantID: gradingTenant}, subscribers.OEBatchCompletedPayload{
		SubmissionID: "sub-1",
		BatchOutcome: "ok",
		TenantID:     gradingTenant,
	})
	if err == nil {
		t.Fatalf("want error on missing event_id, got nil")
	}
}

func TestGradingInboxSubscriber_RejectsMissingTenant(t *testing.T) {
	repo := domain.NewInMemSubmissionRepo()
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	err := s.Handle(context.Background(), events.EventEnvelope{EventID: "evt-1"}, subscribers.OEBatchCompletedPayload{
		SubmissionID: "sub-1",
		BatchOutcome: "ok",
	})
	if err == nil {
		t.Fatalf("want error on missing tenant_id, got nil")
	}
}

func TestGradingInboxSubscriber_UnknownSubmission_Acks(t *testing.T) {
	repo := domain.NewInMemSubmissionRepo()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())
	err := s.Handle(context.Background(), events.EventEnvelope{EventID: "evt-1", TenantID: gradingTenant},
		subscribers.OEBatchCompletedPayload{
			SubmissionID: "does-not-exist",
			BatchOutcome: "ok",
			TenantID:     gradingTenant,
		})
	if err != nil {
		t.Fatalf("unknown submission should ack-and-drop, got %v", err)
	}
	if len(filterByTopic(pub.History(), "chora.delivery.submission.graded.v1")) != 0 {
		t.Errorf("unknown submission: expected NO emitted events")
	}
}

func TestGradingInboxSubscriber_SubscribedTopicConstant(t *testing.T) {
	s := subscribers.NewGradingInboxSubscriber(domain.NewInMemSubmissionRepo(), nil, nil)
	if got := s.SubscribedTopic(); got != "chora.delivery.grading.oe_batch_completed.v1" {
		t.Errorf("SubscribedTopic: got %q", got)
	}
}

// filterByTopic returns events whose Topic matches the supplied string.
func filterByTopic(in []events.PublishedEvent, topic string) []events.PublishedEvent {
	out := make([]events.PublishedEvent, 0)
	for _, e := range in {
		if e.Topic == topic {
			out = append(out, e)
		}
	}
	return out
}
