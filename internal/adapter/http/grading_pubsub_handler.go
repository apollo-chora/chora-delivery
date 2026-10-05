// grading_pubsub_handler.go — Fix-E / ADR-155 Lane B completion-event push.
//
// HTTP push handler fronting subscribers.GradingInboxSubscriber. Receives
// Cloud Pub/Sub push deliveries for chora.delivery.grading.oe_batch_completed.v1
// (chora-ai-kernel-orchestrator → chora-delivery hop).
//
// Per ADR-148 receiver pattern + the always-loaded secrets-and-env rule:
//   - OIDC audience + trusted-SA allowlist come from env at cmd/server boot
//     (CHORA_PUBSUB_PUSH_AUDIENCE + CHORA_PUBSUB_PUSH_TRUSTED_SA). The
//     handler itself never reads env.
//   - NetworkPolicy + Istio authz further restrict /api/internal/* paths to
//     mesh-internal callers (Cloud Pub/Sub's egress IPs are allowed at the
//     ingress gateway).
//
// Per ddd-enforcement:
//   - The subscriber struct is the inbound adapter.
//   - The HTTP handler is a thin transport-only translator — no business
//     logic.
//
// Mirrors services/chora-consumption/internal/adapter/http/familiar_growth_pubsub_handler.go.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protodecode"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
)

// GradingInboxPushDeps wires the handler.
type GradingInboxPushDeps struct {
	// Subscriber is the inbound adapter struct from
	// internal/adapter/subscribers. Required.
	Subscriber *subscribers.GradingInboxSubscriber
	// Verifier is the OIDC token verifier. Use
	// eventpush.NewVerifier(eventpush.VerifierConfig{}) for dev — caller
	// is then expected to enforce auth via mTLS / NetworkPolicy.
	Verifier *eventpush.Verifier
}

// NewGradingInboxPushHandler returns the http.Handler bound to the OE-batch
// completion subscriber. Panics on nil Subscriber.
func NewGradingInboxPushHandler(deps GradingInboxPushDeps) http.Handler {
	if deps.Subscriber == nil {
		panic("http: GradingInboxPushHandler requires Subscriber")
	}
	return eventpush.NewHandler(eventpush.HandlerConfig{
		Verifier: deps.Verifier,
		Dispatch: dispatchGradingInbox(deps.Subscriber),
	})
}

// dispatchGradingInbox returns the DispatchFunc that decodes one Pub/Sub
// push delivery + invokes GradingInboxSubscriber.Handle.
func dispatchGradingInbox(sub *subscribers.GradingInboxSubscriber) eventpush.DispatchFunc {
	return func(ctx context.Context, msg eventpush.PushMessage) error {
		topic := msg.Attributes["topic"]
		if topic == "" {
			// The Python oe_grading_crew publisher cannot emit a Pub/Sub
			// attribute literally named "topic" (it collides with the
			// google-cloud-pubsub PublisherClient.publish() positional arg and
			// raises TypeError), so the per-submission completion carries its
			// routing topic under "event_topic". Honor it before defaulting to
			// the legacy batch topic. Go producers (oe_batch path) set "topic"
			// directly and are unaffected.
			topic = msg.Attributes["event_topic"]
		}
		if topic == "" {
			topic = subscribers.TopicGradingOEBatchCompleted
		}
		// This push endpoint serves both the legacy batch completion (ADR-155)
		// and the per-submission completion (ADR-172). Anything else
		// ack-and-drops so the broker doesn't redeliver indefinitely.
		if topic != subscribers.TopicGradingOEBatchCompleted &&
			topic != subscribers.TopicGradingSubmissionCompleted {
			return nil
		}

		env, err := decodeGradingInboxEnvelope(msg)
		if err != nil {
			return fmt.Errorf("grading_inbox_push: decode envelope: %w", err)
		}

		// ADR-172 per-submission completion — JSON payload from the
		// oe_grading_crew orchestrator.
		if topic == subscribers.TopicGradingSubmissionCompleted {
			p, derr := decodeSubmissionCompleted(msg, env)
			if derr != nil {
				return fmt.Errorf("grading_inbox_push: %w", derr)
			}
			return sub.HandleSubmissionCompleted(ctx, env, p)
		}

		raw, err := protodecode.DecodePayloadMapWithAttrs(topic, msg.Data, msg.Attributes)
		if err != nil {
			return fmt.Errorf("grading_inbox_push: %w", err)
		}

		p := subscribers.OEBatchCompletedPayload{
			OEBatchID:      grStr(raw, "oe_batch_id"),
			SubmissionID:   grStr(raw, "submission_id"),
			AssessmentID:   grStr(raw, "assessment_id"),
			BatchOutcome:   grStr(raw, "batch_outcome"),
			FailureMessage: grStr(raw, "failure_message"),
			TenantID:       grStr(raw, "tenant_id"),
			LearnerGCID:    grStr(raw, "learner_gcid"),
			Traceparent:    grStr(raw, "traceparent"),
			Results:        decodeGradedResults(raw),
		}
		if p.TenantID == "" {
			p.TenantID = env.TenantID
		}
		if p.LearnerGCID == "" {
			p.LearnerGCID = env.GCID
		}
		if p.Traceparent == "" {
			p.Traceparent = env.Traceparent
		}
		if t := grStr(raw, "completed_at"); t != "" {
			if parsed, perr := time.Parse(time.RFC3339Nano, t); perr == nil {
				p.CompletedAt = parsed
			} else if parsed, perr := time.Parse(time.RFC3339, t); perr == nil {
				p.CompletedAt = parsed
			}
		}
		return sub.Handle(ctx, env, p)
	}
}

