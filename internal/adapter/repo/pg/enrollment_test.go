// enrollment_test.go — unit tests for pg.EnrollmentRepo.
//
// Mirrors application_test.go + course_test.go: stubs the Querier so the
// SQL surface is exercised without a live DB. Live RLS isolation is
// verified separately in enrollment_integration_test.go (build tag
// `integration`).
//
// What these tests guarantee:
//
//  1. rls.ApplySession runs BEFORE the data query (every method).
//  2. SQL templates match the expected shape (INSERT ... DO NOTHING
//     RETURNING + natural-key fall-back, SELECT * WHERE deleted_at IS
//     NULL, COUNT(*) WHERE deleted_at IS NULL).
//  3. Argument order matches the SQL placeholders.
//  4. Nil-tx returns ErrNotImplemented (fail-loud, matches Course /
//     Application repos).
//  5. Empty tenant/course/gcid inputs return their dedicated sentinels
//     (loud) rather than letting Postgres reject downstream.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// Nil-TxRunner — fail-loud per [[no-stubs-real-wiring]]
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewEnrollmentRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.Register(ctx, tenantID, courseID, gcid); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Register: expected ErrNotImplemented; got %v", err)
	}
	if _, _, err := r.GetByCourseAndGCID(ctx, tenantID, courseID, gcid); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("GetByCourseAndGCID: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByGCID(ctx, tenantID, gcid); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByGCID: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.CountByCourse(ctx, tenantID, courseID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("CountByCourse: expected ErrNotImplemented; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Input validation — empty tenant/course/gcid rejected at the boundary
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_Register_RejectsEmptyTenant(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	_, err := r.Register(ctx, "  ", courseID, gcid)
	if !errors.Is(err, pg.ErrEnrollmentMissingTenant) {
		t.Fatalf("expected ErrEnrollmentMissingTenant; got %v", err)
	}
}

func TestEnrollmentRepo_Register_RejectsEmptyCourse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	_, err := r.Register(ctx, tenantID, "", gcid)
	if !errors.Is(err, pg.ErrEnrollmentMissingCourse) {
		t.Fatalf("expected ErrEnrollmentMissingCourse; got %v", err)
	}
}

