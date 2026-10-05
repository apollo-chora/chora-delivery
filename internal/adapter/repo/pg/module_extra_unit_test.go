// module_extra_unit_test.go — extra unit tests for pg.ModuleRepo, topping up
// module_test.go. Covers the 0% SetRequirement method end-to-end (load-module-
// in-tx → domain rule revalidation → UPDATE course_modules shape + bind args)
// and the still-missing branches of Create/Get/ListByCourse/AddItem/RemoveItem/
// Reorder/SoftDelete + helpers (error wraps, miss paths, empty-item lists).
//
// Mirrors module_test.go's stub discipline exactly: a stub Querier exercises
// the SQL surface without a live DB — RLS first (SET LOCAL), then the data
// query; error paths assert the wrapped message preserves the cause (%w).
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

// mdxScanErr returns a rowFn whose scan always fails with err — the shared
// miss/error row for module load paths.
func mdxScanErr(err error) func(sql string, args ...any) pg.Row {
	return func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return err }}
	}
}

// mdxModuleRowBadReqJSON is fillModuleRow with a corrupt
// requirement_required_item_ids JSONB — forces scanModule's unmarshal branch.
func mdxModuleRowBadReqJSON(dest []any, id string, position int) error {
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
	*(dest[7].(*[]byte)) = []byte("{not-json") // corrupt JSONB
	*(dest[8].(*time.Time)) = now
	*(dest[9].(*time.Time)) = now
	*(dest[10].(**time.Time)) = nil
	return nil
}

// -----------------------------------------------------------------------------
// SetRequirement — load-mutate-persist of the three requirement columns
// -----------------------------------------------------------------------------

func TestModuleRepo_SetRequirement_NilTx_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewModuleRepo(nil)
	if err := r.SetRequirement(context.Background(), tenantID, modID, module.DefaultRequirement()); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented on nil-tx; got %v", err)
	}
}

func TestModuleRepo_SetRequirement_RejectsEmptyTenantAndID(t *testing.T) {
	t.Parallel()
	r := pg.NewModuleRepo(&stubTxRunner{q: &stubQuerier{}})
	ctx := context.Background()
	if err := r.SetRequirement(ctx, " ", modID, module.DefaultRequirement()); !errors.Is(err, pg.ErrModuleMissingTenant) {
		t.Fatalf("expected ErrModuleMissingTenant; got %v", err)
	}
	if err := r.SetRequirement(ctx, tenantID, "", module.DefaultRequirement()); !errors.Is(err, pg.ErrModuleMissingID) {
		t.Fatalf("expected ErrModuleMissingID; got %v", err)
	}
}

func TestModuleRepo_SetRequirement_AppliesRLSThenLoadsThenUpdates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return fillModuleRow(dest, modID, 1) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return fillModuleItemRow(dest, modItem, modCID1, 0) },
			}}, nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	// One active item ⇒ an n_of_m threshold of 1 is a valid rule.
	if err := r.SetRequirement(ctx, tenantID, modID, module.ModuleRequirement{
		Kind: module.RequirementNOfM, ThresholdN: 1,
	}); err != nil {
		t.Fatalf("SetRequirement: %v", err)
	}
	// SQL sequence: SET LOCAL → module SELECT → items SELECT → requirement UPDATE.
	if len(q.sqls) != 4 {
		t.Fatalf("expected 4 SQLs (SET LOCAL + SELECT + SELECT + UPDATE); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	if !strings.Contains(q.sqls[1], "FROM course_modules") {
		t.Fatalf("second SQL must load the module; got %q", q.sqls[1])
	}
	upd := q.sqls[3]
	for _, frag := range []string{"UPDATE course_modules", "requirement_kind = $3", "requirement_threshold_n = $4", "requirement_required_item_ids = $5::jsonb"} {
		if !strings.Contains(upd, frag) {
			t.Fatalf("requirement UPDATE must contain %q; got %q", frag, upd)
		}
	}
	// Bind order: $1 tenant, $2 module, $3 kind, $4 threshold, $5 jsonb ids.
	args := q.args[3]
	if len(args) != 6 {
		t.Fatalf("expected 6 bind args; got %d (%v)", len(args), args)
	}
	if args[0] != tenantID || args[1] != modID {
		t.Fatalf("tenant/module bind wrong: %v / %v", args[0], args[1])
	}
	if args[2] != string(module.RequirementNOfM) {
		t.Fatalf("expected kind %q; got %v", module.RequirementNOfM, args[2])
	}
	if args[3] != 1 {
		t.Fatalf("expected threshold 1; got %v", args[3])
	}
	if args[4] != "[]" {
		t.Fatalf("expected empty required_item_ids JSONB; got %v", args[4])
	}
	if _, ok := args[5].(time.Time); !ok {
		t.Fatalf("expected time.Time bound at $6; got %T", args[5])
	}
}

