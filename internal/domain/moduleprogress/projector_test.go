// projector_test.go — unit tests for the Projector: resolve enrolled targets →
// advance per-module progress. Uses the real module.InMemModuleStore as the
// ModuleLoader, the InMemProgressStore, and a scripted CompletionResolver.
package moduleprogress_test

import (
	"context"
	"errors"
	"testing"

	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// fakeResolver returns scripted targets (and an optional error) regardless of
// input — the projector's resolver seam under test.
type fakeResolver struct {
	targets []moduleprogress.CompletionTarget
	err     error
	calls   int
}

func (f *fakeResolver) ResolveEnrolledTargets(_ context.Context, _, _, _, _ string) ([]moduleprogress.CompletionTarget, error) {
	f.calls++
	return f.targets, f.err
}

// seedModule creates a persisted module (with items) in a fresh InMemModuleStore
// and returns the store + module.
func seedModule(t *testing.T, ids ...string) (*module.InMemModuleStore, *module.Module) {
	t.Helper()
	store := module.NewInMemModuleStore()
	m := buildModule(t, ids...)
	if _, err := store.Create(context.Background(), m); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	return store, m
}

func TestProjector_RecordCompletion_AdvancesEnrolledModule(t *testing.T) {
	t.Parallel()
	store, m := seedModule(t, cidA, cidB) // all_items over 2
	res := &fakeResolver{targets: []moduleprogress.CompletionTarget{
		{ContentItemID: cidA, ModuleID: m.ID, CourseID: tCourse},
	}}
	prog := moduleprogress.NewInMemProgressStore()
	proj := moduleprogress.NewProjector(res, store, prog)

	n, err := proj.RecordCompletion(context.Background(), tTenant, tGCID, "atom", "atom-ref")
	if err != nil {
		t.Fatalf("RecordCompletion: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 changed row; got %d", n)
	}
	got, ok, _ := prog.GetByLearnerModule(context.Background(), tTenant, tGCID, m.ID)
	if !ok {
		t.Fatalf("expected a progress row to be created")
	}
	if got.IsComplete {
		t.Fatalf("all_items over 2 must be incomplete after 1")
	}
	if len(got.CompletedContentItemIDs) != 1 {
		t.Fatalf("expected 1 completed item; got %d", len(got.CompletedContentItemIDs))
	}
}

func TestProjector_RecordCompletion_CompletesModule(t *testing.T) {
	t.Parallel()
	store, m := seedModule(t, cidA) // all_items over 1 → completing cidA completes it
	res := &fakeResolver{targets: []moduleprogress.CompletionTarget{
		{ContentItemID: cidA, ModuleID: m.ID, CourseID: tCourse},
	}}
	prog := moduleprogress.NewInMemProgressStore()
	proj := moduleprogress.NewProjector(res, store, prog)

	if _, err := proj.RecordCompletion(context.Background(), tTenant, tGCID, "atom", "atom-ref"); err != nil {
		t.Fatalf("RecordCompletion: %v", err)
	}
	got, _, _ := prog.GetByLearnerModule(context.Background(), tTenant, tGCID, m.ID)
	if got == nil || !got.IsComplete || got.CompletedAt == nil {
		t.Fatalf("single-item all_items must be complete with completed_at set: %+v", got)
	}
}

func TestProjector_RecordCompletion_Idempotent(t *testing.T) {
	t.Parallel()
	store, m := seedModule(t, cidA, cidB)
	res := &fakeResolver{targets: []moduleprogress.CompletionTarget{
		{ContentItemID: cidA, ModuleID: m.ID, CourseID: tCourse},
	}}
	prog := moduleprogress.NewInMemProgressStore()
	proj := moduleprogress.NewProjector(res, store, prog)

	if n, _ := proj.RecordCompletion(context.Background(), tTenant, tGCID, "atom", "r"); n != 1 {
		t.Fatalf("first call must change 1 row; got %d", n)
	}
	n, err := proj.RecordCompletion(context.Background(), tTenant, tGCID, "atom", "r")
	if err != nil {
		t.Fatalf("second RecordCompletion: %v", err)
	}
	if n != 0 {
		t.Fatalf("re-recording the same item must change 0 rows; got %d", n)
	}
}

func TestProjector_RecordCompletion_NoTargets_NoRows(t *testing.T) {
	t.Parallel()
	store := module.NewInMemModuleStore()
	res := &fakeResolver{targets: nil} // content in no enrolled module
	prog := moduleprogress.NewInMemProgressStore()
	proj := moduleprogress.NewProjector(res, store, prog)

	n, err := proj.RecordCompletion(context.Background(), tTenant, tGCID, "atom", "r")
	if err != nil {
		t.Fatalf("RecordCompletion: %v", err)
	}
	if n != 0 {
		t.Fatalf("no targets must change 0 rows; got %d", n)
	}
	rows, _ := prog.ListByModuleIDs(context.Background(), tTenant, []string{"anything"})
	if len(rows) != 0 {
		t.Fatalf("no progress rows must be written; got %d", len(rows))
	}
}

func TestProjector_RecordCompletion_ModuleVanished_Skips(t *testing.T) {
	t.Parallel()
	emptyStore := module.NewInMemModuleStore() // module loader that has NO modules
	res := &fakeResolver{targets: []moduleprogress.CompletionTarget{
		{ContentItemID: cidA, ModuleID: "01970000-0000-7000-8000-0000000000de", CourseID: tCourse},
	}}
	prog := moduleprogress.NewInMemProgressStore()
	proj := moduleprogress.NewProjector(res, emptyStore, prog)

	n, err := proj.RecordCompletion(context.Background(), tTenant, tGCID, "atom", "r")
	if err != nil {
		t.Fatalf("a soft-deleted/absent module must be skipped, not error: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 changed rows; got %d", n)
	}
}

func TestProjector_RecordCompletion_ResolverError_Propagates(t *testing.T) {
	t.Parallel()
	store, _ := seedModule(t, cidA)
	res := &fakeResolver{err: errors.New("boom")}
	proj := moduleprogress.NewProjector(res, store, moduleprogress.NewInMemProgressStore())
	if _, err := proj.RecordCompletion(context.Background(), tTenant, tGCID, "atom", "r"); err == nil {
		t.Fatalf("resolver error must propagate (NACK → redelivery), not be swallowed")
	}
}

func TestProjector_RecordCompletion_MissingArgs_Errors(t *testing.T) {
	t.Parallel()
	proj := moduleprogress.NewProjector(&fakeResolver{}, module.NewInMemModuleStore(), moduleprogress.NewInMemProgressStore())
	if _, err := proj.RecordCompletion(context.Background(), tTenant, "", "atom", "r"); !errors.Is(err, moduleprogress.ErrInvalidArgument) {
		t.Fatalf("missing gcid must be ErrInvalidArgument; got %v", err)
	}
}
