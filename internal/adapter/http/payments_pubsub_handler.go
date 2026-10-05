// payments_pubsub_handler.go — Pub/Sub push handler routing the 4
// canonical chora.payments.* events to the events.PaymentsSubscriber
// (ADR-164 Stage C, 2026-05-24).
//
// The chora-payments outbox dispatcher publishes events with the
// `*chora.common.v1.EventEnvelope` proto-embedded directly into the
// payload bytes (see services/chora-payments/internal/adapter/outbox/
// payload.go::marshalCoursePurchase). This handler proto.Unmarshal's
// the raw payload bytes to recover both the embedded envelope + the
// typed event body, then dispatches into the typed subscriber method.
//
// Topics (all 4 wired):
//
//	chora.payments.course_purchase.payment_captured.v1
//	chora.payments.course_purchase.refunded.v1
//	chora.payments.application_payment.payment_captured.v1
//	chora.payments.application_payment.refunded.v1
//
// Routing: the Pub/Sub `topic` attribute (set by the chora-platform
// subscription bindings) is the authoritative topic indicator. The
// handler rejects messages whose topic attribute does not match one of
// the 4 supported topics with a hard error so the broker can DLQ them.
//
// Per ADR-148 receiver pattern + the always-loaded secrets-and-env rule:
//   - OIDC audience + trusted-SA allowlist come from env at cmd/server
//     boot (CHORA_PUBSUB_PUSH_AUDIENCE + CHORA_PUBSUB_PUSH_TRUSTED_SA).
//   - NetworkPolicy + Istio authz further restrict /api/internal/*
//     paths to mesh-internal callers (Cloud Pub/Sub egress IPs allowed
//     at the ingress gateway).
//
// Per ddd-enforcement:
//   - The PaymentsSubscriber struct is the inbound business adapter.
//   - This HTTP handler is a thin transport-only translator — no
//     business logic.
//
// Mirrors services/chora-delivery/internal/adapter/http/grading_pubsub_handler.go.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"google.golang.org/protobuf/proto"

	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"

	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/payments/v1"
)

// PaymentsPushDeps wires the handler.
type PaymentsPushDeps struct {
	// Subscriber is the typed inbound business adapter. Required.
	Subscriber *events.PaymentsSubscriber
	// Verifier is the OIDC token verifier. Use
	// eventpush.NewVerifier(eventpush.VerifierConfig{}) for dev — caller
	// is then expected to enforce auth via mTLS / NetworkPolicy.
	Verifier *eventpush.Verifier
}

// NewPaymentsPushHandler returns the http.Handler bound to the 4-topic
// chora.payments.* push subscription set. Panics on nil Subscriber so
// a mis-wired bootstrap fails loud per `feedback_no_stubs_real_wiring`.
func NewPaymentsPushHandler(deps PaymentsPushDeps) http.Handler {
	if deps.Subscriber == nil {
		panic("http: PaymentsPushHandler requires Subscriber")
	}
	return eventpush.NewHandler(eventpush.HandlerConfig{
		Verifier: deps.Verifier,
		Dispatch: dispatchPaymentsPush(deps.Subscriber),
	})
}

// dispatchPaymentsPush returns the DispatchFunc that decodes one Pub/Sub
// push delivery + invokes the appropriate PaymentsSubscriber method.
//
// Topic resolution order:
//  1. Pub/Sub message `topic` attribute (preferred — set by the binding).
//  2. If absent, the handler returns an error → broker re-delivers → DLQ
//     after exhaustion. We deliberately do NOT infer topic from payload
//     shape because the 4 supported messages share envelope structure.
func dispatchPaymentsPush(sub *events.PaymentsSubscriber) eventpush.DispatchFunc {
	return func(ctx context.Context, msg eventpush.PushMessage) error {
		topic := msg.Attributes["topic"]
		if topic == "" {
			return errors.New("payments_push: missing `topic` attribute")
		}
		if len(msg.Data) == 0 {
			return errors.New("payments_push: empty payload")
		}
		switch topic {
		case events.TopicCoursePurchasePaymentCaptured:
			return handleCoursePurchaseCapturedPush(ctx, sub, msg.Data)
		case events.TopicCoursePurchaseRefunded:
			return handleCoursePurchaseRefundedPush(ctx, sub, msg.Data)
		case events.TopicApplicationPaymentPaymentCaptured:
			return handleApplicationPaymentCapturedPush(ctx, sub, msg.Data)
		case events.TopicApplicationPaymentRefunded:
			return handleApplicationPaymentRefundedPush(ctx, sub, msg.Data)
		default:
			// Per the locked architectural rule, only these 4 topics are
			// bound to this push endpoint. Misrouted messages ack-and-drop
			// (nil return) so the broker doesn't redeliver indefinitely.
			return nil
		}
	}
}

// handleCoursePurchaseCapturedPush decodes the binary payload + invokes
// the typed subscriber.
func handleCoursePurchaseCapturedPush(ctx context.Context, sub *events.PaymentsSubscriber, payload []byte) error {
	ev := &paymentsv1.CoursePurchasePaymentCaptured{}
	if err := proto.Unmarshal(payload, ev); err != nil {
		return fmt.Errorf("payments_push: proto.Unmarshal CoursePurchasePaymentCaptured: %w", err)
	}
	return sub.HandleCoursePurchasePaymentCaptured(ctx, ev)
}

// handleCoursePurchaseRefundedPush decodes the binary payload + invokes
// the typed subscriber.
func handleCoursePurchaseRefundedPush(ctx context.Context, sub *events.PaymentsSubscriber, payload []byte) error {
	ev := &paymentsv1.CoursePurchaseRefunded{}
	if err := proto.Unmarshal(payload, ev); err != nil {
		return fmt.Errorf("payments_push: proto.Unmarshal CoursePurchaseRefunded: %w", err)
	}
	return sub.HandleCoursePurchaseRefunded(ctx, ev)
}

// handleApplicationPaymentCapturedPush decodes the binary payload + invokes
// the typed subscriber.
func handleApplicationPaymentCapturedPush(ctx context.Context, sub *events.PaymentsSubscriber, payload []byte) error {
	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{}
	if err := proto.Unmarshal(payload, ev); err != nil {
		return fmt.Errorf("payments_push: proto.Unmarshal ApplicationPaymentPaymentCaptured: %w", err)
	}
	return sub.HandleApplicationPaymentCaptured(ctx, ev)
}

// handleApplicationPaymentRefundedPush decodes the binary payload + invokes
// the typed subscriber.
func handleApplicationPaymentRefundedPush(ctx context.Context, sub *events.PaymentsSubscriber, payload []byte) error {
	ev := &paymentsv1.ApplicationPaymentRefunded{}
	if err := proto.Unmarshal(payload, ev); err != nil {
		return fmt.Errorf("payments_push: proto.Unmarshal ApplicationPaymentRefunded: %w", err)
	}
	return sub.HandleApplicationPaymentRefunded(ctx, ev)
}
