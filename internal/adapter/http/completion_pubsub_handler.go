// completion_pubsub_handler.go — the push inbox for the auto-issue engine
// (CHO-2157).
//
//	POST /api/internal/pubsub/completion-released-inbox
//	  ← chora.delivery.submission.released.v1
//
// chora-delivery consuming its OWN event: certifications, submissions,
// assessments and offerings all live in chora_delivery, so this is an
// intra-domain projection, exactly like the module-progress inbox next door.
//
// The topic is Schema-Registry BINARY protobuf — decode the real wire format,
// NOT JSON. (A JSON-shaped decode here is how topics dead-letter silently.)
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"
	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
)

// CompletionPushDeps bundles what the completion push endpoint needs.
type CompletionPushDeps struct {
	Verifier   *eventpush.Verifier
	Subscriber *subscribers.CompletionSubscriber
}

// NewCompletionReleasedPushHandler builds the push endpoint for
// chora.delivery.submission.released.v1.
func NewCompletionReleasedPushHandler(deps CompletionPushDeps) http.Handler {
	if deps.Subscriber == nil {
		panic("http: CompletionReleasedPushHandler requires Subscriber")
	}
	return eventpush.NewHandler(eventpush.HandlerConfig{
		Verifier: deps.Verifier,
		Dispatch: dispatchCompletionReleasedInbox(deps.Subscriber),
	})
}

// dispatchCompletionReleasedInbox decodes a submission.released.v1 delivery and
// invokes the completion subscriber.
func dispatchCompletionReleasedInbox(sub *subscribers.CompletionSubscriber) eventpush.DispatchFunc {
	return func(ctx context.Context, msg eventpush.PushMessage) error {
		eventID, tenantID, err := completionEnvelope(msg, subscribers.TopicSubmissionReleased)
		if err != nil {
			return err
		}
		var m deliveryv1.SubmissionReleased
		if err := proto.Unmarshal(msg.Data, &m); err != nil {
			return fmt.Errorf("completion: proto.Unmarshal SubmissionReleased: %w", err)
		}
		gcid := firstNonEmpty(m.GetLearnerGcid(), msg.Attributes["gcid"])
		return sub.Handle(ctx,
			events.EventEnvelope{
				EventID:     eventID,
				TenantID:    tenantID,
				GCID:        gcid,
				Traceparent: strings.TrimSpace(msg.Attributes["traceparent"]),
			},
			subscribers.SubmissionReleasedPayload{
				SubmissionID: strings.TrimSpace(m.GetSubmissionId()),
				AssessmentID: strings.TrimSpace(m.GetAssessmentId()),
				LearnerGCID:  gcid,
				TenantID:     tenantID,
			})
	}
}

// completionEnvelope pulls + validates the required envelope attributes and
// enforces the expected topic — a misroute fails loud (→ DLQ) rather than
// silently issuing a certificate off the wrong event.
func completionEnvelope(msg eventpush.PushMessage, wantTopic string) (string, string, error) {
	if topic := strings.TrimSpace(msg.Attributes["topic"]); topic != "" && topic != wantTopic {
		return "", "", fmt.Errorf("completion: unexpected topic %q (want %q)", topic, wantTopic)
	}
	eventID := strings.TrimSpace(msg.Attributes["event_id"])
	tenantID := strings.TrimSpace(msg.Attributes["tenant_id"])
	if eventID == "" {
		return "", "", errors.New("completion: missing event_id attribute")
	}
	if tenantID == "" {
		return "", "", errors.New("completion: missing tenant_id attribute")
	}
	return eventID, tenantID, nil
}
