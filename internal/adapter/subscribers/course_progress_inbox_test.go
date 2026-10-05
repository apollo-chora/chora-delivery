// course_progress_inbox_test.go - RED-first specification of the ASYNC-analytics
// progress projector (R+ Four-Mode DoD §10.3, epic CHO-1827).
//
// Every test drives Handle with a BARE context.Background(), exactly as the
// Pub/Sub push handler does in production. A test that builds its own
// tenant-carrying ctx would supply what prod does not and go green over a
// subscriber that 500s on every real message (the CHO-2184 lesson: 12 green
// live-DB tests over a repo that failed RLS in prod).
package subscribers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	courseprogress "github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
)

const (
	cpiTenant = "11111111-1111-7111-8111-111111111111"
	cpiGCID   = "00000000-0000-7000-9000-0000000000a1"
	cpiCourse = "01985e7f-5555-7abc-8def-000000000c01"
	cpiPath   = "01985e7f-5555-7abc-8def-000000000d01"
)

var cpiT0 = time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)

// -----------------------------------------------------------------------------
// Test doubles
// -----------------------------------------------------------------------------

// recordingPort wraps the real in-memory store and captures the ctx it was
// called with, so a test can prove the tenant reached the RLS boundary.
type recordingPort struct {
	*courseprogress.InMemProgressStore
	lastCtx     context.Context
	advanceErr  error
	completeErr error
	calls       int
}

func newRecordingPort() *recordingPort {
	return &recordingPort{InMemProgressStore: courseprogress.NewInMemProgressStore()}
}

func (p *recordingPort) Advance(ctx context.Context, tenantID, gcid, courseID, pathID string, completed, total int, at time.Time) (bool, error) {
	p.lastCtx, p.calls = ctx, p.calls+1
	if p.advanceErr != nil {
		return false, p.advanceErr
	}
	return p.InMemProgressStore.Advance(ctx, tenantID, gcid, courseID, pathID, completed, total, at)
}

func (p *recordingPort) Complete(ctx context.Context, tenantID, gcid, courseID, pathID string, at time.Time) (bool, error) {
	p.lastCtx, p.calls = ctx, p.calls+1
	if p.completeErr != nil {
		return false, p.completeErr
	}
	return p.InMemProgressStore.Complete(ctx, tenantID, gcid, courseID, pathID, at)
}

func newCPSub(port courseprogress.ProgressPort) *subscribers.CourseProgressSubscriber {
	return subscribers.NewCourseProgressSubscriber(port, nil)
}

func cpiEnv(eventID string) events.EventEnvelope {
	return events.EventEnvelope{EventID: eventID, TenantID: cpiTenant, GCID: cpiGCID}
}

func advPayload() subscribers.LearningPathAdvancedPayload {
	return subscribers.LearningPathAdvancedPayload{
		PathID: cpiPath, CourseID: cpiCourse, LearnerGCID: cpiGCID, TenantID: cpiTenant,
		CurrentIndex: 3, TotalAtoms: 10, OccurredAt: cpiT0,
	}
}

// storedFraction reads the projected row back - assert the EFFECT, not the call.
func storedFraction(t *testing.T, port *recordingPort) (float64, bool) {
	t.Helper()
	p, ok, err := port.GetByLearnerCourse(context.Background(), cpiTenant, cpiGCID, cpiCourse)
	if err != nil {
		t.Fatalf("GetByLearnerCourse: %v", err)
	}
	if !ok {
		return 0, false
	}
	return p.ProgressFraction(), true
}

// -----------------------------------------------------------------------------
// The projection actually lands
// -----------------------------------------------------------------------------

func TestCourseProgressInbox_AdvanceProjectsProgress(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-1"), advPayload()); err != nil {
		t.Fatalf("HandleAdvance: %v", err)
	}
	got, ok := storedFraction(t, port)
	if !ok {
		t.Fatal("no projection row was written")
	}
	if got != 0.3 {
		t.Fatalf("projected progress: want 0.3 (3/10), got %v", got)
	}
}

