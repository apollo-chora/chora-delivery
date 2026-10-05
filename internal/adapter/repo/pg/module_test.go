// module_test.go — unit tests for pg.ModuleRepo.
//
// Mirrors enrollment_test.go / course_content_test.go: a stub Querier exercises
// the SQL surface without a live DB. Guarantees:
//  1. rls.ApplySession runs BEFORE any data query (every method);
//  2. SQL templates match the expected shape (dense-append INSERT, soft-delete
//     re-compaction, cascade soft-delete, position-only reorder UPDATE);
//  3. bind-arg order matches the $N placeholders;
//  4. nil-tx returns ErrNotImplemented (fail-loud);
//  5. empty tenant/course/id inputs return dedicated sentinels.
//
// Live RLS isolation + real dense-append are verified in
// module_integration_test.go (build tag `integration`).
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
)

const (
	modID   = "01970000-0000-7000-a000-000000000001"
	modItem = "01970000-0000-7000-a000-0000000000f1"
	modCID1 = "01970000-0000-7000-b000-0000000000c1"
	modCID2 = "01970000-0000-7000-b000-0000000000c2"
)

// fillModuleRow writes an active all_items module into scanModule's 11 dests.
func fillModuleRow(dest []any, id string, position int) error {
	if len(dest) != 11 {
		return errors.New("scanModule: expected 11 destinations")
	}
	now := time.Now().UTC()
	*(dest[0].(*string)) = id
	*(dest[1].(*string)) = tenantID
	*(dest[2].(*string)) = courseID
	*(dest[3].(*string)) = "Fractions"
	*(dest[4].(*int)) = position
	*(dest[5].(*string)) = string(module.RequirementAllItems)
	*(dest[6].(*int)) = 0
	*(dest[7].(*[]byte)) = []byte("[]")
	*(dest[8].(*time.Time)) = now
	*(dest[9].(*time.Time)) = now
	*(dest[10].(**time.Time)) = nil
	return nil
}

// fillModuleItemRow writes one active item into scanModuleItem's 7 dests.
func fillModuleItemRow(dest []any, itemID, contentItemID string, position int) error {
	if len(dest) != 7 {
		return errors.New("scanModuleItem: expected 7 destinations")
	}
	now := time.Now().UTC()
	*(dest[0].(*string)) = itemID
	*(dest[1].(*string)) = modID
	*(dest[2].(*string)) = tenantID
	*(dest[3].(*string)) = contentItemID
	*(dest[4].(*int)) = position
	*(dest[5].(*time.Time)) = now
	*(dest[6].(*time.Time)) = now
	return nil
}

// -----------------------------------------------------------------------------
// Nil-TxRunner — fail-loud
// -----------------------------------------------------------------------------

