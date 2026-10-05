// module_progress_pubsub_handler.go — Pub/Sub push handlers for the W7
// StudentModuleProgress projection (CHO-2074). Two single-topic endpoints (per
// the chora-delivery single-topic-inbox convention), both feeding the same
// ModuleProgressInboxSubscriber:
//
//	POST /api/internal/pubsub/module-progress-atom-inbox
//	   ← chora.consumption.atom_session.completed.v1   (kind "atom",       ref = atom_id)
//	POST /api/internal/pubsub/module-progress-graded-inbox
//	   ← chora.delivery.submission.graded.v1           (kind "assessment", ref = assessment_id)
//
// Wire contract: the envelope rides in Pub/Sub ATTRIBUTES (event_id, tenant_id,
// gcid, topic, ...). The DATA-body encoding differs PER TOPIC and must match the
// producer:
//   - atom   ← chora.consumption.atom_session.completed.v1 — UNSCHEMA'd JSON body
//     (chora-consumption publishes a map[string]any) → json.Unmarshal.
//   - graded ← chora.delivery.submission.graded.v1 — Schema-Registry BINARY
//     protobuf body (deliveryv1.SubmissionGraded) → proto.Unmarshal. A JSON
//     decode here silently DLQ'd every graded event, so the graded projection
//     never advanced; fixed to proto-decode the real wire format.
//
// These handlers are thin transport translators — the subscriber is the inbound
// business adapter (idempotency + projection). OIDC + mesh authz gate the
// /api/internal/* path.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"
	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
)

// ModuleProgressPushDeps wires the handlers.
type ModuleProgressPushDeps struct {
	// Subscriber is the typed inbound business adapter. Required.
	Subscriber *subscribers.ModuleProgressInboxSubscriber
	// Verifier is the OIDC token verifier (shared pushVerifier).
	Verifier *eventpush.Verifier
}

// atomSessionCompletedPayload is the JSON body chora-consumption publishes for
// chora.consumption.atom_session.completed.v1 (see chora-consumption
// me_handlers.go). Only the fields the projection needs are decoded.
type atomSessionCompletedPayload struct {
	AtomID      string `json:"atom_id"`
	LearnerGCID string `json:"learner_gcid"`
}

// NewModuleProgressAtomPushHandler binds the atom_session.completed endpoint.
// Panics on nil Subscriber so a mis-wired bootstrap fails loud.
func NewModuleProgressAtomPushHandler(deps ModuleProgressPushDeps) http.Handler {
	if deps.Subscriber == nil {
		panic("http: ModuleProgressAtomPushHandler requires Subscriber")
	}
	return eventpush.NewHandler(eventpush.HandlerConfig{
		Verifier: deps.Verifier,
		Dispatch: dispatchModuleProgressAtomInbox(deps.Subscriber),
	})
}

// dispatchModuleProgressAtomInbox decodes an atom_session.completed delivery and
// invokes the projection subscriber.
func dispatchModuleProgressAtomInbox(sub *subscribers.ModuleProgressInboxSubscriber) eventpush.DispatchFunc {
	return func(ctx context.Context, msg eventpush.PushMessage) error {
		eventID, tenantID, err := moduleProgressEnvelope(msg, subscribers.TopicAtomSessionCompleted)
		if err != nil {
			return err
		}
		var p atomSessionCompletedPayload
		if err := json.Unmarshal(msg.Data, &p); err != nil {
			return fmt.Errorf("module_progress_atom: json.Unmarshal payload: %w", err)
		}
		gcid := firstNonEmpty(p.LearnerGCID, msg.Attributes["gcid"])
		return sub.Handle(ctx, eventID, tenantID, gcid,
			subscribers.ModuleProgressContentAtom, strings.TrimSpace(p.AtomID))
	}
}

// NewModuleProgressGradedPushHandler binds the submission.graded endpoint.
func NewModuleProgressGradedPushHandler(deps ModuleProgressPushDeps) http.Handler {
	if deps.Subscriber == nil {
		panic("http: ModuleProgressGradedPushHandler requires Subscriber")
	}
	return eventpush.NewHandler(eventpush.HandlerConfig{
		Verifier: deps.Verifier,
		Dispatch: dispatchModuleProgressGradedInbox(deps.Subscriber),
	})
}

// dispatchModuleProgressGradedInbox decodes a submission.graded delivery and
// invokes the projection subscriber.
func dispatchModuleProgressGradedInbox(sub *subscribers.ModuleProgressInboxSubscriber) eventpush.DispatchFunc {
	return func(ctx context.Context, msg eventpush.PushMessage) error {
		eventID, tenantID, err := moduleProgressEnvelope(msg, subscribers.TopicSubmissionGraded)
		if err != nil {
			return err
		}
		// chora.delivery.submission.graded.v1 is a Schema-Registry BINARY
		// protobuf topic — decode the real wire format, NOT JSON.
		var m deliveryv1.SubmissionGraded
		if err := proto.Unmarshal(msg.Data, &m); err != nil {
			return fmt.Errorf("module_progress_graded: proto.Unmarshal SubmissionGraded: %w", err)
		}
		gcid := firstNonEmpty(m.GetLearnerGcid(), msg.Attributes["gcid"])
		return sub.Handle(ctx, eventID, tenantID, gcid,
			subscribers.ModuleProgressContentAssess, strings.TrimSpace(m.GetAssessmentId()))
	}
}

// moduleProgressEnvelope pulls + validates the required envelope attributes and
// enforces the expected topic (a misroute fails loud → DLQ), returning the
// event_id + tenant_id.
func moduleProgressEnvelope(msg eventpush.PushMessage, wantTopic string) (string, string, error) {
	if topic := strings.TrimSpace(msg.Attributes["topic"]); topic != "" && topic != wantTopic {
		return "", "", fmt.Errorf("module_progress: unexpected topic %q (want %q)", topic, wantTopic)
	}
	eventID := strings.TrimSpace(msg.Attributes["event_id"])
	tenantID := strings.TrimSpace(msg.Attributes["tenant_id"])
	if eventID == "" {
		return "", "", errors.New("module_progress: missing event_id attribute")
	}
	if tenantID == "" {
		return "", "", errors.New("module_progress: missing tenant_id attribute")
	}
	if len(msg.Data) == 0 {
		return "", "", errors.New("module_progress: empty payload")
	}
	return eventID, tenantID, nil
}
