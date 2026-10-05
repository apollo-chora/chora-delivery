//go:build integration

// module_integration_test.go — exercises pg.ModuleRepo against live Cloud SQL
// via the Cloud SQL Auth Proxy. Shares the liveDB(t) + liveTxRunner harness
// declared in course_integration_test.go (same _test package).
//
// Run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration -run TestIntegration_ModuleRepo \
//	  ./services/chora-delivery/internal/adapter/repo/pg/...
//
// Requires migration 0039_course_modules applied to the target DB. No external
// seeds: course_id + content_item_id are bare cross-aggregate UUIDs (no FK).
//
// Suite:
//  1. Create → Get round-trip (module + items + position assigned).
//  2. Dense-append: two modules in a course get positions 0,1.
//  3. RLS isolation: Create under A, Get/List under B → miss.
//  4. AddItem → RemoveItem re-compaction persists (dense on reload).
//  5. Reorder persists the new order.
//  6. SoftDelete cascades (module + items drop from active reads).
//  7. n_of_m requirement round-trips.
package pg_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
)

// cleanupModule hard-deletes a module + its items after a test (tenant-scoped so
// RLS admits the DELETE), leaving the dev DB pristine.
func cleanupModule(t *testing.T, pool *pgxpool.Pool, tenantID, moduleID string) {
	t.Helper()
	t.Cleanup(func() {
		cctx := context.Background()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer tx.Rollback(cctx)
		if _, err := tx.Exec(cctx, "SET LOCAL chora.tenant_id = '"+tenantID+"'"); err != nil {
			return
		}
		_, _ = tx.Exec(cctx, `DELETE FROM course_module_items WHERE module_id = $1`, moduleID)
		_, _ = tx.Exec(cctx, `DELETE FROM course_modules WHERE id = $1`, moduleID)
		_ = tx.Commit(cctx)
	})
}