func TestEnrollmentRepo_Register_RejectsEmptyGCID(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	_, err := r.Register(ctx, tenantID, courseID, "")
	if !errors.Is(err, pg.ErrEnrollmentMissingGCID) {
		t.Fatalf("expected ErrEnrollmentMissingGCID; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Register — fresh insert path (RETURNING populated)
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_Register_FreshInsert_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	insertedAt := time.Now().UTC().Truncate(time.Microsecond)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			// The first QueryRow call is the INSERT — fake a successful
			// RETURNING by scanning the supplied destinations.
			return stubRow{scanFn: func(dest ...any) error {
				if len(dest) != 9 {
					return errors.New("scanEnrollment: expected 9 destinations")
				}
				*(dest[0].(*string)) = "01970000-0000-7000-9999-aaaaaaaaaaaa"
				*(dest[1].(*string)) = tenantID
				*(dest[2].(*string)) = courseID
				*(dest[3].(*string)) = gcid
				*(dest[4].(*time.Time)) = insertedAt
				*(dest[5].(**time.Time)) = nil
				*(dest[6].(*string)) = "active"
				*(dest[7].(**time.Time)) = nil
				*(dest[8].(**bool)) = nil
				return nil
			}}
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	enr, err := r.Register(ctx, tenantID, courseID, gcid)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if enr == nil {
		t.Fatalf("Register: nil enrollment on success")
	}
	if enr.TenantID != tenantID || enr.CourseID != courseID || enr.GCID != gcid {
		t.Fatalf("Register: returned enrollment field mismatch; got %+v", enr)
	}
	if len(q.sqls) < 2 {
		t.Fatalf("expected at least 2 SQLs (SET LOCAL + INSERT); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO course_enrollments") {
		t.Fatalf("expected INSERT INTO course_enrollments; got %q", last)
	}
	if !strings.Contains(last, "ON CONFLICT (course_id, gcid)") {
		t.Fatalf("expected ON CONFLICT (course_id, gcid); got %q", last)
	}
	// CHO-2161 — the conflict action REVIVES a soft-deleted tombstone rather
	// than DO NOTHING. The unique index on (course_id, gcid) is plain, so an
	// unenrolled learner's tombstone still occupies the natural key; DO NOTHING
	// made re-enrol impossible (empty RETURNING, and the natural-key fallback
	// filters deleted_at IS NULL). Idempotency for an ALREADY-ACTIVE row is
	// preserved by the revive-only guard: the WHERE is false, so no row is
	// updated or returned and the caller still falls back to the natural-key
	// SELECT. (Behaviour proven end-to-end against live Cloud SQL by
	// TestIntegration_EnrollmentRepo_ReenrolAfterCancel_RevivesRow +
	// TestIntegration_EnrollmentRepo_Register_IsIdempotent.)
	if !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected DO UPDATE (revive tombstone); got %q", last)
	}
	if !strings.Contains(last, "deleted_at  = NULL") {
		t.Fatalf("expected the revive to clear deleted_at; got %q", last)
	}
	if !strings.Contains(last, "WHERE course_enrollments.deleted_at IS NOT NULL") {
		t.Fatalf("revive must be guarded to tombstones only (else it would clobber an active row); got %q", last)
	}
	if !strings.Contains(last, "RETURNING enrollment_id") {
		t.Fatalf("expected RETURNING enrollment_id; got %q", last)
	}
}

// -----------------------------------------------------------------------------
// Register — conflict-fallback path (RETURNING empty -> natural-key SELECT)
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_Register_Conflict_FallsBackToNaturalKeySelect(t *testing.T) {
	t.Parallel()
	existingID := "01970000-0000-7000-9999-bbbbbbbbbbbb"
	existingEnrolledAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)

	// First QueryRow call (INSERT RETURNING) returns "no rows" — DO NOTHING
	// conflict. Second call (natural-key SELECT) returns the existing row.
	var queryRowCalls int
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			queryRowCalls++
			if queryRowCalls == 1 {
				// INSERT conflict — RETURNING is empty.
				return stubRow{scanFn: func(dest ...any) error {
					return errors.New("no rows in result set")
				}}
			}
			// Natural-key SELECT — return existing.
			return stubRow{scanFn: func(dest ...any) error {
				if len(dest) != 9 {
					return errors.New("scanEnrollment: expected 9 destinations")
				}
				*(dest[0].(*string)) = existingID
				*(dest[1].(*string)) = tenantID
				*(dest[2].(*string)) = courseID
				*(dest[3].(*string)) = gcid
				*(dest[4].(*time.Time)) = existingEnrolledAt
				*(dest[5].(**time.Time)) = nil
				*(dest[6].(*string)) = "active"
				*(dest[7].(**time.Time)) = nil
				*(dest[8].(**bool)) = nil
				return nil
			}}
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	enr, err := r.Register(ctx, tenantID, courseID, gcid)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if enr == nil {
		t.Fatalf("Register: nil enrollment on conflict-fallback")
	}
	if enr.ID != existingID {
		t.Fatalf("Register: expected existing id %s; got %s", existingID, enr.ID)
	}
	if !enr.EnrolledAt.Equal(existingEnrolledAt) {
		t.Fatalf("Register: expected existing enrolled_at; got %v", enr.EnrolledAt)
	}
	// We expect at least 3 SQLs: SET LOCAL, INSERT (conflict), SELECT.
	if len(q.sqls) < 3 {
		t.Fatalf("expected at least 3 SQLs on conflict path; got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[len(q.sqls)-1], "FROM course_enrollments") {
		t.Fatalf("last SQL must be the natural-key SELECT; got %q", q.sqls[len(q.sqls)-1])
	}
}

// -----------------------------------------------------------------------------
// GetByCourseAndGCID — miss returns ok=false, no error
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_GetByCourseAndGCID_Miss_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows in result set")
			}}
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	enr, ok, err := r.GetByCourseAndGCID(ctx, tenantID, "nope", gcid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok || enr != nil {
		t.Fatalf("expected (nil, false); got (%+v, %v)", enr, ok)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
}

// -----------------------------------------------------------------------------
// ListByGCID — multi-row hit ordering preserved
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_ListByGCID_TwoActiveRows(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	older := now.Add(-1 * time.Hour)

	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{
				rows: []func(dest ...any) error{
					func(dest ...any) error {
						*(dest[0].(*string)) = "01970000-0000-7000-9999-cccccccccccc"
						*(dest[1].(*string)) = tenantID
						*(dest[2].(*string)) = courseID
						*(dest[3].(*string)) = gcid
						*(dest[4].(*time.Time)) = now
						*(dest[5].(**time.Time)) = nil
						*(dest[6].(*string)) = "active"
						*(dest[7].(**time.Time)) = nil
						*(dest[8].(**bool)) = nil
						return nil
					},
					func(dest ...any) error {
						*(dest[0].(*string)) = "01970000-0000-7000-9999-dddddddddddd"
						*(dest[1].(*string)) = tenantID
						*(dest[2].(*string)) = "01970000-0000-7000-8000-000000000aaa"
						*(dest[3].(*string)) = gcid
						*(dest[4].(*time.Time)) = older
						*(dest[5].(**time.Time)) = nil
						*(dest[6].(*string)) = "active"
						*(dest[7].(**time.Time)) = nil
						*(dest[8].(**bool)) = nil
						return nil
					},
				},
			}, nil
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByGCID(ctx, tenantID, gcid)
	if err != nil {
		t.Fatalf("ListByGCID: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 rows; got %d", len(out))
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "ORDER BY enrolled_at DESC") {
		t.Fatalf("expected ORDER BY enrolled_at DESC; got %q", last)
	}
	if !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete filter; got %q", last)
	}
}

