// exam_result_pubsub_handler.go - the push inbox for the EXAM-mode auto-cert
// engine (R+ Four-Mode DoD §10.4 step 4).
//
//	POST /api/internal/pubsub/exam-result-released-inbox
//	  ← chora.delivery.exam_result.released.v1
//
// chora-delivery consuming its OWN outcome event: exams, courses and
// certifications all live in chora_delivery, so this is an intra-domain
// projection, exactly like the graduate-mode completion inbox next door.
//
// The topic is Schema-Registry BINARY protobuf - decode the real wire format
// (deliveryv1.ExamResultReleased), NOT JSON. (A JSON-shaped decode here is how
// topics dead-letter silently - the module-progress graded-inbox regression.)
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

// ExamResultPushDeps bundles what the exam-result push endpoint needs.
type ExamResultPushDeps struct {
	Verifier   *eventpush.Verifier
	Subscriber *subscribers.ExamResultCertSubscriber
}

// NewExamResultReleasedPushHandler builds the push endpoint for
// chora.delivery.exam_result.released.v1.
func NewExamResultReleasedPushHandler(deps ExamResultPushDeps) http.Handler {
	if deps.Subscriber == nil {
		panic("http: ExamResultReleasedPushHandler requires Subscriber")
	}
	return eventpush.NewHandler(eventpush.HandlerConfig{
		Verifier: deps.Verifier,
		Dispatch: dispatchExamResultReleasedInbox(deps.Subscriber),
	})
}

// dispatchExamResultReleasedInbox decodes a chora.delivery.exam_result.released.v1
// delivery and invokes the EXAM-mode auto-cert subscriber.
func dispatchExamResultReleasedInbox(sub *subscribers.ExamResultCertSubscriber) eventpush.DispatchFunc {
	return func(ctx context.Context, msg eventpush.PushMessage) error {
		eventID, tenantID, err := examResultEnvelope(msg, sub.SubscribedTopic())
		if err != nil {
			return err
		}
		var m deliveryv1.ExamResultReleased
		if err := proto.Unmarshal(msg.Data, &m); err != nil {
			return fmt.Errorf("exam-cert: proto.Unmarshal ExamResultReleased: %w", err)
		}
		// candidate_ref IS the learner GCID (see the subscriber's header); fall
		// back to the envelope gcid attribute only if the field is absent.
		gcid := firstNonEmpty(strings.TrimSpace(m.GetCandidateRef()), strings.TrimSpace(msg.Attributes["gcid"]))
		return sub.Handle(ctx,
			events.EventEnvelope{
				EventID:     eventID,
				TenantID:    firstNonEmpty(tenantID, strings.TrimSpace(m.GetTenantId())),
				GCID:        gcid,
				Traceparent: strings.TrimSpace(msg.Attributes["traceparent"]),
			},
			subscribers.ExamResultReleasedPayload{
				ResultID:      strings.TrimSpace(m.GetResultId()),
				ExamID:        strings.TrimSpace(m.GetExamId()),
				ExamFormID:    strings.TrimSpace(m.GetExamFormId()),
				CandidateGCID: gcid,
				Outcome:       examOutcomeString(m.GetOutcome()),
				TenantID:      firstNonEmpty(tenantID, strings.TrimSpace(m.GetTenantId())),
			})
	}
}

// examOutcomeString maps the wire enum onto the domain-native verdict string the
// subscriber compares. UNSPECIFIED (the proto3 zero-guard, never emitted) maps
// to "" so it is treated as NOT-PASS and certifies nothing - fail-safe.
func examOutcomeString(o deliveryv1.ExamResultOutcome) string {
	switch o {
	case deliveryv1.ExamResultOutcome_EXAM_RESULT_OUTCOME_PASS:
		return "PASS"
	case deliveryv1.ExamResultOutcome_EXAM_RESULT_OUTCOME_FAIL:
		return "FAIL"
	default:
		return ""
	}
}

// examResultEnvelope pulls + validates the required envelope attributes and
// enforces the expected topic - a misroute fails loud (→ DLQ) rather than
// silently issuing a certificate off the wrong event.
func examResultEnvelope(msg eventpush.PushMessage, wantTopic string) (string, string, error) {
	if topic := strings.TrimSpace(msg.Attributes["topic"]); topic != "" && topic != wantTopic {
		return "", "", fmt.Errorf("exam-cert: unexpected topic %q (want %q)", topic, wantTopic)
	}
	eventID := strings.TrimSpace(msg.Attributes["event_id"])
	tenantID := strings.TrimSpace(msg.Attributes["tenant_id"])
	if eventID == "" {
		return "", "", errors.New("exam-cert: missing event_id attribute")
	}
	if tenantID == "" {
		return "", "", errors.New("exam-cert: missing tenant_id attribute")
	}
	return eventID, tenantID, nil
}
