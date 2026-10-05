// course_progress_pubsub_handler.go - the push inboxes for the ASYNC-mode
// analytics projection (R+ Four-Mode DoD §10.3 step 3, epic CHO-1827).
//
//	POST /api/internal/pubsub/course-progress-advanced-inbox
//	  ← chora.consumption.learning_path.advanced.v1
//	POST /api/internal/pubsub/course-progress-completed-inbox
//	  ← chora.consumption.learning_path.completed.v1
//
// chora-delivery consuming a chora-consumption event: learner progress lives in
// chora_consumption and Analytics lives in chora_delivery, and a cross-DB query
// is FORBIDDEN, so this event pair is the only legal bridge. Both topics already
// exist and already carry course_id, so no new topic was coined.
//
// # WIRE FORMAT: JSON, not protobuf
//
// Both topics are SCHEMALESS (schemaSettings: null, verified live 2026-07-14 by
// the consumption-side learning-path-topics inbox) and chora-consumption's
// protomarshal registry has NO binary encoder for learning_path, so the producer
// JSON-marshals a map[string]any. json.Unmarshal is therefore the CORRECT decode
// here - the mirror image of the graded-inbox regression, where a JSON decode on
// a Schema-Registry BINARY topic silently dead-lettered every message. If a
// binary encoder is ever added on the consumption side, this decode must flip in
// the same change.
//
// # TWO endpoints, not one topic-dispatching endpoint
//
// chora-consumption's own inbox for this same topic pair dispatches on the
// `topic` ATTRIBUTE, and notes that its outbox "does not always stamp it". An
// attribute that is sometimes absent cannot safely discriminate between an
// advance and a completion: a missing attribute would silently fold one into the
// other. Two single-topic endpoints (delivery's own convention) make the ROUTE
// the discriminator, which the subscription pins and which cannot go missing. A
// stamped-but-WRONG topic is still a real misroute and fails loud → DLQ.
//
// These handlers are thin transport translators - the subscriber owns
// idempotency, the RLS tenant scoping, and the projection write.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
)

// CourseProgressPushDeps wires the two course-progress push endpoints.
type CourseProgressPushDeps struct {
	// Subscriber is the typed inbound business adapter. Required.
	Subscriber *subscribers.CourseProgressSubscriber
	// Verifier is the OIDC token verifier (shared pushVerifier).
	Verifier *eventpush.Verifier
}

// learningPathAdvancedBody is the JSON body chora-consumption publishes for
// chora.consumption.learning_path.advanced.v1 (session_completion.go
// publishAdvanced). Only the fields the projection needs are decoded.
//
// `progress_percent` is on the wire and is deliberately NOT decoded: despite the
// name it carries a FRACTION in [0.0, 1.0] (LearningPath.ProgressPercent()), so
// a consumer that trusted the name would report a 42%-done learner as 0.42%.
// current_index/total_atoms carry the same fact unambiguously.
type learningPathAdvancedBody struct {
	PathID       string `json:"path_id"`
	CourseID     string `json:"course_id"`
	LearnerGCID  string `json:"learner_gcid"`
	AtomID       string `json:"atom_id"`
	CurrentIndex int    `json:"current_index"`
	TotalAtoms   int    `json:"total_atoms"`
}

// learningPathCompletedBody is the JSON body chora-consumption publishes for
// chora.consumption.learning_path.completed.v1 (publishCompleted).
type learningPathCompletedBody struct {
	PathID      string     `json:"path_id"`
	CourseID    string     `json:"course_id"`
	LearnerGCID string     `json:"learner_gcid"`
	CompletedAt *time.Time `json:"completed_at"`
}

// NewCourseProgressAdvancedPushHandler binds the learning_path.advanced.v1
// endpoint. Panics on nil Subscriber: a mounted route that ACKs real progress
// events into a void is strictly worse than no route, because it reports success.
func NewCourseProgressAdvancedPushHandler(deps CourseProgressPushDeps) http.Handler {
	if deps.Subscriber == nil {
		panic("http: CourseProgressAdvancedPushHandler requires Subscriber")
	}
	return eventpush.NewHandler(eventpush.HandlerConfig{
		Verifier: deps.Verifier,
		Dispatch: dispatchCourseProgressAdvancedInbox(deps.Subscriber),
	})
}

