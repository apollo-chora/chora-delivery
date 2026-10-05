// coverage_test.go — closes the remaining statement gaps in the moduleprogress
// package: RecordItemCompletion's nil-module + blank-item guards,
// InMemProgressStore's find-existing + New-error paths in Advance, the
// GetByLearnerModule miss, ListByModuleIDs sorting/filtering, and the
// projector's nil-wiring / module-load-error / ErrItemNotInModule-skip /
// advance-error branches.
package moduleprogress_test

import (
	"context"
	"errors"
	"testing"

	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// -----------------------------------------------------------------------------
// RecordItemCompletion guards
// -----------------------------------------------------------------------------

func TestRecordItemCompletion_NilModule_Errors(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA)
	p := newProgress(t, m)
	if _, err := p.RecordItemCompletion(cidA, nil); !errors.Is(err, moduleprogress.ErrInvalidArgument) {
		t.Fatalf("nil module: expected ErrInvalidArgument; got %v", err)
	}
}

func TestRecordItemCompletion_BlankItem_Errors(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA)
	p := newProgress(t, m)
	if _, err := p.RecordItemCompletion("   ", m); !errors.Is(err, moduleprogress.ErrInvalidArgument) {
		t.Fatalf("blank content item: expected ErrInvalidArgument; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// InMemProgressStore — Advance on an existing projection + New-error path
// -----------------------------------------------------------------------------

func TestInMem_Advance_SecondCompletionFindsExistingRow(t *testing.T) {
	t.Parallel()
	store := moduleprogress.NewInMemProgressStore()
	m := buildModule(t, cidA, cidB)
	ctx := context.Background()

	changed, err := store.Advance(ctx, tTenant, tGCID, m.ID, tCourse, cidA, m)
	if err != nil || !changed {
		t.Fatalf("first advance: changed=%v err=%v", changed, err)
	}
	// Second advance must find the existing row (the loop-hit path) and still
	// report changed when the completed set grew.
	changed, err = store.Advance(ctx, tTenant, tGCID, m.ID, tCourse, cidB, m)
	if err != nil || !changed {
		t.Fatalf("second advance: changed=%v err=%v", changed, err)
	}
	got, ok, _ := store.GetByLearnerModule(ctx, tTenant, tGCID, m.ID)
	if !ok || len(got.CompletedContentItemIDs) != 2 {
		t.Fatalf("expected 2 completed items on one row; got %+v", got)
	}
	// Idempotent redelivery → false, and no error propagates from Record.
	changed, err = store.Advance(ctx, tTenant, tGCID, m.ID, tCourse, cidA, m)
	if err != nil || changed {
		t.Fatalf("redelivery: changed=%v err=%v", changed, err)
	}
}

func TestInMem_Advance_NewErrorPropagates(t *testing.T) {
	t.Parallel()
	store := moduleprogress.NewInMemProgressStore()
	m := buildModule(t, cidA)
	// Blank gcid makes the load-or-create New() fail inside Advance.
	if _, err := store.Advance(context.Background(), tTenant, "  ", m.ID, tCourse, cidA, m); !errors.Is(err, moduleprogress.ErrInvalidArgument) {
		t.Fatalf("blank gcid advance: expected ErrInvalidArgument; got %v", err)
	}
}

func TestInMem_GetByLearnerModule_Miss(t *testing.T) {
	t.Parallel()
	store := moduleprogress.NewInMemProgressStore()
	m := buildModule(t, cidA)
	p, ok, err := store.GetByLearnerModule(context.Background(), tTenant, tGCID, m.ID)
	if err != nil || ok || p != nil {
		t.Fatalf("miss must be (nil, false, nil); got (%v, %v, %v)", p, ok, err)
	}
}

func TestInMem_ListByModuleIDs_SortedAndScoped(t *testing.T) {
	t.Parallel()
	store := moduleprogress.NewInMemProgressStore()
	ctx := context.Background()

	modA := buildModule(t, cidA, cidB)
	modB := buildModule(t, cidA)
	const gcidB = "01970000-0000-7000-8000-0000000000bb"

	if _, err := store.Advance(ctx, tTenant, tGCID, modA.ID, tCourse, cidA, modA); err != nil {
		t.Fatalf("seed a/g: %v", err)
	}
	if _, err := store.Advance(ctx, tTenant, gcidB, modA.ID, tCourse, cidA, modA); err != nil {
		t.Fatalf("seed a/g2: %v", err)
	}
	if _, err := store.Advance(ctx, tTenant, tGCID, modB.ID, tCourse, cidA, modB); err != nil {
		t.Fatalf("seed b: %v", err)
	}

	rows, err := store.ListByModuleIDs(ctx, tTenant, []string{modB.ID, modA.ID})
	if err != nil {
		t.Fatalf("ListByModuleIDs: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows; got %d", len(rows))
	}
	// Sorted by ModuleID first, then GCID — the append + both comparator
	// branches fire together here.
	if rows[0].ModuleID != modA.ID || rows[0].GCID != gcidB {
		t.Fatalf("rows[0]: want %s/%s got %+v", modA.ID, gcidB, rows[0])
	}
	if rows[1].ModuleID != modA.ID || rows[1].GCID != tGCID {
		t.Fatalf("rows[1]: want %s/%s got %+v", modA.ID, tGCID, rows[1])
	}
	if rows[2].ModuleID != modB.ID {
		t.Fatalf("rows[2]: want module %s got %s", modB.ID, rows[2].ModuleID)
	}

	// Tenant scoping + module filter: unknown module / other tenant → empty.
	empty, err := store.ListByModuleIDs(ctx, "01970000-0000-7000-8000-0000000000ff", []string{modA.ID})
	if err != nil || len(empty) != 0 {
		t.Fatalf("cross-tenant list: rows=%d err=%v", len(empty), err)
	}
	empty, err = store.ListByModuleIDs(ctx, tTenant, []string{"ghost"})
	if err != nil || len(empty) != 0 {
		t.Fatalf("unknown module list: rows=%d err=%v", len(empty), err)
	}
}

// -----------------------------------------------------------------------------
// Projector — nil wiring, module-load error, item-not-in-module skip, advance error
// -----------------------------------------------------------------------------

func TestProjector_NilProjector_Errors(t *testing.T) {
	t.Parallel()
	var proj *moduleprogress.Projector
	if _, err := proj.RecordCompletion(context.Background(), tTenant, tGCID, "atom", "r"); err == nil {
		t.Fatalf("nil projector must error, got nil")
	}
}

// errLoader is a ModuleLoader that always fails loudly (subscriber NACK).
type errLoader struct{}

func (errLoader) Get(_ context.Context, _ string) (*module.Module, bool, error) {
	return nil, false, errors.New("db down")
}

func TestProjector_ModuleLoadError_Propagates(t *testing.T) {
	t.Parallel()
	res := &fakeResolver{targets: []moduleprogress.CompletionTarget{
		{ContentItemID: cidA, ModuleID: "01970000-0000-7000-8000-0000000000de", CourseID: tCourse},
	}}
	proj := moduleprogress.NewProjector(res, errLoader{}, moduleprogress.NewInMemProgressStore())
	if _, err := proj.RecordCompletion(context.Background(), tTenant, tGCID, "atom", "r"); err == nil {
		t.Fatalf("module load error must propagate, got nil")
	}
}

func TestProjector_ItemNotInModule_Skipped(t *testing.T) {
	t.Parallel()
	store, m := seedModule(t, cidA) // module holds only cidA
	// Resolver claims a content item the loaded module does not group → the
	// advance fails with ErrItemNotInModule and the projector skips, not aborts.
	res := &fakeResolver{targets: []moduleprogress.CompletionTarget{
		{ContentItemID: "01970000-0000-7000-9000-000000000099", ModuleID: m.ID, CourseID: tCourse},
	}}
	proj := moduleprogress.NewProjector(res, store, moduleprogress.NewInMemProgressStore())
	n, err := proj.RecordCompletion(context.Background(), tTenant, tGCID, "atom", "r")
	if err != nil {
		t.Fatalf("ErrItemNotInModule must be skipped, not returned: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 changed rows; got %d", n)
	}
}

// errAdvanceStore wraps InMemProgressStore with a hard-failing Advance so the
// projector's non-ErrItemNotInModule error path runs.
type errAdvanceStore struct {
	*moduleprogress.InMemProgressStore
}

func (e *errAdvanceStore) Advance(_ context.Context, _, _, _, _, _ string, _ *module.Module) (bool, error) {
	return false, errors.New("persist failed")
}

func TestProjector_AdvanceError_Propagates(t *testing.T) {
	t.Parallel()
	store, m := seedModule(t, cidA)
	res := &fakeResolver{targets: []moduleprogress.CompletionTarget{
		{ContentItemID: cidA, ModuleID: m.ID, CourseID: tCourse},
	}}
	proj := moduleprogress.NewProjector(res, store, &errAdvanceStore{moduleprogress.NewInMemProgressStore()})
	if _, err := proj.RecordCompletion(context.Background(), tTenant, tGCID, "atom", "r"); err == nil {
		t.Fatalf("advance error must propagate, got nil")
	}
}
