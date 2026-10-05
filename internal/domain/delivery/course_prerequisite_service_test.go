// course_prerequisite_service_test.go — port + in-memory adapter + application
// service tests for the Course Prerequisite DAG (ADR-226).
//
// TDD RED-first. The service composes the pure validator with catalogue
// existence checks (via CourseCJ2Port) and the CoursePrerequisitePort.
package delivery

import (
	"context"
	"errors"
	"testing"
)

// seedCourse stores a minimal DRAFT course so CourseCJ2Port.Get reports it.
func seedCourse(t *testing.T, store *InMemCourseCJ2Store, tenantID, id string) {
	t.Helper()
	if err := store.Save(context.Background(), &Course{
		ID: id, TenantID: tenantID, State: CourseStateDraft,
		AuthorGCID: preCourseA, Title: "seed",
	}); err != nil {
		t.Fatalf("seedCourse: %v", err)
	}
}

// prereqTestSvc wires an in-mem edge store + course store with A,B,C,D seeded.
func prereqTestSvc(t *testing.T, tenantID string) (*CoursePrerequisiteService, *InMemCoursePrerequisiteStore, *InMemCourseCJ2Store) {
	t.Helper()
	edges := NewInMemCoursePrerequisiteStore()
	courses := NewInMemCourseCJ2Store()
	for _, id := range []string{preCourseA, preCourseB, preCourseC, preCourseD} {
		seedCourse(t, courses, tenantID, id)
	}
	return NewCoursePrerequisiteService(edges, courses, 32), edges, courses
}

// -----------------------------------------------------------------------------
// UnmetHardGates — ADR-226 §4 enrol enforcement
// -----------------------------------------------------------------------------

// TestCoursePrerequisiteService_UnmetHardGates proves the enrol-gate logic:
// only hard_gate prerequisites the learner has NOT completed block; advisory
// edges never block; a course with no edges is always allowed.
func TestCoursePrerequisiteService_UnmetHardGates(t *testing.T) {
	svc, _, _ := prereqTestSvc(t, preTenantID)
	ctx := context.Background()

	// Course A requires B (hard_gate), C (hard_gate), D (advisory).
	if _, err := svc.Add(ctx, preTenantID, preCourseA, preCourseB, PrereqKindHardGate); err != nil {
		t.Fatalf("add B: %v", err)
	}
	if _, err := svc.Add(ctx, preTenantID, preCourseA, preCourseC, PrereqKindHardGate); err != nil {
		t.Fatalf("add C: %v", err)
	}
	if _, err := svc.Add(ctx, preTenantID, preCourseA, preCourseD, PrereqKindAdvisory); err != nil {
		t.Fatalf("add D: %v", err)
	}

	// Learner completed B only → C (hard_gate, incomplete) blocks; D (advisory) ignored.
	unmet, err := svc.UnmetHardGates(ctx, preTenantID, preCourseA, map[string]bool{preCourseB: true})
	if err != nil {
		t.Fatalf("UnmetHardGates: %v", err)
	}
	if len(unmet) != 1 || unmet[0].PrerequisiteCourseID != preCourseC {
		t.Fatalf("unmet = %+v, want exactly [C hard_gate]", unmet)
	}

	// All hard_gates completed → empty (enrol allowed).
	if got, _ := svc.UnmetHardGates(ctx, preTenantID, preCourseA, map[string]bool{preCourseB: true, preCourseC: true}); len(got) != 0 {
		t.Fatalf("all hard_gates met → want empty; got %+v", got)
	}

	// A course with no prerequisites is always allowed (nil completion set).
	if got, _ := svc.UnmetHardGates(ctx, preTenantID, preCourseB, nil); len(got) != 0 {
		t.Fatalf("no prereqs → want empty; got %+v", got)
	}
}

// -----------------------------------------------------------------------------
// InMemCoursePrerequisiteStore
// -----------------------------------------------------------------------------

func TestInMemCoursePrerequisiteStore_TenantScopedUpsertList(t *testing.T) {
	ctx := context.Background()
	st := NewInMemCoursePrerequisiteStore()

	if err := st.Upsert(ctx, preTenantID, edge(preCourseA, preCourseB)); err != nil {
		t.Fatalf("upsert t1: %v", err)
	}
	if err := st.Upsert(ctx, preTenantOther, edge(preCourseA, preCourseC)); err != nil {
		t.Fatalf("upsert t2: %v", err)
	}
	// tenant-1 sees only its edge
	got, err := st.ListForCourse(ctx, preTenantID, preCourseA)
	if err != nil {
		t.Fatalf("list t1: %v", err)
	}
	if len(got) != 1 || got[0].PrerequisiteCourseID != preCourseB {
		t.Fatalf("t1 ListForCourse = %+v, want [B]", got)
	}
	// tenant-2 isolated
	got2, _ := st.ListForCourse(ctx, preTenantOther, preCourseA)
	if len(got2) != 1 || got2[0].PrerequisiteCourseID != preCourseC {
		t.Fatalf("t2 ListForCourse = %+v, want [C]", got2)
	}
	// ListForTenant scoping
	all1, _ := st.ListForTenant(ctx, preTenantID)
	if len(all1) != 1 || all1[0].CourseID != preCourseA {
		t.Fatalf("t1 ListForTenant = %+v, want 1 edge", all1)
	}
}