// dispatchCourseProgressAdvancedInbox decodes a learning_path.advanced.v1
// delivery and invokes the projection subscriber.
func dispatchCourseProgressAdvancedInbox(sub *subscribers.CourseProgressSubscriber) eventpush.DispatchFunc {
	return func(ctx context.Context, msg eventpush.PushMessage) error {
		env, err := courseProgressEnvelope(msg, subscribers.TopicLearningPathAdvanced)
		if err != nil {
			return err
		}
		var b learningPathAdvancedBody
		if err := json.Unmarshal(msg.Data, &b); err != nil {
			return fmt.Errorf("course_progress_advanced: json.Unmarshal payload: %w", err)
		}
		return sub.HandleAdvance(ctx, env, subscribers.LearningPathAdvancedPayload{
			PathID:       strings.TrimSpace(b.PathID),
			CourseID:     strings.TrimSpace(b.CourseID),
			LearnerGCID:  firstNonEmpty(strings.TrimSpace(b.LearnerGCID), env.GCID),
			TenantID:     env.TenantID,
			AtomID:       strings.TrimSpace(b.AtomID),
			CurrentIndex: b.CurrentIndex,
			TotalAtoms:   b.TotalAtoms,
			OccurredAt:   env.OccurredAt,
		})
	}
}

// NewCourseProgressCompletedPushHandler binds the learning_path.completed.v1
// endpoint.
func NewCourseProgressCompletedPushHandler(deps CourseProgressPushDeps) http.Handler {
	if deps.Subscriber == nil {
		panic("http: CourseProgressCompletedPushHandler requires Subscriber")
	}
	return eventpush.NewHandler(eventpush.HandlerConfig{
		Verifier: deps.Verifier,
		Dispatch: dispatchCourseProgressCompletedInbox(deps.Subscriber),
	})
}

// dispatchCourseProgressCompletedInbox decodes a learning_path.completed.v1
// delivery and invokes the projection subscriber.
func dispatchCourseProgressCompletedInbox(sub *subscribers.CourseProgressSubscriber) eventpush.DispatchFunc {
	return func(ctx context.Context, msg eventpush.PushMessage) error {
		env, err := courseProgressEnvelope(msg, subscribers.TopicLearningPathCompleted)
		if err != nil {
			return err
		}
		var b learningPathCompletedBody
		if err := json.Unmarshal(msg.Data, &b); err != nil {
			return fmt.Errorf("course_progress_completed: json.Unmarshal payload: %w", err)
		}
		// The payload's completed_at IS the authoritative completion stamp
		// (consumption writes it once); the envelope clock is the fallback.
		at := env.OccurredAt
		if b.CompletedAt != nil && !b.CompletedAt.IsZero() {
			at = b.CompletedAt.UTC()
		}
		return sub.HandleCompletion(ctx, env, subscribers.LearningPathCompletedPayload{
			PathID:      strings.TrimSpace(b.PathID),
			CourseID:    strings.TrimSpace(b.CourseID),
			LearnerGCID: firstNonEmpty(strings.TrimSpace(b.LearnerGCID), env.GCID),
			TenantID:    env.TenantID,
			OccurredAt:  at,
		})
	}
}

// courseProgressEnvelope pulls + validates the required envelope attributes and
// enforces the expected topic.
//
// A stamped topic that DISAGREES with the route is a misroute and fails loud (→
// DLQ) rather than being folded into the wrong projection leg. An ABSENT topic
// is tolerated: the subscription pins this route to exactly one topic, and
// chora-consumption's outbox does not always stamp the attribute.
func courseProgressEnvelope(msg eventpush.PushMessage, wantTopic string) (events.EventEnvelope, error) {
	attrs := msg.Attributes
	if topic := strings.TrimSpace(attrs["topic"]); topic != "" && topic != wantTopic {
		return events.EventEnvelope{}, fmt.Errorf("course_progress: unexpected topic %q (want %q)", topic, wantTopic)
	}
	eventID := strings.TrimSpace(attrs["event_id"])
	tenantID := strings.TrimSpace(attrs["tenant_id"])
	if eventID == "" {
		return events.EventEnvelope{}, errors.New("course_progress: missing event_id attribute")
	}
	if tenantID == "" {
		return events.EventEnvelope{}, errors.New("course_progress: missing tenant_id attribute")
	}
	if len(msg.Data) == 0 {
		return events.EventEnvelope{}, errors.New("course_progress: empty payload")
	}
	env := events.EventEnvelope{
		EventID:     eventID,
		TenantID:    tenantID,
		GCID:        strings.TrimSpace(attrs["gcid"]),
		Traceparent: strings.TrimSpace(attrs["traceparent"]),
	}
	// occurred_at is the producer clock and drives the reorder watermark. An
	// unparseable value is left zero rather than guessed at; the subscriber then
	// substitutes now() rather than sorting the event before every stored
	// watermark and discarding it forever.
	if v := strings.TrimSpace(attrs["occurred_at"]); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			env.OccurredAt = t.UTC()
		}
	}
	return env, nil
}