func TestModuleRepo_SetRequirement_Miss_ReturnsModuleNotFound(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: mdxScanErr(errors.New("no rows in result set"))}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	err := r.SetRequirement(ctx, tenantID, modID, module.DefaultRequirement())
	if !errors.Is(err, module.ErrNotFound) {
		t.Fatalf("expected module.ErrNotFound on miss; got %v", err)
	}
}

func TestModuleRepo_SetRequirement_RejectedRule_AbortsWithoutUpdate(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return fillModuleRow(dest, modID, 1) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return fillModuleItemRow(dest, modItem, modCID1, 0) },
			}}, nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	// 1 item but threshold 5 ⇒ invalid — the domain rule must abort before
	// any UPDATE (fail-loud, atomic).
	err := r.SetRequirement(ctx, tenantID, modID, module.ModuleRequirement{
		Kind: module.RequirementNOfM, ThresholdN: 5,
	})
	if !errors.Is(err, module.ErrInvalidRequirement) {
		t.Fatalf("expected module.ErrInvalidRequirement; got %v", err)
	}
	for _, s := range q.sqls {
		if strings.Contains(s, "UPDATE course_modules") {
			t.Fatalf("a rejected rule must not reach the UPDATE; sqls=%v", q.sqls)
		}
	}
}

func TestModuleRepo_SetRequirement_UpdateExecError_Wrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("connection reset")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return fillModuleRow(dest, modID, 1) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "UPDATE course_modules") {
				return boom
			}
			return nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	// No items loaded ⇒ all_items (the default) is the only valid rule; it
	// passes the domain check and reaches the gated UPDATE.
	err := r.SetRequirement(ctx, tenantID, modID, module.DefaultRequirement())
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the UPDATE failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: update module requirement") {
		t.Fatalf("expected the wrap to name the operation; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Create — item-INSERT failure must abort the tx with a wrapped error
// -----------------------------------------------------------------------------

func TestModuleRepo_Create_ItemInsertError_Wrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("unique violation on item")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*int)) = 0
				return nil
			}}
		},
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "INSERT INTO course_module_items") {
				return boom
			}
			return nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	m, _ := module.New(module.NewParams{TenantID: tenantID, CourseID: courseID, Title: "x"})
	m.AddItem(modCID1)

	err, _ := func() (error, error) { _, e := r.Create(context.Background(), m); return e, nil }()
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the item INSERT failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: insert module item") {
		t.Fatalf("expected the wrap to name the item insert; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Get — items-query / scan / unmarshal failure paths (Get has no tenantID
// param, so the caller seeds ctx via tracing.WithTenantID for RLS)
// -----------------------------------------------------------------------------

func TestModuleRepo_Get_ItemsQueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return fillModuleRow(dest, modID, 0) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "course_module_items") {
				return nil, errors.New("items query conn closed")
			}
			return &stubRows{}, nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	_, _, err := r.Get(ctx, modID)
	if err == nil || !strings.Contains(err.Error(), "items query conn closed") {
		t.Fatalf("expected the items query error to propagate; got %v", err)
	}
}

func TestModuleRepo_Get_ItemScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return fillModuleRow(dest, modID, 0) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "course_module_items") {
				return &stubRows{rows: []func(dest ...any) error{
					func(dest ...any) error { return errors.New("bad item row bytes") },
				}}, nil
			}
			return &stubRows{}, nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	_, _, err := r.Get(ctx, modID)
	if err == nil || !strings.Contains(err.Error(), "bad item row bytes") {
		t.Fatalf("expected the item scan error to propagate; got %v", err)
	}
}

