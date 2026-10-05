// project_group_repo_test.go — direct unit tests for the in-memory
// ProjectGroupRepo (M15b R+ build-out). Pins the defensive copy on
// Save/Get/List, tenant + course scoping, soft-delete invisibility, and the
// created_at DESC ordering (with ID tiebreak).
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	pg "github.com/apollo-chora/chora-delivery/internal/domain/project_group"
)

const (
	pgCourse1 = "019e2f93-d586-71b5-8c3d-e2b0d0d5cc01"
	pgCourse2 = "019e2f93-d586-71b5-8c3d-e2b0d0d5cc02"
)

func mustGroup(t *testing.T, tenantID, courseID string) *pg.ProjectGroup {
	t.Helper()
	g, err := pg.NewProjectGroup(pg.NewProjectGroupInput{
		TenantID: tenantID,
		CourseID: courseID,
		Name:     "group-" + courseID,
		Members: []pg.Member{
			{GCID: "019e2f93-d586-71b5-8c3d-e2b0d0d5ee01", Role: pg.RoleMember},
			{GCID: "019e2f93-d586-71b5-8c3d-e2b0d0d5ee02", Role: pg.RoleMember},
		},
	})
	if err != nil {
		t.Fatalf("NewProjectGroup: %v", err)
	}
	return g
}

func TestProjectGroupRepo_SaveGet(t *testing.T) {
	t.Parallel()
	r := inmem.NewProjectGroupRepo()
	ctx := context.Background()

	if err := r.Save(ctx, nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	g := mustGroup(t, tenantA, pgCourse1)
	if err := r.Save(ctx, g); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.Get(ctx, g.ID)
	if err != nil || !ok {
		t.Fatalf("Get: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != g.ID || len(got.Members) != 2 {
		t.Fatalf("round-trip mismatch; members=%d", len(got.Members))
	}

	// The returned copy is decoupled from the stored mirror.
	got.Members = append(got.Members, pg.Member{GCID: "x", Role: pg.RoleMember})
	again, ok, _ := r.Get(ctx, g.ID)
	if !ok || len(again.Members) != 2 {
		t.Fatalf("stored members must be defensive-copied; got %d", len(again.Members))
	}

	if _, ok, _ := r.Get(ctx, "missing"); ok {
		t.Fatal("Get: expected miss for unknown id")
	}
}

func TestProjectGroupRepo_Get_SoftDeletedIsMiss(t *testing.T) {
	t.Parallel()
	r := inmem.NewProjectGroupRepo()
	ctx := context.Background()
	g := mustGroup(t, tenantA, pgCourse1)
	now := time.Now().UTC()
	g.DeletedAt = &now
	if err := r.Save(ctx, g); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, ok, _ := r.Get(ctx, g.ID); ok {
		t.Fatal("soft-deleted group must read as a miss")
	}
}

func TestProjectGroupRepo_ListByTenant_NewestFirst(t *testing.T) {
	t.Parallel()
	r := inmem.NewProjectGroupRepo()
	ctx := context.Background()

	older := mustGroup(t, tenantA, pgCourse1)
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := mustGroup(t, tenantA, pgCourse1)
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	b1 := mustGroup(t, tenantB, pgCourse1)
	gone := mustGroup(t, tenantA, pgCourse1)
	now := time.Now().UTC()
	gone.DeletedAt = &now

	for _, g := range []*pg.ProjectGroup{newer, older, b1, gone} {
		if err := r.Save(ctx, g); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	out, err := r.ListByTenant(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 (tenant-scoped, soft-delete filtered); got %d", len(out))
	}
	// created_at DESC — newest first.
	if out[0].ID != newer.ID || out[1].ID != older.ID {
		t.Fatalf("expected [newer older] by created_at DESC, got [%s %s]", out[0].ID, out[1].ID)
	}
}

func TestProjectGroupRepo_ListByCourse(t *testing.T) {
	t.Parallel()
	r := inmem.NewProjectGroupRepo()
	ctx := context.Background()

	// Two same-tenant groups in the target course (equal CreatedAt → ID tiebreak),
	// one created later (drives the created_at DESC arm of the comparator),
	// plus one in a different course and one soft-deleted.
	c1 := mustGroup(t, tenantA, pgCourse1)
	c1.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c2 := mustGroup(t, tenantA, pgCourse1)
	c2.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // tie with c1
	c3 := mustGroup(t, tenantA, pgCourse1)
	c3.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // newest, same course
	other := mustGroup(t, tenantA, pgCourse2)
	other.CreatedAt = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) // newest, wrong course
	gone := mustGroup(t, tenantA, pgCourse1)
	now := time.Now().UTC()
	gone.DeletedAt = &now

	for _, g := range []*pg.ProjectGroup{c1, c2, c3, other, gone} {
		if err := r.Save(ctx, g); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	out, err := r.ListByCourse(ctx, tenantA, pgCourse1)
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 (tenant+course scoped, soft-delete filtered); got %d", len(out))
	}
	// c3 is newest → first; the c1/c2 tie falls back to ID ascending.
	if out[0].ID != c3.ID {
		t.Fatalf("expected newest-first head %s, got %s", c3.ID, out[0].ID)
	}
	tied := []string{out[1].ID, out[2].ID}
	expected := []string{c1.ID, c2.ID}
	if c1.ID > c2.ID {
		expected = []string{c2.ID, c1.ID}
	}
	if tied[0] != expected[0] || tied[1] != expected[1] {
		t.Fatalf("expected tie-break order [%s %s], got [%s %s]", expected[0], expected[1], tied[0], tied[1])
	}

	empty, err := r.ListByCourse(ctx, tenantB, pgCourse1)
	if err != nil {
		t.Fatalf("ListByCourse(tenantB): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("tenantB must be empty; got %d", len(empty))
	}
}