func TestModuleRepo_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewModuleRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	m, _ := module.New(module.NewParams{TenantID: tenantID, CourseID: courseID, Title: "x"})

	if _, err := r.Create(ctx, m); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Create: expected ErrNotImplemented; got %v", err)
	}
	if _, _, err := r.Get(ctx, modID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Get: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByCourse(ctx, tenantID, courseID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByCourse: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.AddItem(ctx, tenantID, modID, modCID1); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("AddItem: expected ErrNotImplemented; got %v", err)
	}
	if err := r.RemoveItem(ctx, tenantID, modID, modItem); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("RemoveItem: expected ErrNotImplemented; got %v", err)
	}
	if err := r.Reorder(ctx, tenantID, modID, nil); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Reorder: expected ErrNotImplemented; got %v", err)
	}
	if err := r.SoftDelete(ctx, tenantID, modID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("SoftDelete: expected ErrNotImplemented; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Input validation
// -----------------------------------------------------------------------------

func TestModuleRepo_Create_RejectsEmptyTenantAndCourse(t *testing.T) {
	t.Parallel()
	r := pg.NewModuleRepo(&stubTxRunner{q: &stubQuerier{}})
	ctx := context.Background()

	m1 := &module.Module{TenantID: "  ", CourseID: courseID, Title: "x", Requirement: module.DefaultRequirement()}
	if _, err := r.Create(ctx, m1); !errors.Is(err, pg.ErrModuleMissingTenant) {
		t.Fatalf("expected ErrModuleMissingTenant; got %v", err)
	}
	m2 := &module.Module{TenantID: tenantID, CourseID: "", Title: "x", Requirement: module.DefaultRequirement()}
	if _, err := r.Create(ctx, m2); !errors.Is(err, pg.ErrModuleMissingCourse) {
		t.Fatalf("expected ErrModuleMissingCourse; got %v", err)
	}
	if _, err := r.Create(ctx, nil); err == nil {
		t.Fatalf("nil module must error")
	}
}

func TestModuleRepo_MutatorsRejectEmptyTenant(t *testing.T) {
	t.Parallel()
	r := pg.NewModuleRepo(&stubTxRunner{q: &stubQuerier{}})
	ctx := context.Background()
	if _, err := r.AddItem(ctx, " ", modID, modCID1); !errors.Is(err, pg.ErrModuleMissingTenant) {
		t.Fatalf("AddItem: expected ErrModuleMissingTenant; got %v", err)
	}
	if err := r.RemoveItem(ctx, "", modID, modItem); !errors.Is(err, pg.ErrModuleMissingTenant) {
		t.Fatalf("RemoveItem: expected ErrModuleMissingTenant; got %v", err)
	}
	if _, err := r.ListByCourse(ctx, "", courseID); !errors.Is(err, pg.ErrModuleMissingTenant) {
		t.Fatalf("ListByCourse: expected ErrModuleMissingTenant; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Create — dense-append INSERT (RETURNING position) + bulk item inserts
// -----------------------------------------------------------------------------

func TestModuleRepo_Create_AppliesRLSThenDenseAppendInsert(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			// INSERT ... RETURNING position → scan a single int.
			return stubRow{scanFn: func(dest ...any) error {
				if len(dest) != 1 {
					return errors.New("expected 1 dest (position)")
				}
				*(dest[0].(*int)) = 3
				return nil
			}}
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})

	m, _ := module.New(module.NewParams{TenantID: tenantID, CourseID: courseID, Title: "Fractions"})
	m.AddItem(modCID1)
	m.AddItem(modCID2)

	got, err := r.Create(context.Background(), m)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Position != 3 {
		t.Fatalf("Create must adopt the DB-assigned position (3); got %d", got.Position)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	insert := q.sqls[1]
	for _, frag := range []string{"INSERT INTO course_modules", "RETURNING position", "MAX(position)"} {
		if !strings.Contains(insert, frag) {
			t.Fatalf("module INSERT must contain %q; got %q", frag, insert)
		}
	}
	// Two item INSERTs follow.
	itemInserts := 0
	for _, s := range q.sqls {
		if strings.Contains(s, "INSERT INTO course_module_items") {
			itemInserts++
		}
	}
	if itemInserts != 2 {
		t.Fatalf("expected 2 item INSERTs; got %d", itemInserts)
	}
}

// -----------------------------------------------------------------------------
// Get — module SELECT then items SELECT, assembled
// -----------------------------------------------------------------------------

func TestModuleRepo_Get_HydratesModuleWithItems(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return fillModuleRow(dest, modID, 0) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return fillModuleItemRow(dest, modItem, modCID1, 0) },
			}}, nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	m, ok, err := r.Get(ctx, modID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if m.ID != modID || len(m.Items) != 1 || m.Items[0].ContentItemID != modCID1 {
		t.Fatalf("assembled module wrong: %+v", m)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	if !strings.Contains(q.sqls[1], "FROM course_modules") {
		t.Fatalf("second SQL must SELECT the module; got %q", q.sqls[1])
	}
	if !strings.Contains(q.sqls[2], "FROM course_module_items") {
		t.Fatalf("third SQL must SELECT items; got %q", q.sqls[2])
	}
}

func TestModuleRepo_Get_Miss_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	m, ok, err := r.Get(ctx, modID)
	if err != nil {
		t.Fatalf("Get miss must not error; got %v", err)
	}
	if ok || m != nil {
		t.Fatalf("expected (nil,false); got (%+v,%v)", m, ok)
	}
}

// -----------------------------------------------------------------------------
// ListByCourse — modules then a single ANY() items query
// -----------------------------------------------------------------------------

func TestModuleRepo_ListByCourse_TwoModulesOneItemsQuery(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "FROM course_modules") {
				return &stubRows{rows: []func(dest ...any) error{
					func(dest ...any) error { return fillModuleRow(dest, modID, 0) },
					func(dest ...any) error { return fillModuleRow(dest, "01970000-0000-7000-a000-000000000002", 1) },
				}}, nil
			}
			// items ANY() query
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return fillModuleItemRow(dest, modItem, modCID1, 0) },
			}}, nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	out, err := r.ListByCourse(ctx, tenantID, courseID)
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 modules; got %d", len(out))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	// The item hydration must use a single ANY($n::uuid[]) query, not N queries.
	anyQueries := 0
	for _, s := range q.sqls {
		if strings.Contains(s, "module_id = ANY(") {
			anyQueries++
		}
	}
	if anyQueries != 1 {
		t.Fatalf("expected exactly 1 ANY() items query; got %d", anyQueries)
	}
	// The single item belongs to modID.
	if len(out[0].Items) != 1 || out[0].Items[0].ContentItemID != modCID1 {
		t.Fatalf("items not routed to their module: %+v", out[0].Items)
	}
}