func TestCourseProgressInbox_CompletionMarksComplete(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	err := sub.HandleCompletion(context.Background(), cpiEnv("evt-2"), subscribers.LearningPathCompletedPayload{
		PathID: cpiPath, CourseID: cpiCourse, LearnerGCID: cpiGCID, TenantID: cpiTenant, OccurredAt: cpiT0,
	})
	if err != nil {
		t.Fatalf("HandleCompletion: %v", err)
	}
	p, ok, err := port.GetByLearnerCourse(context.Background(), cpiTenant, cpiGCID, cpiCourse)
	if err != nil || !ok {
		t.Fatalf("projection missing: ok=%v err=%v", ok, err)
	}
	if !p.IsComplete {
		t.Fatal("is_complete: want true after learning_path.completed.v1")
	}
	if p.CompletedAt == nil || !p.CompletedAt.Equal(cpiT0) {
		t.Fatalf("completed_at: want %v, got %v", cpiT0, p.CompletedAt)
	}
}

// -----------------------------------------------------------------------------
// THE RLS TRAP: the tenant must be on the CONTEXT before any write
// -----------------------------------------------------------------------------

func TestCourseProgressInbox_PutsTenantOnContext(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	// A bare ctx - precisely what pubsubpush hands the dispatcher.
	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-3"), advPayload()); err != nil {
		t.Fatalf("HandleAdvance: %v", err)
	}
	if port.lastCtx == nil {
		t.Fatal("port was never called")
	}
	if got := tracing.TenantIDFromContext(port.lastCtx); got != cpiTenant {
		t.Fatalf("tenant on ctx: want %q, got %q - rls.ApplySession reads the tenant from "+
			"the CONTEXT, so without it every write is a SILENT no-op (0 rows, no error)", cpiTenant, got)
	}
}

func TestCourseProgressInbox_CompletionPutsTenantOnContext(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	err := sub.HandleCompletion(context.Background(), cpiEnv("evt-4"), subscribers.LearningPathCompletedPayload{
		PathID: cpiPath, CourseID: cpiCourse, LearnerGCID: cpiGCID, TenantID: cpiTenant, OccurredAt: cpiT0,
	})
	if err != nil {
		t.Fatalf("HandleCompletion: %v", err)
	}
	if got := tracing.TenantIDFromContext(port.lastCtx); got != cpiTenant {
		t.Fatalf("tenant on ctx: want %q, got %q", cpiTenant, got)
	}
}

// The tenant may arrive on the envelope rather than the payload.
func TestCourseProgressInbox_FallsBackToEnvelopeTenant(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	p := advPayload()
	p.TenantID = "" // absent from the payload
	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-5"), p); err != nil {
		t.Fatalf("HandleAdvance: %v", err)
	}
	if got := tracing.TenantIDFromContext(port.lastCtx); got != cpiTenant {
		t.Fatalf("tenant on ctx from envelope: want %q, got %q", cpiTenant, got)
	}
}

// -----------------------------------------------------------------------------
// Not every LearningPath is delivery-bound
// -----------------------------------------------------------------------------

// A collection-derived / study-list path carries NO course_id (only paths
// bootstrapped from chora.delivery.enrollment.created.v1 do). It is not an error
// and retrying will never give it a course: ACK and write nothing.
func TestCourseProgressInbox_SkipsPathWithoutCourse(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	p := advPayload()
	p.CourseID = ""
	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-6"), p); err != nil {
		t.Fatalf("a course-less path must ACK, not error: %v", err)
	}
	if port.calls != 0 {
		t.Fatalf("a course-less path must not touch the projection, got %d calls", port.calls)
	}
}

func TestCourseProgressInbox_SkipsCompletionWithoutCourse(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	err := sub.HandleCompletion(context.Background(), cpiEnv("evt-7"), subscribers.LearningPathCompletedPayload{
		PathID: cpiPath, LearnerGCID: cpiGCID, TenantID: cpiTenant, OccurredAt: cpiT0,
	})
	if err != nil {
		t.Fatalf("a course-less completion must ACK, not error: %v", err)
	}
	if port.calls != 0 {
		t.Fatalf("want 0 projection calls, got %d", port.calls)
	}
}

// -----------------------------------------------------------------------------
// Fail-loud on a malformed envelope (→ NACK → DLQ, never a silent drop)
// -----------------------------------------------------------------------------

func TestCourseProgressInbox_RequiresTenant(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	p := advPayload()
	p.TenantID = ""
	env := cpiEnv("evt-8")
	env.TenantID = ""
	if err := sub.HandleAdvance(context.Background(), env, p); err == nil {
		t.Fatal("missing tenant_id must NACK (error), got nil")
	}
}

