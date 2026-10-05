// module_progress_inbox_test.go — the module-progress inbox must dedupe on
// event_id (at-least-once redelivery is a no-op) and propagate resolve/advance
// failures (NACK → redelivery), while validating the envelope.
package subscribers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// tenantCapturingLoader records the tenant present on ctx when Get is called —
// the seam the pg module loader relies on (ModuleRepo.Get reads the tenant from
// ctx for RLS). Guards the regression where the subscriber forgot to stamp it.
type tenantCapturingLoader struct {
	inner    moduleprogress.ModuleLoader
	seenTID  string
	getCalls int
}

func (l *tenantCapturingLoader) Get(ctx context.Context, moduleID string) (*module.Module, bool, error) {
	l.getCalls++
	l.seenTID = tracing.TenantIDFromContext(ctx)
	return l.inner.Get(ctx, moduleID)
}

const (
	mpTenant = "01970000-0000-7000-8000-000000000001"
	mpCourse = "01970000-0000-7000-8000-000000000099"
	mpGCID   = "01970000-0000-7000-8000-000000000abc"
	mpItem   = "01970000-0000-7000-9000-0000000000a1"
)

type scriptedResolver struct {
	targets []moduleprogress.CompletionTarget
	err     error
	calls   int
}

func (s *scriptedResolver) ResolveEnrolledTargets(_ context.Context, _, _, _, _ string) ([]moduleprogress.CompletionTarget, error) {
	s.calls++
	return s.targets, s.err
}

func seedProjector(t *testing.T, res moduleprogress.CompletionResolver) (*moduleprogress.Projector, *moduleprogress.InMemProgressStore, string) {
	t.Helper()
	store := module.NewInMemModuleStore()
	m, err := module.New(module.NewParams{TenantID: mpTenant, CourseID: mpCourse, Title: "M"})
	if err != nil {
		t.Fatalf("module.New: %v", err)
	}
	if _, err := m.AddItem(mpItem); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if _, err := store.Create(context.Background(), m); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	prog := moduleprogress.NewInMemProgressStore()
	return moduleprogress.NewProjector(res, store, prog), prog, m.ID
}

func TestModuleProgressInbox_DedupesOnEventID(t *testing.T) {
	res := &scriptedResolver{}
	proj, prog, moduleID := seedProjector(t, res)
	res.targets = []moduleprogress.CompletionTarget{{ContentItemID: mpItem, ModuleID: moduleID, CourseID: mpCourse}}
	sub := subscribers.NewModuleProgressInboxSubscriber(proj, idempotent.NewMemoryStore())

	if err := sub.Handle(context.Background(), "evt-1", mpTenant, mpGCID, "atom", "ref"); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := sub.Handle(context.Background(), "evt-1", mpTenant, mpGCID, "atom", "ref"); err != nil {
		t.Fatalf("redelivered Handle: %v", err)
	}
	if res.calls != 1 {
		t.Fatalf("same event_id must be deduped before the resolve fan-out; resolver calls = %d", res.calls)
	}
	got, ok, _ := prog.GetByLearnerModule(context.Background(), mpTenant, mpGCID, moduleID)
	if !ok || len(got.CompletedContentItemIDs) != 1 {
		t.Fatalf("expected exactly one recorded completion; got %+v", got)
	}
}

func TestModuleProgressInbox_DistinctEventIDsBothProcess(t *testing.T) {
	res := &scriptedResolver{}
	proj, _, moduleID := seedProjector(t, res)
	res.targets = []moduleprogress.CompletionTarget{{ContentItemID: mpItem, ModuleID: moduleID, CourseID: mpCourse}}
	sub := subscribers.NewModuleProgressInboxSubscriber(proj, idempotent.NewMemoryStore())

	_ = sub.Handle(context.Background(), "evt-1", mpTenant, mpGCID, "atom", "ref")
	_ = sub.Handle(context.Background(), "evt-2", mpTenant, mpGCID, "atom", "ref")
	if res.calls != 2 {
		t.Fatalf("distinct event ids must both process; resolver calls = %d", res.calls)
	}
}

func TestModuleProgressInbox_StampsTenantOnContext(t *testing.T) {
	store := module.NewInMemModuleStore()
	m, err := module.New(module.NewParams{TenantID: mpTenant, CourseID: mpCourse, Title: "M"})
	if err != nil {
		t.Fatalf("module.New: %v", err)
	}
	if _, err := m.AddItem(mpItem); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if _, err := store.Create(context.Background(), m); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	loader := &tenantCapturingLoader{inner: store}
	res := &scriptedResolver{targets: []moduleprogress.CompletionTarget{{ContentItemID: mpItem, ModuleID: m.ID, CourseID: mpCourse}}}
	proj := moduleprogress.NewProjector(res, loader, moduleprogress.NewInMemProgressStore())
	sub := subscribers.NewModuleProgressInboxSubscriber(proj, idempotent.NewMemoryStore())

	if err := sub.Handle(context.Background(), "evt-tid", mpTenant, mpGCID, "atom", "ref"); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if loader.getCalls == 0 {
		t.Fatalf("expected the module loader to be invoked")
	}
	if loader.seenTID != mpTenant {
		t.Fatalf("subscriber must stamp tenant on ctx before the pg module loader reads it (RLS); got %q want %q", loader.seenTID, mpTenant)
	}
}

func TestModuleProgressInbox_ResolveError_Propagates(t *testing.T) {
	res := &scriptedResolver{err: errors.New("db down")}
	proj, _, _ := seedProjector(t, res)
	sub := subscribers.NewModuleProgressInboxSubscriber(proj, idempotent.NewMemoryStore())
	if err := sub.Handle(context.Background(), "evt-err", mpTenant, mpGCID, "atom", "ref"); err == nil {
		t.Fatalf("a resolve failure must propagate (NACK → redelivery), not ack")
	}
}

func TestModuleProgressInbox_MissingEnvelope_Errors(t *testing.T) {
	res := &scriptedResolver{}
	proj, _, _ := seedProjector(t, res)
	sub := subscribers.NewModuleProgressInboxSubscriber(proj, idempotent.NewMemoryStore())
	if err := sub.Handle(context.Background(), "", mpTenant, mpGCID, "atom", "ref"); err == nil {
		t.Fatalf("missing event_id must error")
	}
	if err := sub.Handle(context.Background(), "evt", "", mpGCID, "atom", "ref"); err == nil {
		t.Fatalf("missing tenant_id must error")
	}
}