func mkLiveModule(t *testing.T, tenantID, courseID, title string) *module.Module {
	t.Helper()
	m, err := module.New(module.NewParams{TenantID: tenantID, CourseID: courseID, Title: title})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

// -----------------------------------------------------------------------------
// 1. Create -> Get round-trip
// -----------------------------------------------------------------------------

func TestIntegration_ModuleRepo_CreateGet_Roundtrip(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewModuleRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	course, _ := uuid.NewV7()
	cItem, _ := uuid.NewV7()
	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	m := mkLiveModule(t, tenantA.String(), course.String(), "Intro")
	m.AddItem(cItem.String())
	cleanupModule(t, pool, tenantA.String(), m.ID)

	got, err := r.Create(ctx, m)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Position != 0 {
		t.Fatalf("first module in course must be position 0; got %d", got.Position)
	}
	loaded, ok, err := r.Get(ctx, m.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if loaded.Title != "Intro" || len(loaded.Items) != 1 || loaded.Items[0].ContentItemID != cItem.String() {
		t.Fatalf("round-trip mismatch: %+v", loaded)
	}
}

// -----------------------------------------------------------------------------
// 2. Dense-append position within a course
// -----------------------------------------------------------------------------

func TestIntegration_ModuleRepo_DenseAppendPosition(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewModuleRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	course, _ := uuid.NewV7()
	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	m0 := mkLiveModule(t, tenantA.String(), course.String(), "One")
	m1 := mkLiveModule(t, tenantA.String(), course.String(), "Two")
	cleanupModule(t, pool, tenantA.String(), m0.ID)
	cleanupModule(t, pool, tenantA.String(), m1.ID)

	if _, err := r.Create(ctx, m0); err != nil {
		t.Fatalf("Create m0: %v", err)
	}
	if _, err := r.Create(ctx, m1); err != nil {
		t.Fatalf("Create m1: %v", err)
	}
	if m0.Position != 0 || m1.Position != 1 {
		t.Fatalf("dense append expected 0,1; got %d,%d", m0.Position, m1.Position)
	}
	list, err := r.ListByCourse(ctx, tenantA.String(), course.String())
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(list) != 2 || list[0].ID != m0.ID || list[1].ID != m1.ID {
		t.Fatalf("list order wrong: %+v", list)
	}
}

// -----------------------------------------------------------------------------
// 3. RLS isolation through the repo layer
// -----------------------------------------------------------------------------

func TestIntegration_ModuleRepo_RLS_TenantIsolation(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewModuleRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	course, _ := uuid.NewV7()

	ctxA := tracing.WithTenantID(context.Background(), tenantA.String())
	m := mkLiveModule(t, tenantA.String(), course.String(), "Secret")
	cleanupModule(t, pool, tenantA.String(), m.ID)
	if _, err := r.Create(ctxA, m); err != nil {
		t.Fatalf("Create under A: %v", err)
	}

	ctxB := tracing.WithTenantID(context.Background(), tenantB.String())
	if got, ok, _ := r.Get(ctxB, m.ID); ok || got != nil {
		t.Fatalf("RLS LEAK: tenant B saw tenant A's module")
	}
	listB, err := r.ListByCourse(ctxB, tenantB.String(), course.String())
	if err != nil {
		t.Fatalf("ListByCourse under B: %v", err)
	}
	if len(listB) != 0 {
		t.Fatalf("RLS LEAK: tenant B listed tenant A's module")
	}
	if got, ok, _ := r.Get(ctxA, m.ID); !ok || got == nil {
		t.Fatalf("tenant A must see its own module")
	}
}

// -----------------------------------------------------------------------------
// 4. AddItem -> RemoveItem re-compaction persists
// -----------------------------------------------------------------------------

func TestIntegration_ModuleRepo_AddRemove_Recompacts(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewModuleRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	course, _ := uuid.NewV7()
	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	m := mkLiveModule(t, tenantA.String(), course.String(), "Core")
	cleanupModule(t, pool, tenantA.String(), m.ID)
	if _, err := r.Create(ctx, m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	c0, _ := uuid.NewV7()
	c1, _ := uuid.NewV7()
	c2, _ := uuid.NewV7()
	if _, err := r.AddItem(ctx, tenantA.String(), m.ID, c0.String()); err != nil {
		t.Fatalf("AddItem c0: %v", err)
	}
	mid, err := r.AddItem(ctx, tenantA.String(), m.ID, c1.String())
	if err != nil {
		t.Fatalf("AddItem c1: %v", err)
	}
	if _, err := r.AddItem(ctx, tenantA.String(), m.ID, c2.String()); err != nil {
		t.Fatalf("AddItem c2: %v", err)
	}
	if err := r.RemoveItem(ctx, tenantA.String(), m.ID, mid.ID); err != nil {
		t.Fatalf("RemoveItem: %v", err)
	}
	loaded, _, err := r.Get(ctx, m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.Items) != 2 {
		t.Fatalf("expected 2 items after remove; got %d", len(loaded.Items))
	}
	for i, it := range loaded.Items {
		if it.Position != i {
			t.Fatalf("positions must be dense after remove; item %d @ %d", i, it.Position)
		}
	}
	if loaded.Items[0].ContentItemID != c0.String() || loaded.Items[1].ContentItemID != c2.String() {
		t.Fatalf("wrong survivors after remove: %+v", loaded.Items)
	}
}

// -----------------------------------------------------------------------------
// 5. Reorder persists
// -----------------------------------------------------------------------------

func TestIntegration_ModuleRepo_Reorder_Persists(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewModuleRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	course, _ := uuid.NewV7()
	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	m := mkLiveModule(t, tenantA.String(), course.String(), "Order")
	cleanupModule(t, pool, tenantA.String(), m.ID)
	if _, err := r.Create(ctx, m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	c0, _ := uuid.NewV7()
	c1, _ := uuid.NewV7()
	a, _ := r.AddItem(ctx, tenantA.String(), m.ID, c0.String())
	b, _ := r.AddItem(ctx, tenantA.String(), m.ID, c1.String())

	if err := r.Reorder(ctx, tenantA.String(), m.ID, []string{b.ID, a.ID}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	loaded, _, _ := r.Get(ctx, m.ID)
	if loaded.Items[0].ID != b.ID || loaded.Items[1].ID != a.ID {
		t.Fatalf("reorder not persisted: %+v", loaded.Items)
	}
}

// -----------------------------------------------------------------------------
// 6. SoftDelete cascades within the aggregate
// -----------------------------------------------------------------------------

func TestIntegration_ModuleRepo_SoftDelete_Cascades(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewModuleRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	course, _ := uuid.NewV7()
	cItem, _ := uuid.NewV7()
	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	m := mkLiveModule(t, tenantA.String(), course.String(), "Doomed")
	m.AddItem(cItem.String())
	cleanupModule(t, pool, tenantA.String(), m.ID)
	if _, err := r.Create(ctx, m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := r.SoftDelete(ctx, tenantA.String(), m.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, ok, _ := r.Get(ctx, m.ID); ok {
		t.Fatalf("soft-deleted module must not read active")
	}
	list, _ := r.ListByCourse(ctx, tenantA.String(), course.String())
	if len(list) != 0 {
		t.Fatalf("soft-deleted module must drop from listing; got %d", len(list))
	}
}

// -----------------------------------------------------------------------------
// 7. n_of_m requirement round-trips
// -----------------------------------------------------------------------------

func TestIntegration_ModuleRepo_Requirement_Roundtrips(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewModuleRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	course, _ := uuid.NewV7()
	c0, _ := uuid.NewV7()
	c1, _ := uuid.NewV7()
	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	m := mkLiveModule(t, tenantA.String(), course.String(), "Reqs")
	m.AddItem(c0.String())
	m.AddItem(c1.String())
	if err := m.SetRequirement(module.ModuleRequirement{Kind: module.RequirementNOfM, ThresholdN: 1}); err != nil {
		t.Fatalf("SetRequirement: %v", err)
	}
	cleanupModule(t, pool, tenantA.String(), m.ID)
	if _, err := r.Create(ctx, m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	loaded, _, err := r.Get(ctx, m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Requirement.Kind != module.RequirementNOfM || loaded.Requirement.ThresholdN != 1 {
		t.Fatalf("requirement did not round-trip: %+v", loaded.Requirement)
	}
}