func TestCourseProgressInbox_RequiresLearnerGCID(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	p := advPayload()
	p.LearnerGCID = ""
	env := cpiEnv("evt-9")
	env.GCID = ""
	if err := sub.HandleAdvance(context.Background(), env, p); err == nil {
		t.Fatal("missing learner_gcid must NACK (error), got nil")
	}
}

func TestCourseProgressInbox_RequiresEventID(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	if err := sub.HandleAdvance(context.Background(), cpiEnv(""), advPayload()); err == nil {
		t.Fatal("missing event_id must NACK (error), got nil")
	}
}

func TestCourseProgressInbox_UnwiredPortFailsLoud(t *testing.T) {
	sub := newCPSub(nil)
	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-10"), advPayload()); err == nil {
		t.Fatal("an unwired port must fail loud, got nil")
	}
}

// -----------------------------------------------------------------------------
// ACK vs NACK asymmetry
// -----------------------------------------------------------------------------

// A transient infra failure MUST NACK: the DLQ keeps the message replayable.
func TestCourseProgressInbox_TransientErrorNacks(t *testing.T) {
	port := newRecordingPort()
	port.advanceErr = errors.New("connection reset by peer")
	sub := newCPSub(port)

	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-11"), advPayload()); err == nil {
		t.Fatal("a transient port failure must NACK (error), got nil - an ACK would destroy the event")
	}
}

// A SQLSTATE class 22 fault is PROVEN unretryable: the same bytes will be
// refused identically forever. ACK it (loudly) so the DLQ keeps meaning
// "replayable" - the pg_error_class contract.
func TestCourseProgressInbox_PermanentDataFaultAcks(t *testing.T) {
	port := newRecordingPort()
	port.advanceErr = &pgconn.PgError{Code: "22P02", Message: "invalid input syntax for type uuid"}
	sub := newCPSub(port)

	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-12"), advPayload()); err != nil {
		t.Fatalf("a permanent data fault must ACK (nil), got %v", err)
	}
}

// Class 23 (integrity violation) is NOT proven permanent - NACK, per the
// deliberately conservative classifier.
func TestCourseProgressInbox_UnrecognisedPGClassNacks(t *testing.T) {
	port := newRecordingPort()
	port.advanceErr = &pgconn.PgError{Code: "23505", Message: "duplicate key"}
	sub := newCPSub(port)

	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-13"), advPayload()); err == nil {
		t.Fatal("an unrecognised SQLSTATE class must NACK, got nil")
	}
}

// -----------------------------------------------------------------------------
// Idempotency
// -----------------------------------------------------------------------------

func TestCourseProgressInbox_DedupesRedelivery(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	for i := 0; i < 3; i++ {
		if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-same"), advPayload()); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	if port.calls != 1 {
		t.Fatalf("redelivery of one event_id must reach the port once, got %d", port.calls)
	}
	got, _ := storedFraction(t, port)
	if got != 0.3 {
		t.Fatalf("progress after redelivery: want 0.3, got %v", got)
	}
}

// A stale advance arriving after a newer one must not rewind the projection -
// the durable end-to-end guard, asserted on the stored EFFECT.
func TestCourseProgressInbox_StaleAdvanceDoesNotRewind(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)

	newer := advPayload()
	newer.CurrentIndex, newer.OccurredAt = 8, cpiT0.Add(time.Minute)
	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-new"), newer); err != nil {
		t.Fatalf("newer: %v", err)
	}
	older := advPayload()
	older.CurrentIndex, older.OccurredAt = 2, cpiT0
	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-old"), older); err != nil {
		t.Fatalf("older: %v", err)
	}
	got, _ := storedFraction(t, port)
	if got != 0.8 {
		t.Fatalf("a stale event REWOUND progress: want 0.8, got %v", got)
	}
}

func TestCourseProgressInbox_SubscribedTopicsAreTheConsumptionPair(t *testing.T) {
	got := newCPSub(newRecordingPort()).SubscribedTopics()
	want := map[string]bool{
		"chora.consumption.learning_path.advanced.v1":  true,
		"chora.consumption.learning_path.completed.v1": true,
	}
	if len(got) != len(want) {
		t.Fatalf("SubscribedTopics: want %d topics, got %v", len(want), got)
	}
	for _, tp := range got {
		if !want[tp] {
			t.Fatalf("unexpected topic %q", tp)
		}
	}
}