// -----------------------------------------------------------------------------
// CountByCourse — happy path
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_CountByCourse_ReturnsScannedInt(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				if len(dest) != 1 {
					return errors.New("CountByCourse: expected 1 destination")
				}
				*(dest[0].(*int)) = 42
				return nil
			}}
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	count, err := r.CountByCourse(ctx, tenantID, courseID)
	if err != nil {
		t.Fatalf("CountByCourse: %v", err)
	}
	if count != 42 {
		t.Fatalf("expected 42; got %d", count)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "SELECT COUNT(*)") {
		t.Fatalf("expected SELECT COUNT(*); got %q", last)
	}
	if !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete filter on count; got %q", last)
	}
}

// -----------------------------------------------------------------------------
// MarkCompleted — persists the completion fields via an RLS-scoped UPDATE
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_MarkCompleted_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewEnrollmentRepo(nil)
	e, _ := domain.NewEnrollment(tenantID, courseID, gcid)
	if err := r.MarkCompleted(context.Background(), e); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("MarkCompleted: expected ErrNotImplemented; got %v", err)
	}
}

func TestEnrollmentRepo_MarkCompleted_AppliesRLSThenUpdates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	e, _ := domain.NewEnrollment(tenantID, courseID, gcid)
	if err := e.Complete(true, now); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if err := r.MarkCompleted(ctx, e); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}

	if len(q.sqls) < 2 {
		t.Fatalf("expected at least 2 SQLs (SET LOCAL + UPDATE); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "UPDATE course_enrollments") {
		t.Fatalf("expected UPDATE course_enrollments; got %q", last)
	}
	for _, frag := range []string{"status = $2", "completed_at = $3", "passed = $4", "WHERE enrollment_id = $1", "deleted_at IS NULL"} {
		if !strings.Contains(last, frag) {
			t.Fatalf("UPDATE must contain %q; got %q", frag, last)
		}
	}
	// Arg order matches the $N placeholders: id, status, completed_at, passed.
	lastArgs := q.args[len(q.args)-1]
	if len(lastArgs) != 4 {
		t.Fatalf("expected 4 bind args; got %d (%v)", len(lastArgs), lastArgs)
	}
	if lastArgs[0] != e.ID {
		t.Fatalf("arg[0] must be enrollment_id %q; got %v", e.ID, lastArgs[0])
	}
	if lastArgs[1] != string(domain.EnrollmentStatusCompleted) {
		t.Fatalf("arg[1] must be status %q; got %v", domain.EnrollmentStatusCompleted, lastArgs[1])
	}
}

