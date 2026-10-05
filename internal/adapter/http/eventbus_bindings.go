// eventbus_bindings.go — adapters projecting NATS JetStream eventbus deliveries
// onto the inbound subscriber dispatch functions.
//
// The `/api/internal/pubsub/*` HTTP receivers (internal/adapter/eventpush)
// remain as a local compatibility transport, but the canonical inbound
// transport is the event bus. Each adapter reuses the SAME dispatch function
// the HTTP receiver uses, so the two transports cannot drift: eventpush.
// FromEventbus projects an eventbus.Message onto the push-message shape the
// dispatcher already understands (subject → "topic" attribute, envelope fields
// → attributes, payload → data).
package httpapi

import (
	"context"

	"github.com/apollo-chora/chora-common/eventbus"
	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
)

// eventbusFromDispatch adapts an eventpush dispatcher to an eventbus.Handler.
func eventbusFromDispatch(d eventpush.DispatchFunc) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		return d(ctx, eventpush.FromEventbus(msg))
	}
}

// GradingInboxEventbusHandler binds the grading inbox subscriber to the bus.
func GradingInboxEventbusHandler(sub *subscribers.GradingInboxSubscriber) eventbus.Handler {
	return eventbusFromDispatch(dispatchGradingInbox(sub))
}

// PaymentsInboxEventbusHandler binds the 4-topic payments subscriber to the bus.
func PaymentsInboxEventbusHandler(sub *events.PaymentsSubscriber) eventbus.Handler {
	return eventbusFromDispatch(dispatchPaymentsPush(sub))
}

// IdentityProfileInboxEventbusHandler binds the identity-profile projection.
func IdentityProfileInboxEventbusHandler(sub *events.IdentityProfileSubscriber) eventbus.Handler {
	return eventbusFromDispatch(dispatchIdentityProfilePush(sub))
}

// ModuleProgressAtomInboxEventbusHandler binds the atom_session.completed leg.
func ModuleProgressAtomInboxEventbusHandler(sub *subscribers.ModuleProgressInboxSubscriber) eventbus.Handler {
	return eventbusFromDispatch(dispatchModuleProgressAtomInbox(sub))
}

// ModuleProgressGradedInboxEventbusHandler binds the submission.graded leg.
func ModuleProgressGradedInboxEventbusHandler(sub *subscribers.ModuleProgressInboxSubscriber) eventbus.Handler {
	return eventbusFromDispatch(dispatchModuleProgressGradedInbox(sub))
}

// CourseProgressAdvancedInboxEventbusHandler binds the learning_path.advanced leg.
func CourseProgressAdvancedInboxEventbusHandler(sub *subscribers.CourseProgressSubscriber) eventbus.Handler {
	return eventbusFromDispatch(dispatchCourseProgressAdvancedInbox(sub))
}

// CourseProgressCompletedInboxEventbusHandler binds the learning_path.completed leg.
func CourseProgressCompletedInboxEventbusHandler(sub *subscribers.CourseProgressSubscriber) eventbus.Handler {
	return eventbusFromDispatch(dispatchCourseProgressCompletedInbox(sub))
}

// BatchTestSetInboxEventbusHandler binds the question_batch.accepted subscriber.
func BatchTestSetInboxEventbusHandler(sub *subscribers.BatchTestSetSubscriber) eventbus.Handler {
	return eventbusFromDispatch(dispatchBatchTestSetInbox(sub))
}

// CompletionReleasedInboxEventbusHandler binds the submission.released subscriber.
func CompletionReleasedInboxEventbusHandler(sub *subscribers.CompletionSubscriber) eventbus.Handler {
	return eventbusFromDispatch(dispatchCompletionReleasedInbox(sub))
}

// ExamResultReleasedInboxEventbusHandler binds the exam_result.released subscriber.
func ExamResultReleasedInboxEventbusHandler(sub *subscribers.ExamResultCertSubscriber) eventbus.Handler {
	return eventbusFromDispatch(dispatchExamResultReleasedInbox(sub))
}
