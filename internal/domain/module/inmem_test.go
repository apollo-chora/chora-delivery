// inmem_test.go — round-trip tests for InMemModuleStore (the dev/test
// ModulePort impl). Mirrors the InMemEnrollmentStore style: ctx accepted then
// dropped, tenant-scoped lookups, soft-delete-aware reads.
package module_test

import (
	"context"
	"errors"
	"testing"

	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
)

func mustCreate(t *testing.T, s *module.InMemModuleStore, title string) *module.Module {
	t.Helper()
	m, err := module.New(module.NewParams{TenantID: tTenant, CourseID: tCourse, Title: title})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := s.Create(context.Background(), m)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return got
}

func TestInMem_Create_AssignsDensePositionPerCourse(t *testing.T) {
	t.Parallel()
	s := module.NewInMemModuleStore()
	m0 := mustCreate(t, s, "Intro")
	m1 := mustCreate(t, s, "Core")
	m2 := mustCreate(t, s, "Advanced")
	if m0.Position != 0 || m1.Position != 1 || m2.Position != 2 {
		t.Fatalf("dense positions expected 0,1,2; got %d,%d,%d", m0.Position, m1.Position, m2.Position)
	}
}

func TestInMem_Create_NilModule(t *testing.T) {
	t.Parallel()
	s := module.NewInMemModuleStore()
	if _, err := s.Create(context.Background(), nil); !errors.Is(err, module.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument; got %v", err)
	}
}

func TestInMem_Get_HitMissDeleted(t *testing.T) {
	t.Parallel()
	s := module.NewInMemModuleStore()
	m := mustCreate(t, s, "Intro")

	got, ok, err := s.Get(context.Background(), m.ID)
	if err != nil || !ok || got.ID != m.ID {
		t.Fatalf("Get hit: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := s.Get(context.Background(), "ghost"); ok {
		t.Fatalf("Get miss must be ok=false")
	}
	if err := s.SoftDelete(context.Background(), tTenant, m.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, ok, _ := s.Get(context.Background(), m.ID); ok {
		t.Fatalf("Get on soft-deleted must be ok=false")
	}
}

func TestInMem_ListByCourse_SortedActiveScoped(t *testing.T) {
	t.Parallel()
	s := module.NewInMemModuleStore()
	m0 := mustCreate(t, s, "Intro")
	m1 := mustCreate(t, s, "Core")
	mustCreate(t, s, "Advanced")

	// A module in a different course must not surface.
	other, _ := module.New(module.NewParams{TenantID: tTenant, CourseID: "01970000-0000-7000-8000-0000000000ff", Title: "Other"})
	s.Create(context.Background(), other)

	// Soft-delete the middle module — it must drop out of the listing.
	if err := s.SoftDelete(context.Background(), tTenant, m1.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	got, err := s.ListByCourse(context.Background(), tTenant, tCourse)
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 active modules in course; got %d", len(got))
	}
	if got[0].ID != m0.ID {
		t.Fatalf("first listed module must be the position-0 module")
	}
	if got[0].Position > got[1].Position {
		t.Fatalf("modules must be position-ASC ordered")
	}

	// Cross-tenant read must be empty.
	empty, _ := s.ListByCourse(context.Background(), "01970000-0000-7000-8000-000000000002", tCourse)
	if len(empty) != 0 {
		t.Fatalf("cross-tenant list must be empty; got %d", len(empty))
	}
}

func TestInMem_ItemLifecycle_Delegates(t *testing.T) {
	t.Parallel()
	s := module.NewInMemModuleStore()
	m := mustCreate(t, s, "Intro")

	a, err := s.AddItem(context.Background(), tTenant, m.ID, cid1)
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	b, err := s.AddItem(context.Background(), tTenant, m.ID, cid2)
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}

	// Reorder b,a.
	if err := s.Reorder(context.Background(), tTenant, m.ID, []string{b.ID, a.ID}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	got, _, _ := s.Get(context.Background(), m.ID)
	if got.Items[0].ID != b.ID || got.Items[1].ID != a.ID {
		t.Fatalf("reorder not applied: %+v", got.Items)
	}

	// Remove the first, expect a single dense item left.
	if err := s.RemoveItem(context.Background(), tTenant, m.ID, b.ID); err != nil {
		t.Fatalf("RemoveItem: %v", err)
	}
	got, _, _ = s.Get(context.Background(), m.ID)
	if len(got.Items) != 1 || got.Items[0].ID != a.ID || got.Items[0].Position != 0 {
		t.Fatalf("post-remove state wrong: %+v", got.Items)
	}
}

func TestInMem_CrossTenantMutation_Refused(t *testing.T) {
	t.Parallel()
	s := module.NewInMemModuleStore()
	m := mustCreate(t, s, "Intro")
	const otherTenant = "01970000-0000-7000-8000-000000000002"

	if _, err := s.AddItem(context.Background(), otherTenant, m.ID, cid1); !errors.Is(err, module.ErrNotFound) {
		t.Fatalf("cross-tenant AddItem must miss; got %v", err)
	}
	if err := s.SoftDelete(context.Background(), otherTenant, m.ID); !errors.Is(err, module.ErrNotFound) {
		t.Fatalf("cross-tenant SoftDelete must miss; got %v", err)
	}
	if err := s.RemoveItem(context.Background(), otherTenant, m.ID, "x"); !errors.Is(err, module.ErrNotFound) {
		t.Fatalf("cross-tenant RemoveItem must miss; got %v", err)
	}
	if err := s.Reorder(context.Background(), otherTenant, m.ID, nil); !errors.Is(err, module.ErrNotFound) {
		t.Fatalf("cross-tenant Reorder must miss; got %v", err)
	}
}
