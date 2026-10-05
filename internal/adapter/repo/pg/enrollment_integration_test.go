//go:build integration

// enrollment_integration_test.go — exercises the EnrollmentRepo round-trip
// against live Cloud SQL via the Cloud SQL Auth Proxy.
//
// Run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration -run TestIntegration_EnrollmentRepo \
//	  ./services/chora-delivery/internal/adapter/repo/pg/...
//
// Mirrors course_integration_test.go (live pgxpool via liveDB(t) + the
// liveTxRunner adapter declared there — same _test package). The
// integration build tag keeps these off the default unit-test run; CI
// invokes them when the live-DB env vars are wired (Cloud Build per
// [[feedback-no-local-cicd-run]]).
//
// Suite:
//
//  1. Register -> GetByCourseAndGCID round-trip (insert + read same row).
//  2. RLS isolation: Register under A; read under B must miss.
//  3. Idempotent Register: 2 calls with same (course, gcid) return same id.
//  4. ListByGCID returns the active set in enrolled_at DESC order.
package pg_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
)

// seedCoursesRowForEnrolmentTest writes a minimal courses row so the
// course_enrollments.course_id_fkey constraint accepts the enrolment
// insert. Cleanup is registered via t.Cleanup so the row + any enrolments
// FK'd to it are removed after the test, leaving the dev DB pristine.
func seedCoursesRowForEnrolmentTest(t *testing.T, pool *pgxpool.Pool, tenantID, courseID, instructorGCID string) {
	t.Helper()
	ctx := context.Background()
	// SET LOCAL is per-tx; use a session-level set via an explicit tx so
	// the INSERT lands under tenant_id RLS.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("pool.Begin (seed): %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL chora.tenant_id = '"+tenantID+"'"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("SET LOCAL (seed): %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO courses (course_id, tenant_id, instructor_gcid, title, atom_ids,
    public, price_sgd_cents, sf_eligible, max_capacity, created_at, updated_at)