// decodeGradedResults projects the inbound JSON `graded` array (the canonical
// QuestionGradeBatchResult repeated field) into the subscriber input shape.
//
// Tolerates either snake_case (legacy JSON publisher) OR the canonical
// snake_case projection from protodecode's binary path (when codegen lands).
func decodeGradedResults(raw map[string]any) []subscribers.OEBatchGradedAnswer {
	arr, _ := raw["graded"].([]any)
	out := make([]subscribers.OEBatchGradedAnswer, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, subscribers.OEBatchGradedAnswer{
			SubmissionID:         grStr(m, "submission_id"),
			TestSetQuestionID:    grStr(m, "test_set_question_id"),
			QuestionID:           grStr(m, "question_id"),
			LearnerGCID:          grStr(m, "learner_gcid"),
			PointsEarned:         grFloat(m, "points_earned"),
			PointsPossible:       grInt(m, "points_possible"),
			CriterionScoresJSON:  grStr(m, "criterion_scores_json"),
			LLMEvaluatorFeedback: grStr(m, "llm_evaluator_feedback"),
		})
	}
	return out
}

// decodeSubmissionCompleted projects the ADR-172 per-submission completion
// JSON payload into the subscriber input shape. The oe_grading_crew
// orchestrator publishes this event as JSON (envelope fields ride Pub/Sub
// attributes; the body carries graded[] + overall_comment).
func decodeSubmissionCompleted(msg eventpush.PushMessage, env events.EventEnvelope) (subscribers.SubmissionCompletedPayload, error) {
	var raw map[string]any
	if len(msg.Data) > 0 {
		if err := json.Unmarshal(msg.Data, &raw); err != nil {
			return subscribers.SubmissionCompletedPayload{}, fmt.Errorf("decode submission_completed body: %w", err)
		}
	}
	p := subscribers.SubmissionCompletedPayload{
		SubmissionID:             grStr(raw, "submission_id"),
		AssessmentID:             grStr(raw, "assessment_id"),
		LearnerGCID:              grStr(raw, "learner_gcid"),
		Outcome:                  grStr(raw, "outcome"),
		FailureMessage:           grStr(raw, "failure_message"),
		OverallComment:           grStr(raw, "overall_comment"),
		OverallCommentModelID:    grStr(raw, "overall_comment_model_id"),
		OverallCommentResponseID: grStr(raw, "overall_comment_response_id"),
		TenantID:                 grStr(raw, "tenant_id"),
		Traceparent:              grStr(raw, "traceparent"),
		Graded:                   decodeSubmissionGrades(raw),
	}
	if p.TenantID == "" {
		p.TenantID = env.TenantID
	}
	if p.LearnerGCID == "" {
		p.LearnerGCID = env.GCID
	}
	if p.Traceparent == "" {
		p.Traceparent = env.Traceparent
	}
	if t := grStr(raw, "completed_at"); t != "" {
		if parsed, perr := time.Parse(time.RFC3339Nano, t); perr == nil {
			p.CompletedAt = parsed
		} else if parsed, perr := time.Parse(time.RFC3339, t); perr == nil {
			p.CompletedAt = parsed
		}
	}
	return p, nil
}

func decodeSubmissionGrades(raw map[string]any) []subscribers.SubmissionGradedAnswer {
	arr, _ := raw["graded"].([]any)
	out := make([]subscribers.SubmissionGradedAnswer, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		qf, _ := m["quality_flagged"].(bool)
		out = append(out, subscribers.SubmissionGradedAnswer{
			TestSetQuestionID:   grStr(m, "test_set_question_id"),
			QuestionID:          grStr(m, "question_id"),
			PointsEarned:        grFloat(m, "points_earned"),
			PointsPossible:      grInt(m, "points_possible"),
			CriterionScoresJSON: grStr(m, "criterion_scores_json"),
			Comment:             grStr(m, "comment"),
			GradingModelID:      grStr(m, "grading_model_id"),
			GradingResponseID:   grStr(m, "grading_response_id"),
			QualityFlagged:      qf,
		})
	}
	return out
}

// decodeGradingInboxEnvelope reconstructs an events.EventEnvelope from the
// Pub/Sub attribute map. Mirrors chora-consumption's decodeChoraEnvelope.
func decodeGradingInboxEnvelope(msg eventpush.PushMessage) (events.EventEnvelope, error) {
	attrs := msg.Attributes
	if len(attrs) == 0 {
		return events.EventEnvelope{}, errors.New("missing envelope attributes")
	}
	env := events.EventEnvelope{
		EventID:        attrs["event_id"],
		IdempotencyKey: attrs["idempotency_key"],
		TenantID:       attrs["tenant_id"],
		GCID:           attrs["gcid"],
		Traceparent:    attrs["traceparent"],
		Tracestate:     attrs["tracestate"],
		SourceProject:  attrs["source_project"],
		SourceService:  attrs["source_service"],
	}
	if v := attrs["occurred_at"]; v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			env.OccurredAt = t
		}
	}
	if v := attrs["published_at"]; v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			env.PublishedAt = t
		}
	}
	if v := attrs["schema_version"]; v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			env.SchemaVersion = int32(n)
		}
	}
	return env, nil
}

// grStr returns the string value at the supplied key (empty if missing).
func grStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// grFloat returns the float64 value at the supplied key. JSON unmarshal
// numbers land as float64 by default.
func grFloat(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

// grInt returns the int value at the supplied key. JSON unmarshal numbers
// land as float64 by default.
func grInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