func TestInMemCoursePrerequisiteStore_UpsertIdempotentKindUpdate(t *testing.T) {
	ctx := context.Background()
	st := NewInMemCoursePrerequisiteStore()
	_ = st.Upsert(ctx, preTenantID, PrerequisiteEdge{CourseID: preCourseA, PrerequisiteCourseID: preCourseB, Kind: PrereqKindHardGate})
	_ = st.Upsert(ctx, preTenantID, PrerequisiteEdge{CourseID: preCourseA, PrerequisiteCourseID: preCourseB, Kind: PrereqKindAdvisory})
	got, _ := st.ListForCourse(ctx, preTenantID, preCourseA)
	if len(got) != 1 {
		t.Fatalf("idempotent upsert produced %d edges, want 1", len(got))
	}
	if got[0].Kind != PrereqKindAdvisory {
		t.Fatalf("kind not updated: got %q want advisory", got[0].Kind)
	}
}

func TestInMemCoursePrerequisiteStore_RemoveSoftDeletesAndReAdd(t *testing.T) {
	ctx := context.Background()
	st := NewInMemCoursePrerequisiteStore()
	_ = st.Upsert(ctx, preTenantID, edge(preCourseA, preCourseB))
	if err := st.Remove(ctx, preTenantID, preCourseA, preCourseB); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, _ := st.ListForCourse(ctx, preTenantID, preCourseA)
	if len(got) != 0 {
		t.Fatalf("after remove ListForCourse = %+v, want empty", got)
	}
	// removing again is a no-op (idempotent)
	if err := st.Remove(ctx, preTenantID, preCourseA, preCourseB); err != nil {
		t.Fatalf("idempotent remove: %v", err)
	}
	// re-add after soft-delete works
	if err := st.Upsert(ctx, preTenantID, edge(preCourseA, preCourseB)); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	got2, _ := st.ListForCourse(ctx, preTenantID, preCourseA)
	if len(got2) != 1 {
		t.Fatalf("re-add ListForCourse = %+v, want 1", got2)
	}
}

// -----------------------------------------------------------------------------
// CoursePrerequisiteService
// -----------------------------------------------------------------------------

func TestPrereqService_Add_HappyPath(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := prereqTestSvc(t, preTenantID)
	got, err := svc.Add(ctx, preTenantID, preCourseA, preCourseB, PrereqKindHardGate)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(got) != 1 || got[0].PrerequisiteCourseID != preCourseB || got[0].Kind != PrereqKindHardGate {
		t.Fatalf("Add returned %+v, want [B/hard_gate]", got)
	}
}

func TestPrereqService_Add_UnknownCourses(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := prereqTestSvc(t, preTenantID)
	const ghost = "019e2f93-d586-71b5-8c3d-e2b0d0d5ffff"
	// unknown target
	if _, err := svc.Add(ctx, preTenantID, preCourseA, ghost, PrereqKindHardGate); !errors.Is(err, ErrPrerequisiteUnknownCourse) {
		t.Fatalf("unknown target err = %v, want ErrPrerequisiteUnknownCourse", err)
	}
	// unknown source
	if _, err := svc.Add(ctx, preTenantID, ghost, preCourseA, PrereqKindHardGate); !errors.Is(err, ErrPrerequisiteUnknownCourse) {
		t.Fatalf("unknown source err = %v, want ErrPrerequisiteUnknownCourse", err)
	}
}

func TestPrereqService_Add_Rejections(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := prereqTestSvc(t, preTenantID)

	// self-edge
	if _, err := svc.Add(ctx, preTenantID, preCourseA, preCourseA, PrereqKindHardGate); !errors.Is(err, ErrPrerequisiteSelfEdge) {
		t.Fatalf("self-edge err = %v", err)
	}
	// bad kind
	if _, err := svc.Add(ctx, preTenantID, preCourseA, preCourseB, PrereqKind("nope")); !errors.Is(err, ErrPrerequisiteKindInvalid) {
		t.Fatalf("bad kind err = %v", err)
	}
	// cycle: A requires B, then B requires A
	if _, err := svc.Add(ctx, preTenantID, preCourseA, preCourseB, PrereqKindHardGate); err != nil {
		t.Fatalf("seed A->B: %v", err)
	}
	if _, err := svc.Add(ctx, preTenantID, preCourseB, preCourseA, PrereqKindHardGate); !errors.Is(err, ErrPrerequisiteCycle) {
		t.Fatalf("cycle err = %v, want ErrPrerequisiteCycle", err)
	}
}