// A corrupt requirement JSONB fails scanModule's unmarshal — Get treats any
// scanModule error as a miss, so the row must come back (nil, false).
func TestModuleRepo_Get_CorruptRequirementJSONB_TreatedAsMiss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return mdxModuleRowBadReqJSON(dest, modID, 0) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	m, ok, err := r.Get(ctx, modID)
	if err != nil {
		t.Fatalf("corrupt module row must be treated as a miss, not an error; got %v", err)
	}
	if ok || m != nil {
		t.Fatalf("expected (nil,false) on corrupt row; got (%+v,%v)", m, ok)
	}
}

// -----------------------------------------------------------------------------
// ListByCourse — empty-module list + error paths
// -----------------------------------------------------------------------------

func TestModuleRepo_ListByCourse_NoModules_SkipsItemsQuery(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil // no modules at all
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	out, err := r.ListByCourse(ctx, tenantID, courseID)
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected an empty list; got %d modules", len(out))
	}
	for _, s := range q.sqls {
		if strings.Contains(s, "module_id = ANY(") {
			t.Fatalf("no items query may run for an empty module list; got %q", s)
		}
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
}

func TestModuleRepo_ListByCourse_ModuleScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "FROM course_modules") {
				return &stubRows{rows: []func(dest ...any) error{
					func(dest ...any) error { return errors.New("bad module row bytes") },
				}}, nil
			}
			return &stubRows{}, nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	if _, err := r.ListByCourse(ctx, tenantID, courseID); err == nil || !strings.Contains(err.Error(), "bad module row bytes") {
		t.Fatalf("expected the module scan error to propagate; got %v", err)
	}
}

func TestModuleRepo_ListByCourse_ItemsQueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "FROM course_modules") {
				return &stubRows{rows: []func(dest ...any) error{
					func(dest ...any) error { return fillModuleRow(dest, modID, 0) },
				}}, nil
			}
			return nil, errors.New("items ANY query failed")
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	if _, err := r.ListByCourse(ctx, tenantID, courseID); err == nil || !strings.Contains(err.Error(), "items ANY query failed") {
		t.Fatalf("expected the items ANY query error to propagate; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// AddItem — miss, domain duplicate, items-query and insert failures
// -----------------------------------------------------------------------------

func TestModuleRepo_AddItem_Miss_ReturnsModuleNotFound(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: mdxScanErr(errors.New("no rows in result set"))}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	if _, err := r.AddItem(ctx, tenantID, modID, modCID1); !errors.Is(err, module.ErrNotFound) {
		t.Fatalf("expected module.ErrNotFound on miss; got %v", err)
	}
}

func TestModuleRepo_AddItem_ItemsQueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return fillModuleRow(dest, modID, 0) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("items load conn closed")
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	if _, err := r.AddItem(ctx, tenantID, modID, modCID1); err == nil || !strings.Contains(err.Error(), "items load conn closed") {
		t.Fatalf("expected the items load error to propagate; got %v", err)
	}
}

func TestModuleRepo_AddItem_DuplicateContentItem_DomainError_NoInsert(t *testing.T) {
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
	ctx := context.Background()

	// modCID1 is already a member — the domain must refuse before the INSERT.
	if _, err := r.AddItem(ctx, tenantID, modID, modCID1); !errors.Is(err, module.ErrDuplicateItem) {
		t.Fatalf("expected module.ErrDuplicateItem; got %v", err)
	}
	for _, s := range q.sqls {
		if strings.Contains(s, "INSERT INTO course_module_items") {
			t.Fatalf("duplicate item must not reach the INSERT; sqls=%v", q.sqls)
		}
	}
}

