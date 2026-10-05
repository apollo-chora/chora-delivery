// Enrollment tests — Phyllis MVP (Comic Ch4 P8 + Ch5 P10 P3).
//
// Same-identity invariant: POST /enrollments must NOT create a new user;
// it just adds a row to the Enrollment registry linking gcid + course_id.
// The role context (instructor on own course, learner on enrolled course)
// is computed by chora-identity downstream; chora-delivery just stores the
// enrollment row.
//
// Comic anchor (Ch4 P8): "instructor on CSPO + learner on CSM, same gcid".
package delivery_test

import (
	"context"
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// NewEnrollment
// -----------------------------------------------------------------------------

func TestNewEnrollment_AssignsUUIDv7AndDefaults(t *testing.T) {
	t.Parallel()

	e, err := domain.NewEnrollment(tenantA, "course-1", gcidA)
	if err != nil {
		t.Fatalf("NewEnrollment: %v", err)
	}
	if e.ID == "" {
		t.Fatalf("expected non-empty ID")
	}
	if e.CourseID != "course-1" {
		t.Fatalf("course_id mismatch")
	}
	if e.GCID != gcidA {
		t.Fatalf("gcid mismatch")
	}
	if e.EnrolledAt.IsZero() {
		t.Fatalf("expected EnrolledAt set")
	}
	if e.DeletedAt != nil {
		t.Fatalf("expected fresh enrollment not soft-deleted")
	}
}

func TestNewEnrollment_RejectsEmptyTenant(t *testing.T) {
	t.Parallel()
	if _, err := domain.NewEnrollment("", "course-1", gcidA); err == nil {
		t.Fatalf("expected error for empty tenant")
	}
}

func TestNewEnrollment_RejectsEmptyCourse(t *testing.T) {
	t.Parallel()
	if _, err := domain.NewEnrollment(tenantA, "", gcidA); err == nil {
		t.Fatalf("expected error for empty course_id")
	}
}

func TestNewEnrollment_RejectsEmptyGCID(t *testing.T) {
	t.Parallel()
	if _, err := domain.NewEnrollment(tenantA, "course-1", ""); err == nil {
		t.Fatalf("expected error for empty gcid")
	}
}

// -----------------------------------------------------------------------------
// EnrollmentRegistry — same-identity invariant + idempotency
// -----------------------------------------------------------------------------

func TestEnrollmentRegistry_RegisterIsIdempotent(t *testing.T) {
	t.Parallel()

	reg := domain.NewEnrollmentRegistry()
	e1, err := reg.Register(tenantA, "course-1", gcidA)
	if err != nil {
		t.Fatalf("Register #1: %v", err)
	}
	e2, err := reg.Register(tenantA, "course-1", gcidA)
	if err != nil {
		t.Fatalf("Register #2 (idempotent retry): unexpected error: %v", err)
	}
	if e1.ID != e2.ID {
		t.Fatalf("idempotent re-register should return same enrollment ID; got %q vs %q", e1.ID, e2.ID)
	}
}

// Same-identity invariant (Comic Ch4 P8):
//
// Phyllis is the instructor on her CSPO course (created it) AND the learner
// on Mr. Chen's CSM course (enrolled). The enrollment row stores HER gcid
// for the CSM course; identity computes role per-course downstream.
//
// This test asserts both rows live in the registry under the SAME gcid —
// no new identity gets created.
func TestEnrollmentRegistry_SameIdentityAcrossCourses(t *testing.T) {
	t.Parallel()

	reg := domain.NewEnrollmentRegistry()

	// Phyllis enrolls in Mr. Chen's CSM course (as a learner).
	csm, err := reg.Register(tenantA, "course-csm", gcidA /* phyllis */)
	if err != nil {
		t.Fatalf("Register CSM: %v", err)
	}

	// Phyllis enrolls in her own CSPO course (the registry doesn't care
	// about the role — same gcid still holds an enrollment row).
	cspo, err := reg.Register(tenantA, "course-cspo", gcidA /* phyllis */)
	if err != nil {
		t.Fatalf("Register CSPO: %v", err)
	}

	if csm.GCID != cspo.GCID {
		t.Fatalf("same-identity invariant: CSM gcid=%q, CSPO gcid=%q (must match)", csm.GCID, cspo.GCID)
	}
	if csm.GCID != gcidA {
		t.Fatalf("expected gcid to remain phyllis (%s), got %s", gcidA, csm.GCID)
	}
	if csm.ID == cspo.ID {
		t.Fatalf("two different course enrollments must have distinct IDs")
	}
}

// -----------------------------------------------------------------------------
// Listing for a learner ("My Enrollments") — Comic Ch4 P3 dual-card view
// -----------------------------------------------------------------------------

func TestEnrollmentRegistry_ListByGCID(t *testing.T) {
	t.Parallel()

	reg := domain.NewEnrollmentRegistry()
	if _, err := reg.Register(tenantA, "course-csm", gcidA); err != nil {
		t.Fatalf("seed CSM: %v", err)
	}
	if _, err := reg.Register(tenantA, "course-cspo", gcidA); err != nil {
		t.Fatalf("seed CSPO: %v", err)
	}
	if _, err := reg.Register(tenantA, "course-other", gcidB); err != nil {
		t.Fatalf("seed other-learner: %v", err)
	}

	mine := reg.ListByGCID(tenantA, gcidA)
	if len(mine) != 2 {
		t.Fatalf("expected 2 enrollments for gcidA, got %d", len(mine))
	}

	other := reg.ListByGCID(tenantA, gcidB)
	if len(other) != 1 {
		t.Fatalf("expected 1 enrollment for gcidB, got %d", len(other))
	}
}

func TestEnrollmentRegistry_ListByGCID_TenantIsolation(t *testing.T) {
	t.Parallel()
	reg := domain.NewEnrollmentRegistry()
	tenantOther := "01970000-0000-7000-8000-0000000000FF"
	if _, err := reg.Register(tenantA, "course-csm", gcidA); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := reg.Register(tenantOther, "course-x", gcidA); err != nil {
		t.Fatalf("seed: %v", err)
	}
	out := reg.ListByGCID(tenantA, gcidA)
	if len(out) != 1 {
		t.Fatalf("expected tenant-isolated list of 1, got %d", len(out))
	}
}

func TestEnrollmentRegistry_GetByCourseAndGCID(t *testing.T) {
	t.Parallel()
	reg := domain.NewEnrollmentRegistry()
	created, _ := reg.Register(tenantA, "course-1", gcidA)
	got, ok := reg.GetByCourseAndGCID(tenantA, "course-1", gcidA)
	if !ok {
		t.Fatalf("expected hit")
	}
	if got.ID != created.ID {
		t.Fatalf("ID mismatch")
	}
	if _, ok := reg.GetByCourseAndGCID(tenantA, "course-1", gcidB); ok {
		t.Fatalf("expected miss for different gcid")
	}
	if _, ok := reg.GetByCourseAndGCID(tenantA, "course-2", gcidA); ok {
		t.Fatalf("expected miss for different course")
	}
}

func TestEnrollmentRegistry_CountByCourse(t *testing.T) {
	t.Parallel()
	reg := domain.NewEnrollmentRegistry()
	for _, g := range []string{gcidA, gcidB, gcidC} {
		if _, err := reg.Register(tenantA, "course-1", g); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if got := reg.CountByCourse(tenantA, "course-1"); got != 3 {
		t.Fatalf("expected 3, got %d", got)
	}
	if got := reg.CountByCourse(tenantA, "course-2"); got != 0 {
		t.Fatalf("expected 0 for unknown course, got %d", got)
	}
}

func TestEnrollmentRegistry_GetByID(t *testing.T) {
	t.Parallel()
	reg := domain.NewEnrollmentRegistry()
	created, _ := reg.Register(tenantA, "course-1", gcidA)
	got, ok := reg.Get(created.ID)
	if !ok {
		t.Fatalf("expected hit")
	}
	if got.ID != created.ID {
		t.Fatalf("ID mismatch")
	}
	if _, ok := reg.Get("does-not-exist"); ok {
		t.Fatalf("expected miss")
	}
}

func TestEnrollmentRegistry_RejectsBlankInput(t *testing.T) {
	t.Parallel()
	reg := domain.NewEnrollmentRegistry()
	if _, err := reg.Register("", "c", gcidA); err == nil {
		t.Fatalf("expected error for empty tenant")
	}
	if _, err := reg.Register(tenantA, "", gcidA); err == nil {
		t.Fatalf("expected error for empty course")
	}
	if _, err := reg.Register(tenantA, "c", ""); err == nil {
		t.Fatalf("expected error for empty gcid")
	}
}

// -----------------------------------------------------------------------------
// InMemEnrollmentStore.Cancel — the EnrollmentPort persist path that the pg
// adapter needs. Mutating a loaded aggregate is not enough on Postgres (Get
// materialises a fresh struct); Cancel is the write-back. In-mem, Get returns
// the live pointer so the soft-delete is observable via GetByCourseAndGCID.
// -----------------------------------------------------------------------------

func TestInMemEnrollmentStore_Cancel_SoftDeletesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := domain.NewInMemEnrollmentStore()

	e, err := store.Register(ctx, tenantA, "course-1", gcidA)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Precondition: the row is live.
	if _, ok, _ := store.GetByCourseAndGCID(ctx, tenantA, "course-1", gcidA); !ok {
		t.Fatalf("precondition: expected live enrolment before cancel")
	}

	if err := store.Cancel(ctx, tenantA, e.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// The soft-delete must be observable through the port — the enrolment is
	// gone from active reads (proves persistence, not just in-struct mutation).
	if _, ok, _ := store.GetByCourseAndGCID(ctx, tenantA, "course-1", gcidA); ok {
		t.Fatalf("Cancel: expected enrolment gone from store after cancel")
	}
	if _, ok, _ := store.Get(ctx, e.ID); ok {
		t.Fatalf("Cancel: expected Get miss after cancel")
	}

	// Idempotent: cancelling an already-cancelled row is a no-op returning nil.
	if err := store.Cancel(ctx, tenantA, e.ID); err != nil {
		t.Fatalf("Cancel (idempotent re-cancel): %v", err)
	}
}

func TestInMemEnrollmentStore_Cancel_AbsentRowIsNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := domain.NewInMemEnrollmentStore()
	// Cancelling an enrolment_id that was never registered returns nil (no-op).
	if err := store.Cancel(ctx, tenantA, "01970000-0000-7000-9999-ffffffffffff"); err != nil {
		t.Fatalf("Cancel absent row: expected nil, got %v", err)
	}
}

func TestInMemEnrollmentStore_Cancel_WrongTenantIsNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := domain.NewInMemEnrollmentStore()
	e, err := store.Register(ctx, tenantA, "course-1", gcidA)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Cancel under a DIFFERENT tenant must NOT soft-delete tenantA's row.
	tenantOther := "01970000-0000-7000-8000-0000000000FF"
	if err := store.Cancel(ctx, tenantOther, e.ID); err != nil {
		t.Fatalf("Cancel wrong tenant: %v", err)
	}
	if _, ok, _ := store.GetByCourseAndGCID(ctx, tenantA, "course-1", gcidA); !ok {
		t.Fatalf("Cancel wrong tenant must be a no-op; tenantA row was deleted")
	}
}

func TestEnrollment_SoftDelete(t *testing.T) {
	t.Parallel()
	e, _ := domain.NewEnrollment(tenantA, "c", gcidA)
	if e.DeletedAt != nil {
		t.Fatalf("fresh enrollment should not be soft-deleted")
	}
	e.SoftDelete()
	if e.DeletedAt == nil {
		t.Fatalf("expected DeletedAt set")
	}
	first := *e.DeletedAt
	e.SoftDelete()
	if !e.DeletedAt.Equal(first) {
		t.Fatalf("SoftDelete must be idempotent")
	}
}
