// module_progress_inbox.go — W7 StudentModuleProgress projection subscriber
// (CHO-2074). Consumes completion events and advances per-learner, per-module
// progress via the moduleprogress.Projector:
//
//	chora.consumption.atom_session.completed.v1  → kind "atom"       (ref = atom_id)
//	chora.delivery.submission.graded.v1          → kind "assessment" (ref = assessment_id)
//
// Both map to the SAME projector; the two HTTP push endpoints (atom + graded)
// translate their distinct payloads into (kind, ref) then call Handle. The
// projector resolves the enrolled module targets and folds the completion in.
//
// Idempotency: keyed on (event_id) via idempotent.Store — Pub/Sub at-least-once
// means a completion event MAY be redelivered; the inbox short-circuits the
// second arrival, and the aggregate itself no-ops an already-recorded item, so
// double delivery never double-counts. The projector's RecordCompletion is
// itself idempotent, but the inbox dedupe avoids re-running the resolve + advance
// query fan-out on every redelivery.
package subscribers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/tracing"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// Completion topics the module-progress projection binds to (one push
// subscription per topic → one HTTP endpoint each, per the chora-delivery
// single-topic-inbox convention).
const (
	TopicAtomSessionCompleted   = "chora.consumption.atom_session.completed.v1"
	TopicSubmissionGraded       = "chora.delivery.submission.graded.v1"
	ModuleProgressInboxTTL      = 24 * time.Hour
	ModuleProgressContentAtom   = "atom"
	ModuleProgressContentAssess = "assessment"
)

// ModuleProgressInboxSubscriber advances StudentModuleProgress on a content-item
// completion, with event_id idempotency.
type ModuleProgressInboxSubscriber struct {
	projector *moduleprogress.Projector
	inbox     idempotent.Store
	ttl       time.Duration
}

// NewModuleProgressInboxSubscriber wires the projector + idempotency store.
// inbox is OPTIONAL — nil triggers a MemoryStore fallback for dev / unit tests;
// production SHOULD pass a Postgres-backed store so dedupe survives pod restart.
func NewModuleProgressInboxSubscriber(projector *moduleprogress.Projector, inbox idempotent.Store) *ModuleProgressInboxSubscriber {
	if inbox == nil {
		inbox = idempotent.NewMemoryStore()
	}
	return &ModuleProgressInboxSubscriber{projector: projector, inbox: inbox, ttl: ModuleProgressInboxTTL}
}

// Handle dedupes on eventID then advances module progress for the learner's
// completion of (kind, ref). It stamps the tenant onto ctx (tracing.WithTenantID)
// so EVERY downstream pg call — the module loader (module.ModulePort.Get reads
// the tenant from ctx), the resolver, the progress repo, and the idempotency
// store — runs under the right RLS session. Returns an error (Nack → redelivery)
// on any resolve/advance failure; nil (ack) on success or a duplicate.
func (s *ModuleProgressInboxSubscriber) Handle(ctx context.Context, eventID, tenantID, gcid, kind, ref string) error {
	if s == nil || s.projector == nil {
		return errors.New("subscribers: nil module-progress inbox")
	}
	if strings.TrimSpace(eventID) == "" {
		return errors.New("subscribers: module-progress inbox event_id required")
	}
	if strings.TrimSpace(tenantID) == "" {
		return errors.New("subscribers: module-progress inbox tenant_id required")
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	// gcid / kind / ref are validated by the projector (ErrInvalidArgument →
	// NACK so a malformed delivery lands in the DLQ rather than silently acking).
	return s.inbox.Process(ctx, "module_progress:"+eventID, s.ttl, func() error {
		_, err := s.projector.RecordCompletion(ctx, tenantID, gcid, kind, ref)
		return err
	})
}