func TestModuleRepo_AddItem_InsertExecError_Wrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("disk full")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return fillModuleRow(dest, modID, 0) }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "INSERT INTO course_module_items") {
				return boom
			}
			return nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	err, _ := func() (error, error) { _, e := r.AddItem(ctx, tenantID, modID, modCID1); return e, nil }()
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the item INSERT failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: insert module item") {
		t.Fatalf("expected the wrap to name the item insert; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// RemoveItem — shift-down exec failure (soft-delete happy path already covered
// by module_test.go)
// -----------------------------------------------------------------------------

func TestModuleRepo_RemoveItem_ShiftExecError_Wrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("constraint violation on shift")
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
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "position = position - 1") {
				return boom
			}
			return nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	err := r.RemoveItem(ctx, tenantID, modID, modItem)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the shift UPDATE failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: shift module item positions") {
		t.Fatalf("expected the wrap to name the shift; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Reorder — invalid permutation aborts pre-write; UPDATE failure wraps
// -----------------------------------------------------------------------------

func TestModuleRepo_Reorder_InvalidPermutation_NoUpdates(t *testing.T) {
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

	// 1 id for 2 items is not a permutation — the domain refuses pre-write.
	err := r.Reorder(ctx, tenantID, modID, []string{modItem})
	if !errors.Is(err, module.ErrInvalidReorder) {
		t.Fatalf("expected module.ErrInvalidReorder; got %v", err)
	}
	for _, s := range q.sqls {
		if strings.Contains(s, "position = $") {
			t.Fatalf("an invalid permutation must not write positions; sqls=%v", q.sqls)
		}
	}
}

func TestModuleRepo_Reorder_UpdateExecError_Wrapped(t *testing.T) {
	t.Parallel()
	item2 := "01970000-0000-7000-a000-0000000000f2"
	boom := errors.New("lock timeout")
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
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "position = $3") {
				return boom
			}
			return nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	err := r.Reorder(ctx, tenantID, modID, []string{item2, modItem})
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the position UPDATE failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: reorder update item") {
		t.Fatalf("expected the wrap to name the reorder write; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// SoftDelete — module + cascade UPDATE failures (happy cascade covered by
// module_test.go)
// -----------------------------------------------------------------------------

func TestModuleRepo_SoftDelete_ModuleUpdateError_Wrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("deadlock detected")
	q := &stubQuerier{
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "UPDATE course_modules") {
				return boom
			}
			return nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	err := r.SoftDelete(ctx, tenantID, modID)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the module UPDATE failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: soft-delete module") {
		t.Fatalf("expected the wrap to name the soft-delete; got %v", err)
	}
}

func TestModuleRepo_SoftDelete_CascadeUpdateError_Wrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("cascade constraint violation")
	q := &stubQuerier{
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "module_id = $2") && strings.Contains(sql, "deleted_at IS NULL") {
				return boom
			}
			return nil
		},
	}
	r := pg.NewModuleRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	err := r.SoftDelete(ctx, tenantID, modID)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the cascade UPDATE failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: cascade soft-delete module items") {
		t.Fatalf("expected the wrap to name the cascade; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Reorder / SoftDelete — empty module_id guard (adds the missing-ID branches
// of the existing mutator validation tests)
// -----------------------------------------------------------------------------

func TestModuleRepo_ReorderSoftDelete_RejectEmptyModuleID(t *testing.T) {
	t.Parallel()
	r := pg.NewModuleRepo(&stubTxRunner{q: &stubQuerier{}})
	ctx := context.Background()
	if err := r.Reorder(ctx, tenantID, "", nil); !errors.Is(err, pg.ErrModuleMissingID) {
		t.Fatalf("Reorder: expected ErrModuleMissingID; got %v", err)
	}
	if err := r.SoftDelete(ctx, tenantID, ""); !errors.Is(err, pg.ErrModuleMissingID) {
		t.Fatalf("SoftDelete: expected ErrModuleMissingID; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// loadModuleTx / selectModuleItems / scanModuleItem miss branches — selectModule
// items query returning zero rows is exercised by the AddItem tests above; the
// scan-error branch of scanModuleItem is exercised by Get_ItemScanError. A
// rejected-rule SetRequirement (above) also proves the load path runs inside
// the tx before any validation error surfaces.
// -----------------------------------------------------------------------------
