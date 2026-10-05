// publisher_session_testset_test.go — top-up coverage for the InMemoryPublisher
// emitters that previously had zero coverage: live-quiz session lifecycle,
// the five test-set lifecycle topics, and the certification no-hash default.
package events_test

import (
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
)

// -----------------------------------------------------------------------------
// ADR-168 session lifecycle
// -----------------------------------------------------------------------------

func TestPublisher_PublishLiveQuizSessionStarted(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishLiveQuizSessionStarted(events.LiveQuizSessionStarted{
		TenantID:       tenantA,
		GCID:           gcidA,
		SessionID:      "01970000-0000-7000-a000-000000000001",
		LiveQuizID:     "01970000-0000-7000-b000-000000000001",
		InstructorGCID: gcidA,
		StartedAt:      time.Now().UTC(),
		Traceparent:    "00-0123456789abcdef0123456789abcdef-fedcba9876543210-01",
	})
	if err != nil {
		t.Fatalf("PublishLiveQuizSessionStarted: %v", err)
	}
	if got.Topic != events.TopicLiveQuizSessionStarted {
		t.Fatalf("topic: got %q", got.Topic)
	}
	if !strings.Contains(got.Envelope.IdempotencyKey, "session_started") {
		t.Fatalf("idempotency_key: got %q", got.Envelope.IdempotencyKey)
	}
	if got.Payload["session_id"] != "01970000-0000-7000-a000-000000000001" {
		t.Fatalf("payload session_id: %v", got.Payload["session_id"])
	}
	if _, ok := got.Payload["started_at"].(time.Time); !ok {
		t.Fatalf("payload started_at must be time.Time, got %T", got.Payload["started_at"])
	}
}

func TestPublisher_PublishLiveQuizSessionStarted_Validation(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	if _, err := pub.PublishLiveQuizSessionStarted(events.LiveQuizSessionStarted{SessionID: "s"}); err == nil {
		t.Fatal("want error when tenant_id blank")
	}
	if _, err := pub.PublishLiveQuizSessionStarted(events.LiveQuizSessionStarted{TenantID: tenantA}); err == nil {
		t.Fatal("want error when session_id blank")
	}
}

