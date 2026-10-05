// course_progress_inbox.go - the ASYNC-mode analytics projector (R+ Four-Mode
// DoD keystone, docs/R-PLUS-FOUR-MODE-DELIVERY-REFACTOR-2026-06-24.md §10.3
// step 3: "R+ Analytics reflects the enrolment/progress").
//
// # The gap this closes
//
// R+ Analytics for a self-paced (async) offering reflected ENROLMENT ONLY.
// avg_progress_pct and completion_rate were dead, and the handler said why:
// learner progress lives in chora_consumption, Analytics lives in
// chora_delivery, and a cross-DB query is FORBIDDEN. So an instructor could see
// who signed up and nothing about whether a single one of them was learning.
//
// The only legal bridge is an event. This is the delivery-side consumer of one
// that ALREADY EXISTS - no new topic was coined:
//
//	chora.consumption.learning_path.advanced.v1   → current_index / total_atoms
//	chora.consumption.learning_path.completed.v1  → the completion + its stamp
//
// # Why these two topics and not the module lane next door
//
// chora-delivery already consumes chora.consumption.atom_session.completed.v1
// into student_module_progress (module_progress_inbox.go). That lane cannot
// serve async: its resolver joins course_module_items ⋈ course_modules, and an
// async product HAS no modules (the async workspace tab set is Overview ·
// Publish/Catalog-handoff · Analytics - there is no Curriculum tab to build
// them). Rolling async analytics off it would have shipped a metric that reads 0
// forever and looks exactly like "nobody is learning" - a gate keyed on rows
// nothing writes cannot fire. The learning_path pair needs no module structure,
// only atoms, which is what a self-paced product has.
//
// # Direction
//
// consumption → delivery. Note chora.delivery.module_progress.completed.v1 flows
// the OTHER way (delivery emits it, consumption's Wave-1 XP leg consumes it) and
// is therefore useless as a source here.
//
// # What crosses the wire
//
// course_id is on the payload because chora.delivery.enrollment.created.v1 put
// it there: consumption's BootstrapFromEnrollment stamps CourseID onto the
// LearningPath as the delivery BINDING. A path WITHOUT a course (a study list,
// or collection-derived traversal) is self-paced discovery outside delivery and
// is skipped - see handleCourseless.
//
// # Idempotency
//
// The inbox dedupe key guards broker RETRIES; the DURABLE, cross-pod guarantee
// is course_learner_progress' UNIQUE(tenant_id, gcid, course_id) plus the
// aggregate's own producer-clock watermark, which together mean a redelivered or
// REORDERED advance can never fork or rewind a learner's progress.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	courseprogress "github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
)

// The consumption topics this projection binds to. One inbox, two topics,
// dispatched on the topic attribute - the shape chora-consumption's own
// learning-path-topics inbox already uses for this same pair.
const (
	TopicLearningPathAdvanced  = "chora.consumption.learning_path.advanced.v1"
	TopicLearningPathCompleted = "chora.consumption.learning_path.completed.v1"

	// courseProgressInboxTTL is the dedupe-key retention for broker retries. The
	// durable guarantee is the UNIQUE index + the aggregate watermark, which hold
	// forever and across pods; this only suppresses a same-message retry storm.
	courseProgressInboxTTL = 24 * time.Hour
)

// LearningPathAdvancedPayload is the slice of learning_path.advanced.v1 the
// projection needs.
//
// CurrentIndex/TotalAtoms are taken rather than the payload's `progress_percent`
// on purpose: that field is MISNAMED at the source - LearningPath.ProgressPercent()
// returns a FRACTION in [0.0,1.0], not a percent - so a consumer that trusted the
// name would report 0.42% for a learner who is 42% done. The integers carry the
// same fact with no trap, and let the roll-up divide exactly once. (This is also
// what consumption's own advanced.v1 consumer reads.)
type LearningPathAdvancedPayload struct {
	PathID      string
	CourseID    string
	LearnerGCID string
	TenantID    string
	AtomID      string
	// CurrentIndex is the learner's cursor = the number of atoms completed.
	CurrentIndex int
	// TotalAtoms is the path's atom count. It can GROW (atoms are appended
	// retroactively as creation events arrive), so it is carried per-event
	// rather than assumed fixed.
	TotalAtoms int
	OccurredAt time.Time
}

// LearningPathCompletedPayload is the slice of learning_path.completed.v1 the
// projection needs. The atom count is not read: completion is the fact, and the
// aggregate treats a complete path as fully traversed regardless of counters.
type LearningPathCompletedPayload struct {
	PathID      string
	CourseID    string
	LearnerGCID string
	TenantID    string
	OccurredAt  time.Time
}

// CourseProgressSubscriber projects a learner's LearningPath traversal onto the
// delivery-side CourseLearnerProgress read model that R+ Analytics rolls up.
type CourseProgressSubscriber struct {
	progress courseprogress.ProgressPort
	inbox    idempotent.Store
}

// NewCourseProgressSubscriber constructs the projector. A nil inbox falls back to
// an in-process memory store (broker-retry dedupe only; the durable guarantee is
// the DB UNIQUE index + the aggregate watermark).
func NewCourseProgressSubscriber(progress courseprogress.ProgressPort, inbox idempotent.Store) *CourseProgressSubscriber {
	if inbox == nil {
		inbox = idempotent.NewMemoryStore()
	}
	return &CourseProgressSubscriber{progress: progress, inbox: inbox}
}

// SubscribedTopics reports the topics this subscriber rides.
func (s *CourseProgressSubscriber) SubscribedTopics() []string {
	return []string{TopicLearningPathAdvanced, TopicLearningPathCompleted}
}

