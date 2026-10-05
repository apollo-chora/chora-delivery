// live_quiz_score_test.go — durable score_awarded event (ADR-168).
package events_test

import (
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
)

func TestPublisher_PublishLiveQuizScoreAwarded(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishLiveQuizScoreAwarded(events.LiveQuizScoreAwarded{
		TenantID:        tenantA,
		GCID:            gcidA,
		SessionID:       "01970000-0000-7000-a000-000000000001",
		LiveQuizID:      "01970000-0000-7000-b000-000000000001",
		QuestionID:      "q1",
		AwardedPoints:   750,
		CumulativeScore: 1750,
		Correct:         true,
		AnswerMillis:    4200,
	})
	if err != nil {
		t.Fatalf("PublishLiveQuizScoreAwarded: %v", err)
	}
	if got.Topic != "chora.delivery.live_quiz_session.score_awarded.v1" {
		t.Fatalf("topic: got %q", got.Topic)
	}
	if got.Envelope.TenantID != tenantA || got.Envelope.GCID != gcidA {
		t.Fatalf("envelope tenant/gcid wrong: %+v", got.Envelope)
	}
	if got.Envelope.IdempotencyKey == "" || got.Envelope.EventID == "" {
		t.Fatalf("envelope must carry idempotency_key + event_id")
	}
	if got.Payload["awarded_points"] != 750 || got.Payload["correct"] != true {
		t.Fatalf("payload wrong: %#v", got.Payload)
	}
}

func TestPublisher_LiveQuizScoreAwarded_Validation(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	if _, err := pub.PublishLiveQuizScoreAwarded(events.LiveQuizScoreAwarded{SessionID: "s"}); err == nil {
		t.Fatalf("want error when tenant_id blank")
	}
	if _, err := pub.PublishLiveQuizScoreAwarded(events.LiveQuizScoreAwarded{TenantID: tenantA}); err == nil {
		t.Fatalf("want error when session_id blank")
	}
}

// CR2-C3: an atom-linked question's score_awarded carries atom_id + topic_tags
// in the payload (consumed by chora-consumption's derived-weakness subscriber).
func TestPublisher_LiveQuizScoreAwarded_CarriesAtomAndTopicTags(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishLiveQuizScoreAwarded(events.LiveQuizScoreAwarded{
		TenantID:   tenantA,
		GCID:       gcidA,
		SessionID:  "01970000-0000-7000-a000-000000000001",
		LiveQuizID: "01970000-0000-7000-b000-000000000001",
		QuestionID: "q1",
		Correct:    true,
		AtomID:     "atom-uuid-9",
		TopicTags:  []string{"scrum", "agile"},
	})
	if err != nil {
		t.Fatalf("PublishLiveQuizScoreAwarded: %v", err)
	}
	if got.Payload["atom_id"] != "atom-uuid-9" {
		t.Errorf("payload atom_id = %#v; want atom-uuid-9", got.Payload["atom_id"])
	}
	tags, ok := got.Payload["topic_tags"].([]string)
	if !ok || len(tags) != 2 || tags[0] != "scrum" || tags[1] != "agile" {
		t.Errorf("payload topic_tags = %#v; want [scrum agile]", got.Payload["topic_tags"])
	}
}