func TestPublisher_PublishLiveQuizSessionEnded(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishLiveQuizSessionEnded(events.LiveQuizSessionEnded{
		TenantID:       tenantA,
		GCID:           gcidA,
		SessionID:      "01970000-0000-7000-a000-000000000001",
		LiveQuizID:     "01970000-0000-7000-b000-000000000001",
		TotalResponses: 23,
		EndedAt:        time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("PublishLiveQuizSessionEnded: %v", err)
	}
	if got.Topic != events.TopicLiveQuizSessionEnded {
		t.Fatalf("topic: got %q", got.Topic)
	}
	if !strings.Contains(got.Envelope.IdempotencyKey, "session_ended") {
		t.Fatalf("idempotency_key: got %q", got.Envelope.IdempotencyKey)
	}
	if got.Payload["total_responses"] != 23 {
		t.Fatalf("payload total_responses: %v", got.Payload["total_responses"])
	}
}

func TestPublisher_PublishLiveQuizSessionEnded_Validation(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	if _, err := pub.PublishLiveQuizSessionEnded(events.LiveQuizSessionEnded{SessionID: "s"}); err == nil {
		t.Fatal("want error when tenant_id blank")
	}
	if _, err := pub.PublishLiveQuizSessionEnded(events.LiveQuizSessionEnded{TenantID: tenantA}); err == nil {
		t.Fatal("want error when session_id blank")
	}
}

// -----------------------------------------------------------------------------
// TestSet lifecycle — five topics + idempotency-key stability
// -----------------------------------------------------------------------------

func TestPublisher_PublishTestSetLifecycle(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")

	created, err := pub.PublishTestSetCreated(events.TestSetCreated{
		TenantID: tenantA, GCID: gcidA, TestSetID: "ts-1", AuthorGCID: gcidA, Title: "Scrum 101",
	})
	if err != nil {
		t.Fatalf("PublishTestSetCreated: %v", err)
	}
	if created.Topic != events.TopicTestSetCreated {
		t.Fatalf("created topic: %q", created.Topic)
	}
	if !strings.Contains(created.Envelope.IdempotencyKey, "ts-1") {
		t.Fatalf("created idempotency_key: %q", created.Envelope.IdempotencyKey)
	}
	if created.Payload["author_gcid"] != gcidA || created.Payload["title"] != "Scrum 101" {
		t.Fatalf("created payload: %v", created.Payload)
	}

	added, err := pub.PublishTestSetQuestionAdded(events.TestSetQuestionAdded{
		TenantID: tenantA, GCID: gcidA, TestSetID: "ts-1", TestSetQuestionID: "q-1",
		QuestionAtomID: "atom-1", QuestionType: "mcq", DisplayOrder: 1, Points: 5,
	})
	if err != nil {
		t.Fatalf("PublishTestSetQuestionAdded: %v", err)
	}
	if added.Topic != events.TopicTestSetQuestionAdded {
		t.Fatalf("added topic: %q", added.Topic)
	}
	if !strings.Contains(added.Envelope.IdempotencyKey, "question_added") {
		t.Fatalf("added idempotency_key: %q", added.Envelope.IdempotencyKey)
	}
	if added.Payload["question_type"] != "mcq" || added.Payload["points"] != float64(5) {
		t.Fatalf("added payload: %v", added.Payload)
	}

	updated, err := pub.PublishTestSetQuestionUpdated(events.TestSetQuestionUpdated{
		TenantID: tenantA, GCID: gcidA, TestSetID: "ts-1", TestSetQuestionID: "q-1",
		DisplayOrder: 2, Points: 10,
	})
	if err != nil {
		t.Fatalf("PublishTestSetQuestionUpdated: %v", err)
	}
	if updated.Topic != events.TopicTestSetQuestionUpdated {
		t.Fatalf("updated topic: %q", updated.Topic)
	}
	if !strings.Contains(updated.Envelope.IdempotencyKey, "question_updated") {
		t.Fatalf("updated idempotency_key: %q", updated.Envelope.IdempotencyKey)
	}
	if updated.Payload["display_order"] != int32(2) || updated.Payload["points"] != float64(10) {
		t.Fatalf("updated payload: %v", updated.Payload)
	}

	removed, err := pub.PublishTestSetQuestionRemoved(events.TestSetQuestionRemoved{
		TenantID: tenantA, GCID: gcidA, TestSetID: "ts-1", TestSetQuestionID: "q-1",
	})
	if err != nil {
		t.Fatalf("PublishTestSetQuestionRemoved: %v", err)
	}
	if removed.Topic != events.TopicTestSetQuestionRemoved {
		t.Fatalf("removed topic: %q", removed.Topic)
	}
	if !strings.Contains(removed.Envelope.IdempotencyKey, "question_removed") {
		t.Fatalf("removed idempotency_key: %q", removed.Envelope.IdempotencyKey)
	}

	published, err := pub.PublishTestSetPublished(events.TestSetPublished{
		TenantID: tenantA, GCID: gcidA, TestSetID: "ts-1", AuthorGCID: gcidA,
		QuestionCount: 2, TotalPoints: 15,
	})
	if err != nil {
		t.Fatalf("PublishTestSetPublished: %v", err)
	}
	if published.Topic != events.TopicTestSetPublished {
		t.Fatalf("published topic: %q", published.Topic)
	}
	if published.Payload["question_count"] != int32(2) || published.Payload["total_points"] != float64(15) {
		t.Fatalf("published payload: %v", published.Payload)
	}

	// Published idempotency key is deterministic on test_set_id.
	published2, _ := pub.PublishTestSetPublished(events.TestSetPublished{TenantID: tenantA, TestSetID: "ts-1"})
	if published2.Envelope.IdempotencyKey != published.Envelope.IdempotencyKey {
		t.Fatalf("published idempotency_key must be deterministic on test_set_id")
	}

	if len(pub.History()) != 6 {
		t.Fatalf("expected 6 events in history, got %d", len(pub.History()))
	}
}

func TestPublisher_TestSetLifecycle_RejectsMissingTenant(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	if _, err := pub.PublishTestSetCreated(events.TestSetCreated{}); err == nil {
		t.Fatal("TestSetCreated: want error")
	}
	if _, err := pub.PublishTestSetQuestionAdded(events.TestSetQuestionAdded{}); err == nil {
		t.Fatal("TestSetQuestionAdded: want error")
	}
	if _, err := pub.PublishTestSetQuestionUpdated(events.TestSetQuestionUpdated{}); err == nil {
		t.Fatal("TestSetQuestionUpdated: want error")
	}
	if _, err := pub.PublishTestSetQuestionRemoved(events.TestSetQuestionRemoved{}); err == nil {
		t.Fatal("TestSetQuestionRemoved: want error")
	}
	if _, err := pub.PublishTestSetPublished(events.TestSetPublished{}); err == nil {
		t.Fatal("TestSetPublished: want error")
	}
}

// -----------------------------------------------------------------------------
// CertificationIssued no-hash default + CourseCreated missing-tenant branch
// -----------------------------------------------------------------------------

func TestPublisher_PublishCertificationIssued_NilHashDefaults(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishCertificationIssued(events.CertificationIssued{
		TenantID:        tenantA,
		GCID:            gcidA,
		CertificationID: "cert-1",
		CourseID:        "course-1",
		LearnerGCID:     gcidA,
	})
	if err != nil {
		t.Fatalf("PublishCertificationIssued: %v", err)
	}
	if !strings.Contains(got.Envelope.IdempotencyKey, "no-hash") {
		t.Fatalf("expected no-hash fallback in idempotency_key, got %q", got.Envelope.IdempotencyKey)
	}
	if got.Payload["hash"] != "" {
		t.Fatalf("payload hash must stay empty on the wire, got %v", got.Payload["hash"])
	}
}

func TestPublisher_PublishCourseCreated_RejectsMissingTenant(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	if _, err := pub.PublishCourseCreated(events.CourseCreated{}); err == nil {
		t.Fatal("expected error for missing tenant_id")
	}
}