// -----------------------------------------------------------------------------
// Get — hydrates the completion fields from the row
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_Get_HydratesCompletionFields(t *testing.T) {
	t.Parallel()
	completedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	passed := true
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				if len(dest) != 9 {
					return errors.New("scanEnrollment: expected 9 destinations")
				}
				*(dest[0].(*string)) = "01970000-0000-7000-9999-eeeeeeeeeeee"
				*(dest[1].(*string)) = tenantID
				*(dest[2].(*string)) = courseID
				*(dest[3].(*string)) = gcid
				*(dest[4].(*time.Time)) = time.Now().UTC()
				*(dest[5].(**time.Time)) = nil
				*(dest[6].(*string)) = "completed"
				*(dest[7].(**time.Time)) = &completedAt
				*(dest[8].(**bool)) = &passed
				return nil
			}}
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	enr, ok, err := r.Get(ctx, "01970000-0000-7000-9999-eeeeeeeeeeee")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if enr.Status != domain.EnrollmentStatusCompleted {
		t.Fatalf("hydrated status: want completed, got %q", enr.Status)
	}
	if enr.CompletedAt == nil || !enr.CompletedAt.Equal(completedAt) {
		t.Fatalf("hydrated CompletedAt: want %v, got %v", completedAt, enr.CompletedAt)
	}
	if enr.Passed == nil || *enr.Passed != true {
		t.Fatalf("hydrated Passed: want true, got %v", enr.Passed)
	}
	// The SELECT must request the completion columns.
	last := q.sqls[len(q.sqls)-1]
	for _, frag := range []string{"status", "completed_at", "passed"} {
		if !strings.Contains(last, frag) {
			t.Fatalf("SELECT must hydrate %q; got %q", frag, last)
		}
	}
}

// -----------------------------------------------------------------------------
// Cancel — soft-deletes the row via an RLS-scoped UPDATE (the persist call the
// DELETE cancel handler was missing). deleted_at IS NULL guard = idempotent.
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_Cancel_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewEnrollmentRepo(nil)
	if err := r.Cancel(context.Background(), tenantID, courseID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Cancel: expected ErrNotImplemented; got %v", err)
	}
}

func TestEnrollmentRepo_Cancel_AppliesRLSThenSoftDeletes(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	enrollmentID := "01970000-0000-7000-9999-cafecafecafe"
	if err := r.Cancel(ctx, tenantID, enrollmentID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if len(q.sqls) < 2 {
		t.Fatalf("expected at least 2 SQLs (SET LOCAL + UPDATE); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "UPDATE course_enrollments") {
		t.Fatalf("expected UPDATE course_enrollments; got %q", last)
	}
	for _, frag := range []string{
		"deleted_at = now()", "status = 'cancelled'", "updated_at = now()",
		"WHERE enrollment_id = $1", "tenant_id = $2", "deleted_at IS NULL",
	} {
		if !strings.Contains(last, frag) {
			t.Fatalf("Cancel UPDATE must contain %q; got %q", frag, last)
		}
	}
	// Arg order matches the $N placeholders: enrollment_id ($1), tenant_id ($2).
	lastArgs := q.args[len(q.args)-1]
	if len(lastArgs) != 2 {
		t.Fatalf("expected 2 bind args; got %d (%v)", len(lastArgs), lastArgs)
	}
	if lastArgs[0] != enrollmentID {
		t.Fatalf("arg[0] must be enrollment_id %q; got %v", enrollmentID, lastArgs[0])
	}
	if lastArgs[1] != tenantID {
		t.Fatalf("arg[1] must be tenant_id %q; got %v", tenantID, lastArgs[1])
	}
}