VALUES ($1, $2, $3, 'enrolment-test course', '{}', false, 0, false, 100, now(), now())
ON CONFLICT (course_id) DO NOTHING
`, courseID, tenantID, instructorGCID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("INSERT courses (seed): %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit (seed): %v", err)
	}

	t.Cleanup(func() {
		cctx := context.Background()
		cleanupTx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer func() { _ = cleanupTx.Rollback(cctx) }()
		_, _ = cleanupTx.Exec(cctx, "SET LOCAL chora.tenant_id = '"+tenantID+"'")
		_, _ = cleanupTx.Exec(cctx, `DELETE FROM course_enrollments WHERE course_id = $1`, courseID)
		_, _ = cleanupTx.Exec(cctx, `DELETE FROM courses WHERE course_id = $1`, courseID)
		_ = cleanupTx.Commit(cctx)
	})
}

// -----------------------------------------------------------------------------
// 1. Register -> GetByCourseAndGCID round-trip
// -----------------------------------------------------------------------------

func TestIntegration_EnrollmentRepo_RegisterGet_Roundtrip(t *testing.T) {
	pool := liveDB(t)
	tx := &liveTxRunner{pool: pool}
	r := pg.NewEnrollmentRepo(tx)

	tenantA, _ := uuid.NewV7()
	courseID, _ := uuid.NewV7()
	instructorGCID, _ := uuid.NewV7()
	learnerGCID, _ := uuid.NewV7()

	seedCoursesRowForEnrolmentTest(t, pool, tenantA.String(), courseID.String(), instructorGCID.String())

	ctx := tracing.WithTenantID(context.Background(), tenantA.String())
	enr, err := r.Register(ctx, tenantA.String(), courseID.String(), learnerGCID.String())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if enr == nil || enr.ID == "" {
		t.Fatalf("Register: empty enrollment id on fresh insert")
	}

	got, ok, err := r.GetByCourseAndGCID(ctx, tenantA.String(), courseID.String(), learnerGCID.String())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || got == nil {
		t.Fatalf("expected ok=true on round-trip")
	}
	if got.ID != enr.ID {
		t.Fatalf("round-trip id mismatch: insert=%s got=%s", enr.ID, got.ID)
	}
}

// -----------------------------------------------------------------------------
// 2. RLS isolation: Register under A, read under B -> miss
// -----------------------------------------------------------------------------

func TestIntegration_EnrollmentRepo_RLS_TenantIsolation(t *testing.T) {
	pool := liveDB(t)
	tx := &liveTxRunner{pool: pool}
	r := pg.NewEnrollmentRepo(tx)

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	courseID, _ := uuid.NewV7()
	instructorGCID, _ := uuid.NewV7()
	learnerGCID, _ := uuid.NewV7()

	seedCoursesRowForEnrolmentTest(t, pool, tenantA.String(), courseID.String(), instructorGCID.String())

	ctxA := tracing.WithTenantID(context.Background(), tenantA.String())
	if _, err := r.Register(ctxA, tenantA.String(), courseID.String(), learnerGCID.String()); err != nil {
		t.Fatalf("Register under A: %v", err)
	}

	// Read under B -> miss.
	ctxB := tracing.WithTenantID(context.Background(), tenantB.String())
	got, ok, err := r.GetByCourseAndGCID(ctxB, tenantB.String(), courseID.String(), learnerGCID.String())
	if err != nil {
		t.Fatalf("Get under B: %v", err)
	}
	if ok || got != nil {
		t.Fatalf("RLS LEAK: tenant B saw tenant A's enrolment")
	}

	// Read under A -> hit.
	gotA, okA, err := r.GetByCourseAndGCID(ctxA, tenantA.String(), courseID.String(), learnerGCID.String())
	if err != nil {
		t.Fatalf("Get under A: %v", err)
	}
	if !okA || gotA == nil {
		t.Fatalf("expected tenant A to see its own enrolment")
	}
	t.Logf("RLS isolation OK at the repo layer: A=ok B=hidden")
}

// -----------------------------------------------------------------------------
// 3. Idempotent Register: same (tenant, course, gcid) -> same id
// -----------------------------------------------------------------------------

func TestIntegration_EnrollmentRepo_Register_IsIdempotent(t *testing.T) {
	pool := liveDB(t)
	tx := &liveTxRunner{pool: pool}
	r := pg.NewEnrollmentRepo(tx)

	tenantA, _ := uuid.NewV7()
	courseID, _ := uuid.NewV7()
	instructorGCID, _ := uuid.NewV7()
	learnerGCID, _ := uuid.NewV7()

	seedCoursesRowForEnrolmentTest(t, pool, tenantA.String(), courseID.String(), instructorGCID.String())

	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	first, err := r.Register(ctx, tenantA.String(), courseID.String(), learnerGCID.String())
	if err != nil {
		t.Fatalf("Register #1: %v", err)
	}
	second, err := r.Register(ctx, tenantA.String(), courseID.String(), learnerGCID.String())
	if err != nil {
		t.Fatalf("Register #2: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotency violated: first.ID=%s second.ID=%s", first.ID, second.ID)
	}

	// Count must be 1, not 2 — UNIQUE(course_id, gcid) holds.
	count, err := r.CountByCourse(ctx, tenantA.String(), courseID.String())
	if err != nil {
		t.Fatalf("CountByCourse: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected count=1 on idempotent re-register; got %d", count)
	}
}

// -----------------------------------------------------------------------------
// 4. ListByGCID returns active set in enrolled_at DESC order
// -----------------------------------------------------------------------------

func TestIntegration_EnrollmentRepo_ListByGCID_DESCOrder(t *testing.T) {
	pool := liveDB(t)
	tx := &liveTxRunner{pool: pool}
	r := pg.NewEnrollmentRepo(tx)

	tenantA, _ := uuid.NewV7()
	course1, _ := uuid.NewV7()
	course2, _ := uuid.NewV7()
	instructorGCID, _ := uuid.NewV7()
	learnerGCID, _ := uuid.NewV7()

	seedCoursesRowForEnrolmentTest(t, pool, tenantA.String(), course1.String(), instructorGCID.String())
	seedCoursesRowForEnrolmentTest(t, pool, tenantA.String(), course2.String(), instructorGCID.String())

	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	if _, err := r.Register(ctx, tenantA.String(), course1.String(), learnerGCID.String()); err != nil {
		t.Fatalf("Register #1: %v", err)
	}
	if _, err := r.Register(ctx, tenantA.String(), course2.String(), learnerGCID.String()); err != nil {
		t.Fatalf("Register #2: %v", err)
	}

	out, err := r.ListByGCID(ctx, tenantA.String(), learnerGCID.String())
	if err != nil {
		t.Fatalf("ListByGCID: %v", err)
	}
	if len(out) < 2 {
		t.Fatalf("expected at least 2 enrolments; got %d", len(out))
	}
	// course2 registered later -> should appear at index 0 (DESC order on
	// enrolled_at). Allow equal-timestamp ties as still-valid (real Cloud
	// SQL distinguishes microsecond-precision now()).
	if out[0].EnrolledAt.Before(out[len(out)-1].EnrolledAt) {
		t.Fatalf("expected DESC order on enrolled_at; first=%v last=%v",
			out[0].EnrolledAt, out[len(out)-1].EnrolledAt)
	}
}

// -----------------------------------------------------------------------------
// 5. ListByCourse returns the course's learners, RLS-isolated by tenant
// -----------------------------------------------------------------------------

func TestIntegration_EnrollmentRepo_ListByCourse_TenantScoped(t *testing.T) {
	pool := liveDB(t)
	tx := &liveTxRunner{pool: pool}
	r := pg.NewEnrollmentRepo(tx)

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	courseID, _ := uuid.NewV7()
	instructorGCID, _ := uuid.NewV7()
	learner1, _ := uuid.NewV7()
	learner2, _ := uuid.NewV7()

	seedCoursesRowForEnrolmentTest(t, pool, tenantA.String(), courseID.String(), instructorGCID.String())

	ctxA := tracing.WithTenantID(context.Background(), tenantA.String())
	if _, err := r.Register(ctxA, tenantA.String(), courseID.String(), learner1.String()); err != nil {
		t.Fatalf("Register learner1: %v", err)
	}
	if _, err := r.Register(ctxA, tenantA.String(), courseID.String(), learner2.String()); err != nil {
		t.Fatalf("Register learner2: %v", err)
	}

	// Tenant A sees both learners enrolled in the course.
	rosterA, err := r.ListByCourse(ctxA, tenantA.String(), courseID.String())
	if err != nil {
		t.Fatalf("ListByCourse under A: %v", err)
	}
	if len(rosterA) != 2 {
		t.Fatalf("expected 2 learners on the course under tenant A; got %d", len(rosterA))
	}
	seen := map[string]bool{}
	for _, e := range rosterA {
		if e.CourseID != courseID.String() {
			t.Fatalf("cross-course leak: got course %s", e.CourseID)
		}
		if e.TenantID != tenantA.String() {
			t.Fatalf("cross-tenant leak: got tenant %s", e.TenantID)
		}
		seen[e.GCID] = true
	}
	if !seen[learner1.String()] || !seen[learner2.String()] {
		t.Fatalf("roster missing an enrolled learner; seen=%v", seen)
	}
	// ASC order on enrolled_at — oldest first.
	if rosterA[0].EnrolledAt.After(rosterA[len(rosterA)-1].EnrolledAt) {
		t.Fatalf("expected ASC order on enrolled_at; first=%v last=%v",
			rosterA[0].EnrolledAt, rosterA[len(rosterA)-1].EnrolledAt)
	}

	// Tenant B (no enrolments on this course; RLS hides A's rows) -> empty.
	ctxB := tracing.WithTenantID(context.Background(), tenantB.String())
	rosterB, err := r.ListByCourse(ctxB, tenantB.String(), courseID.String())
	if err != nil {
		t.Fatalf("ListByCourse under B: %v", err)
	}
	if len(rosterB) != 0 {
		t.Fatalf("RLS LEAK: tenant B saw %d of tenant A's enrolments via ListByCourse", len(rosterB))
	}
	t.Logf("ListByCourse RLS isolation OK: A=2 learners, B=0")
}

// -----------------------------------------------------------------------------
// 6. Re-enrol after unenrol REVIVES the soft-deleted row (CHO-2161)
// -----------------------------------------------------------------------------
//
// Unenrol soft-deletes the row, but UNIQUE(course_id, gcid) is a PLAIN index —
// it does NOT exclude tombstones. So a re-enrol's INSERT conflicts with the
// soft-deleted row; with ON CONFLICT DO NOTHING the RETURNING clause yields no
// row, and the natural-key fallback SELECT filters deleted_at IS NULL and finds
// nothing => "conflict but natural-key lookup empty". An unenrolled learner
// could never be re-enrolled.
//
// Register must instead REVIVE the tombstone in place (never hard-delete, never
// widen the unique index).
func TestIntegration_EnrollmentRepo_ReenrolAfterCancel_RevivesRow(t *testing.T) {
	pool := liveDB(t)
	tx := &liveTxRunner{pool: pool}
	r := pg.NewEnrollmentRepo(tx)

	tenantA, _ := uuid.NewV7()
	courseID, _ := uuid.NewV7()
	instructorGCID, _ := uuid.NewV7()
	learnerGCID, _ := uuid.NewV7()

	seedCoursesRowForEnrolmentTest(t, pool, tenantA.String(), courseID.String(), instructorGCID.String())
	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	first, err := r.Register(ctx, tenantA.String(), courseID.String(), learnerGCID.String())
	if err != nil {
		t.Fatalf("Register #1: %v", err)
	}

	// Unenrol — soft-delete.
	if err := r.Cancel(ctx, tenantA.String(), first.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, ok, gerr := r.GetByCourseAndGCID(ctx, tenantA.String(), courseID.String(), learnerGCID.String()); gerr != nil || ok {
		t.Fatalf("after Cancel: want invisible to active reads, got ok=%v err=%v", ok, gerr)
	}

	// Re-enrol — must revive the tombstone, not error.
	revived, err := r.Register(ctx, tenantA.String(), courseID.String(), learnerGCID.String())
	if err != nil {
		t.Fatalf("re-enrol after cancel must revive, got error: %v", err)
	}
	if revived == nil {
		t.Fatalf("re-enrol after cancel returned nil enrolment")
	}

	// The revived row must be visible + active again.
	got, ok, gerr := r.GetByCourseAndGCID(ctx, tenantA.String(), courseID.String(), learnerGCID.String())
	if gerr != nil || !ok {
		t.Fatalf("after revive: want an active enrolment, got ok=%v err=%v", ok, gerr)
	}
	if got.Status != "active" {
		t.Fatalf("revived status: want active, got %q", got.Status)
	}

	// Still exactly one physical row — the tombstone was revived, not duplicated.
	count, err := r.CountByCourse(ctx, tenantA.String(), courseID.String())
	if err != nil {
		t.Fatalf("CountByCourse: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 enrolment after revive, got %d", count)
	}
}