// scope validates the envelope + resolves the RLS identity, returning the ctx to
// use for the write.
//
// The tenant goes on the CONTEXT before anything touches the DB: rls.ApplySession
// reads it from there, not from a SQL argument, and a Pub/Sub push handler hands
// us a BARE ctx. Without this line every write is a SILENT no-op against the
// FORCE-RLS table - 0 rows, no error - and the Analytics tab would keep showing
// enrolment-only while looking perfectly healthy.
func (s *CourseProgressSubscriber) scope(ctx context.Context, eventID, payloadTenant, envTenant, payloadGCID, envGCID string) (context.Context, string, string, error) {
	if s == nil || s.progress == nil {
		return nil, "", "", errors.New("subscribers: course-progress projection not wired")
	}
	if strings.TrimSpace(eventID) == "" {
		return nil, "", "", errors.New("subscribers: course-progress inbox event_id required")
	}
	tenantID := firstNonBlank(payloadTenant, envTenant)
	if tenantID == "" {
		return nil, "", "", errors.New("subscribers: course-progress inbox tenant_id required")
	}
	gcid := firstNonBlank(payloadGCID, envGCID)
	if gcid == "" {
		return nil, "", "", errors.New("subscribers: course-progress inbox learner_gcid required")
	}
	return tracing.WithTenantID(ctx, tenantID), tenantID, gcid, nil
}

// HandleAdvance folds one learning_path.advanced.v1 into the projection.
func (s *CourseProgressSubscriber) HandleAdvance(ctx context.Context, env events.EventEnvelope, p LearningPathAdvancedPayload) error {
	ctx, tenantID, gcid, err := s.scope(ctx, env.EventID, p.TenantID, env.TenantID, p.LearnerGCID, env.GCID)
	if err != nil {
		return err
	}
	courseID := strings.TrimSpace(p.CourseID)
	if courseID == "" {
		return handleCourseless("advance", p.PathID, gcid)
	}
	return s.inbox.Process(ctx, "course_progress_adv:"+env.EventID, courseProgressInboxTTL, func() error {
		_, err := s.progress.Advance(ctx, tenantID, gcid, courseID, strings.TrimSpace(p.PathID),
			p.CurrentIndex, p.TotalAtoms, occurredOrNow(p.OccurredAt))
		return s.classify(err, "advance", gcid, courseID, p.PathID)
	})
}

// HandleCompletion folds one learning_path.completed.v1 into the projection.
func (s *CourseProgressSubscriber) HandleCompletion(ctx context.Context, env events.EventEnvelope, p LearningPathCompletedPayload) error {
	ctx, tenantID, gcid, err := s.scope(ctx, env.EventID, p.TenantID, env.TenantID, p.LearnerGCID, env.GCID)
	if err != nil {
		return err
	}
	courseID := strings.TrimSpace(p.CourseID)
	if courseID == "" {
		return handleCourseless("completion", p.PathID, gcid)
	}
	return s.inbox.Process(ctx, "course_progress_done:"+env.EventID, courseProgressInboxTTL, func() error {
		_, err := s.progress.Complete(ctx, tenantID, gcid, courseID, strings.TrimSpace(p.PathID),
			occurredOrNow(p.OccurredAt))
		return s.classify(err, "completion", gcid, courseID, p.PathID)
	})
}

// classify decides ACK vs NACK on a write failure.
//
// ACK and NACK are NOT symmetric. A NACK is reversible (redelivery, then a DLQ a
// human can replay). An ACK is final. So: ACK only what is PROVEN unretryable -
// a SQLSTATE class 22 data fault, whose next delivery carries the identical
// bytes into the identical refusal - and NACK everything else, including
// anything unrecognised. Never silently: the ACK path logs the row that will
// never land and says what to do about it.
func (s *CourseProgressSubscriber) classify(err error, leg, gcid, courseID, pathID string) error {
	if err == nil {
		return nil
	}
	if pgErr, permanent := permanentPGDataFault(err); permanent {
		log.Printf("course-progress: PERMANENT DATA FAULT - %s NOT projected, NOT retried (acked) - "+
			"learner=%s course_id=%q path=%s: postgres refused the row with SQLSTATE %s: %s. "+
			"course_learner_progress.course_id/gcid are UUID NOT NULL, so a non-UUID id on the wire can "+
			"never insert and every redelivery would carry the same value into the same refusal. "+
			"ACTION: this learner's progress will NOT appear in R+ Analytics until the source value is "+
			"fixed and the event replayed - it will NOT arrive on its own.",
			leg, gcid, courseID, pathID, pgErr.Code, pgErr.Message)
		return nil
	}
	return fmt.Errorf("subscribers: project course progress (%s) learner=%s course=%s: %w", leg, gcid, courseID, err)
}

// handleCourseless ACKs an event for a LearningPath that carries no course.
//
// This is a legitimate, PERMANENT non-target, not a failure: only paths
// bootstrapped from chora.delivery.enrollment.created.v1 carry a CourseID, and a
// study list or collection-derived traversal never will. Retrying cannot give it
// one, so NACKing would dead-letter a message that is working as designed and
// bury the real DLQ signal under noise. Logged at debug volume by being logged
// once per event, not silently dropped.
func handleCourseless(leg, pathID, gcid string) error {
	log.Printf("course-progress: skipping %s for path=%s learner=%s - the path carries no course_id, "+
		"so it is self-paced traversal outside any delivery offering (not a delivery target)", leg, pathID, gcid)
	return nil
}

// occurredOrNow defends the watermark against an absent producer clock. A zero
// occurred_at would sort BEFORE every stored watermark and so would be discarded
// as stale forever; treating it as now keeps the event applyable while staying
// honest that the ordering information was not supplied.
func occurredOrNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

// firstNonBlank returns the first non-blank, space-trimmed value.
func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