func TestPrereqService_Add_CapExceeded(t *testing.T) {
	ctx := context.Background()
	edges := NewInMemCoursePrerequisiteStore()
	courses := NewInMemCourseCJ2Store()
	for _, id := range []string{preCourseA, preCourseB, preCourseC, preCourseD} {
		seedCourse(t, courses, preTenantID, id)
	}
	svc := NewCoursePrerequisiteService(edges, courses, 2) // cap = 2
	if _, err := svc.Add(ctx, preTenantID, preCourseA, preCourseB, PrereqKindHardGate); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Add(ctx, preTenantID, preCourseA, preCourseC, PrereqKindHardGate); err != nil {
		t.Fatal(err)
	}
	// 3rd distinct edge exceeds cap 2
	if _, err := svc.Add(ctx, preTenantID, preCourseA, preCourseD, PrereqKindHardGate); !errors.Is(err, ErrPrerequisiteCapExceeded) {
		t.Fatalf("cap err = %v, want ErrPrerequisiteCapExceeded", err)
	}
}

func TestPrereqService_Add_IdempotentKindUpdateSkipsCap(t *testing.T) {
	ctx := context.Background()
	edges := NewInMemCoursePrerequisiteStore()
	courses := NewInMemCourseCJ2Store()
	for _, id := range []string{preCourseA, preCourseB} {
		seedCourse(t, courses, preTenantID, id)
	}
	svc := NewCoursePrerequisiteService(edges, courses, 1) // cap = 1
	if _, err := svc.Add(ctx, preTenantID, preCourseA, preCourseB, PrereqKindHardGate); err != nil {
		t.Fatal(err)
	}
	// re-add same edge with a new kind: idempotent update, must NOT trip the cap
	got, err := svc.Add(ctx, preTenantID, preCourseA, preCourseB, PrereqKindAdvisory)
	if err != nil {
		t.Fatalf("idempotent re-add err = %v", err)
	}
	if len(got) != 1 || got[0].Kind != PrereqKindAdvisory {
		t.Fatalf("re-add = %+v, want [B/advisory]", got)
	}
}

func TestPrereqService_GuardsAndDefaults(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := prereqTestSvc(t, preTenantID)

	// List / Remove / Add reject empty ids up front (before any repo call).
	if _, err := svc.List(ctx, preTenantID, ""); !errors.Is(err, ErrPrerequisiteCourseRequired) {
		t.Fatalf("List empty course err = %v", err)
	}
	if _, err := svc.Remove(ctx, preTenantID, preCourseA, ""); !errors.Is(err, ErrPrerequisiteCourseRequired) {
		t.Fatalf("Remove empty prereq err = %v", err)
	}
	if _, err := svc.Add(ctx, preTenantID, "", preCourseB, PrereqKindHardGate); !errors.Is(err, ErrPrerequisiteCourseRequired) {
		t.Fatalf("Add empty source err = %v", err)
	}

	// NewCoursePrerequisiteService with a non-positive cap falls back to the
	// domain default (32), NOT 0 — so three edges add freely.
	edges := NewInMemCoursePrerequisiteStore()
	courses := NewInMemCourseCJ2Store()
	for _, id := range []string{preCourseA, preCourseB, preCourseC, preCourseD} {
		seedCourse(t, courses, preTenantID, id)
	}
	defSvc := NewCoursePrerequisiteService(edges, courses, 0) // 0 ⇒ default 32
	for _, target := range []string{preCourseB, preCourseC, preCourseD} {
		if _, err := defSvc.Add(ctx, preTenantID, preCourseA, target, PrereqKindHardGate); err != nil {
			t.Fatalf("default-cap Add %s: %v", target, err)
		}
	}
	got, _ := defSvc.List(ctx, preTenantID, preCourseA)
	if len(got) != 3 {
		t.Fatalf("default-cap service capped early: got %d edges, want 3", len(got))
	}
}

func TestPrereqService_RemoveAndList(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := prereqTestSvc(t, preTenantID)
	_, _ = svc.Add(ctx, preTenantID, preCourseA, preCourseB, PrereqKindHardGate)
	_, _ = svc.Add(ctx, preTenantID, preCourseA, preCourseC, PrereqKindAdvisory)

	list, err := svc.List(ctx, preTenantID, preCourseA)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List = %d edges, want 2", len(list))
	}

	got, err := svc.Remove(ctx, preTenantID, preCourseA, preCourseB)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(got) != 1 || got[0].PrerequisiteCourseID != preCourseC {
		t.Fatalf("after Remove = %+v, want [C]", got)
	}
	// idempotent remove
	if _, err := svc.Remove(ctx, preTenantID, preCourseA, preCourseB); err != nil {
		t.Fatalf("idempotent Remove: %v", err)
	}
}
