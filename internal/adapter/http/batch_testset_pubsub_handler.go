// batch_testset_pubsub_handler.go — Lane 1c W4 (CHO-1703 / ADR-180 D10)
// batch→test-set assembly push endpoint.
//
// HTTP push handler fronting subscribers.BatchTestSetSubscriber. Receives
// Cloud Pub/Sub push deliveries for chora.creation.question_batch.accepted.v1
// (chora-creation → chora-delivery hop) at
// POST /api/internal/pubsub/batch-testset-inbox.
//
// The topic is Schema-Registry-bound BINARY: message.data carries the
// QuestionBatchAccepted protobuf wire bytes, decoded by the protodecode
// registry entry (real proto.Unmarshal — see the in_app.created dead-letter
// lesson). The JSON fallback inside protodecode keeps transition-window /
// DLQ-replay JSON producers working with the identical projected shape.
//
// Per the existing receiver pattern (grading_pubsub_handler.go):
//   - OIDC audience + trusted-SA allowlist come from env at cmd/server boot
//     (CHORA_PUBSUB_PUSH_AUDIENCE + CHORA_PUBSUB_PUSH_TRUSTED_SA); the
//     handler itself never reads env. NetworkPolicy + Istio authz further
//     restrict /api/internal/* to mesh-internal callers; the chora-gateway
//     internal-pubsub passthrough routes unknown inboxes to chora-delivery
//     by default (no gateway change needed for this inbox).
//   - The subscriber struct is the inbound adapter; this handler is a thin
//     transport-only translator — no business logic.
package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protodecode"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
)

// BatchTestSetInboxPushDeps wires the handler.
type BatchTestSetInboxPushDeps struct {
	// Subscriber is the inbound adapter struct from
	// internal/adapter/subscribers. Required.
	Subscriber *subscribers.BatchTestSetSubscriber
	// Verifier is the OIDC token verifier. Use
	// eventpush.NewVerifier(eventpush.VerifierConfig{}) for dev — caller
	// is then expected to enforce auth via mTLS / NetworkPolicy.
	Verifier *eventpush.Verifier
}

// NewBatchTestSetInboxPushHandler returns the http.Handler bound to the
// question_batch.accepted subscriber. Panics on nil Subscriber.
func NewBatchTestSetInboxPushHandler(deps BatchTestSetInboxPushDeps) http.Handler {
	if deps.Subscriber == nil {
		panic("http: BatchTestSetInboxPushHandler requires Subscriber")
	}
	return eventpush.NewHandler(eventpush.HandlerConfig{
		Verifier: deps.Verifier,
		Dispatch: dispatchBatchTestSetInbox(deps.Subscriber),
	})
}

// dispatchBatchTestSetInbox returns the DispatchFunc that decodes one
// Pub/Sub push delivery + invokes BatchTestSetSubscriber.Handle.
func dispatchBatchTestSetInbox(sub *subscribers.BatchTestSetSubscriber) eventpush.DispatchFunc {
	return func(ctx context.Context, msg eventpush.PushMessage) error {
		topic := msg.Attributes["topic"]
		if topic == "" {
			// Python-side producers cannot stamp an attribute literally
			// named "topic" (collides with PublisherClient.publish()'s
			// positional arg) — honor "event_topic" before defaulting, same
			// as the grading inbox. The Go outbox bridge stamps "topic".
			topic = msg.Attributes["event_topic"]
		}
		if topic == "" {
			topic = subscribers.TopicQuestionBatchAccepted
		}
		// Single-topic endpoint — anything else ack-and-drops so the broker
		// doesn't redeliver a mis-routed subscription binding indefinitely.
		if topic != subscribers.TopicQuestionBatchAccepted {
			return nil
		}

		env, err := decodeGradingInboxEnvelope(msg)
		if err != nil {
			return fmt.Errorf("batch_testset_push: decode envelope: %w", err)
		}

		raw, err := protodecode.DecodePayloadMapWithAttrs(topic, msg.Data, msg.Attributes)
		if err != nil {
			return fmt.Errorf("batch_testset_push: %w", err)
		}

		p := subscribers.QuestionBatchAcceptedPayload{
			JobID:       grStr(raw, "job_id"),
			HostAtomID:  grStr(raw, "host_atom_id"),
			TenantID:    grStr(raw, "tenant_id"),
			AuthorGCID:  grStr(raw, "author_gcid"),
			Traceparent: grStr(raw, "traceparent"),
			Items:       decodeBatchItems(raw),
		}
		if spec, ok := raw["test_set"].(map[string]any); ok {
			p.Title = grStr(spec, "title")
			p.Description = grStr(spec, "description")
		}
		if p.TenantID == "" {
			p.TenantID = env.TenantID
		}
		if p.AuthorGCID == "" {
			p.AuthorGCID = env.GCID
		}
		if p.Traceparent == "" {
			p.Traceparent = env.Traceparent
		}
		return sub.Handle(ctx, env, p)
	}
}

// decodeBatchItems projects the inbound `items` array (the canonical
// QuestionBatchAccepted.Item repeated field) into the subscriber input
// shape. Tolerates both the binary projector's output and a JSON-shape
// producer — numbers are JSON-number float64 on both paths.
func decodeBatchItems(raw map[string]any) []subscribers.QuestionBatchItem {
	arr, _ := raw["items"].([]any)
	out := make([]subscribers.QuestionBatchItem, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, subscribers.QuestionBatchItem{
			QuestionAtomID: grStr(m, "question_atom_id"),
			QuestionID:     grStr(m, "question_id"),
			QuestionType:   grStr(m, "question_type"),
			Points:         int32(grInt(m, "points")),
			DisplayOrder:   int32(grInt(m, "display_order")),
		})
	}
	return out
}