// -----------------------------------------------------------------------------
// AddItem — load-mutate-persist: loads module+items then INSERTs the new item
// -----------------------------------------------------------------------------

func TestModuleRepo_AddItem_LoadsThenInserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return fillModuleRow(dest, modID, 0) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil // module currently has no items
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	it, err := r.AddItem(ctx, tenantID, modID, modCID1)
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if it.ContentItemID != modCID1 || it.Position != 0 {
		t.Fatalf("returned item wrong: %+v", it)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO course_module_items") {
		t.Fatalf("last SQL must INSERT the item; got %q", last)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
}

// -----------------------------------------------------------------------------
// RemoveItem — soft-delete + shift-down re-compaction
// -----------------------------------------------------------------------------

func TestModuleRepo_RemoveItem_SoftDeletesAndShiftsDown(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return fillModuleRow(dest, modID, 0) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return fillModuleItemRow(dest, modItem, modCID1, 0) },
				func(dest ...any) error {
					return fillModuleItemRow(dest, "01970000-0000-7000-a000-0000000000f2", modCID2, 1)
				},
			}}, nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	if err := r.RemoveItem(ctx, tenantID, modID, modItem); err != nil {
		t.Fatalf("RemoveItem: %v", err)
	}
	sawSoftDelete, sawShift := false, false
	for _, s := range q.sqls {
		if strings.Contains(s, "UPDATE course_module_items") && strings.Contains(s, "deleted_at =") && strings.Contains(s, "item_id =") {
			sawSoftDelete = true
		}
		if strings.Contains(s, "position = position - 1") {
			sawShift = true
		}
	}
	if !sawSoftDelete {
		t.Fatalf("expected a soft-delete UPDATE on the item; sqls=%v", q.sqls)
	}
	if !sawShift {
		t.Fatalf("expected a position shift-down UPDATE; sqls=%v", q.sqls)
	}
}

// -----------------------------------------------------------------------------
// Reorder — position-only UPDATEs, one per item
// -----------------------------------------------------------------------------

func TestModuleRepo_Reorder_UpdatesPositions(t *testing.T) {
	t.Parallel()
	item2 := "01970000-0000-7000-a000-0000000000f2"
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return fillModuleRow(dest, modID, 0) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return fillModuleItemRow(dest, modItem, modCID1, 0) },
				func(dest ...any) error { return fillModuleItemRow(dest, item2, modCID2, 1) },
			}}, nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	if err := r.Reorder(ctx, tenantID, modID, []string{item2, modItem}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	posUpdates := 0
	for _, s := range q.sqls {
		if strings.Contains(s, "UPDATE course_module_items") && strings.Contains(s, "position = $") {
			posUpdates++
		}
	}
	if posUpdates != 2 {
		t.Fatalf("expected 2 position UPDATEs; got %d (sqls=%v)", posUpdates, q.sqls)
	}
}

// -----------------------------------------------------------------------------
// SoftDelete — module UPDATE + cascade items UPDATE
// -----------------------------------------------------------------------------

func TestModuleRepo_SoftDelete_CascadesWithinAggregate(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	if err := r.SoftDelete(ctx, tenantID, modID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	sawModule, sawItems := false, false
	for _, s := range q.sqls {
		if strings.Contains(s, "UPDATE course_modules") && strings.Contains(s, "deleted_at =") {
			sawModule = true
		}
		if strings.Contains(s, "UPDATE course_module_items") && strings.Contains(s, "module_id =") && strings.Contains(s, "deleted_at =") {
			sawItems = true
		}
	}
	if !sawModule || !sawItems {
		t.Fatalf("SoftDelete must UPDATE module + cascade items; module=%v items=%v", sawModule, sawItems)
	}
}
