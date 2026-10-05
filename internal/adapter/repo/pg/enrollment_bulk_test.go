// enrollment_bulk_test.go - unit tests for pg.EnrollmentRepo.RegisterBulk, the
// ATOMIC (all-or-nothing) offering-roster bulk-enrol path.
//
// The single-enrol path (Register + after-commit publish) tees its outbox row
// on a SEPARATE connection (the delivery outbox Store.Insert / a nil-tx
// Recorder.Record), so a crash between the committed enrolment and the outbox
// write loses the event. RegisterBulk closes that gap: it writes ALL N
// enrolment rows AND invokes the per-learner outbox hook inside ONE RunInTx, so
// each enrolment row and its chora.delivery.enrollment.created.v1 outbox row
// commit atomically (a TRUE transactional outbox).
//
// What these tests guarantee:
//
//  1. All N enrolment inserts + N outbox writes happen in EXACTLY ONE RunInTx.
//  2. rls.ApplySession runs BEFORE any data query.
//  3. Only NEWLY inserted (fresh or revived-from-tombstone) rows fire the outbox
//     hook - an already-active row (conflict, empty RETURNING) is skipped.
//  4. ALL-OR-NOTHING: a mid-batch failure (an outbox-hook error, mirroring an
//     outbox INSERT failing) rolls the WHOLE batch back - the committed store
//     ends with ZERO enrolment rows and ZERO outbox rows (no partial write).
//  5. Nil-tx returns ErrNotImplemented (fail-loud, matches the other methods).
//  6. Empty tenant/course/gcid inputs return their dedicated sentinels (loud).
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

// bulkGCID1..3 are three distinct learner GCIDs for the batch.
const (
	bulkGCID1 = "01970000-0000-7000-9000-0000000000b1"
	bulkGCID2 = "01970000-0000-7000-9000-0000000000b2"
	bulkGCID3 = "01970000-0000-7000-9000-0000000000b3"
)

// freshEnrollmentRowFn returns a stubRow scan that fakes a successful
// INSERT ... RETURNING for a brand-new enrolment (id derived from the gcid arg).
func freshEnrollmentRowFn(sql string, args ...any) pg.Row {
	return stubRow{scanFn: func(dest ...any) error {
		if len(dest) != 9 {
			return errors.New("scanEnrollment: expected 9 destinations")
		}
		gcidArg, _ := args[3].(string) // $4 = gcid
		*(dest[0].(*string)) = "en-" + gcidArg
		*(dest[1].(*string)) = tenantID
		*(dest[2].(*string)) = courseID
		*(dest[3].(*string)) = gcidArg
		*(dest[4].(*time.Time)) = time.Now().UTC()
		*(dest[5].(**time.Time)) = nil
		*(dest[6].(*string)) = "active"
		*(dest[7].(**time.Time)) = nil
		*(dest[8].(**bool)) = nil
		return nil
	}}
}

// -----------------------------------------------------------------------------
// recordingTxRunner - simulates pgx tx commit/rollback so all-or-nothing is
// observable at the unit level. Writes during fn land in a per-tx scratch
// stubQuerier; on fn success they are appended to `committed`, on fn error they
// are DISCARDED (rollback). Also counts RunInTx invocations.
// -----------------------------------------------------------------------------

type recordingTxRunner struct {
	committed *stubQuerier
	newQ      func() *stubQuerier
	runs      int
	lastQ     *stubQuerier
}

func (r *recordingTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q pg.Querier) error) error {
	r.runs++
	pending := r.newQ()
	r.lastQ = pending
	if err := fn(ctx, pending); err != nil {
		return err // rollback: pending discarded, committed unchanged
	}
	r.committed.sqls = append(r.committed.sqls, pending.sqls...)
	r.committed.args = append(r.committed.args, pending.args...)
	return nil
}

// countContains returns how many recorded SQLs contain sub.
func countContains(sqls []string, sub string) int {
	n := 0
	for _, s := range sqls {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// -----------------------------------------------------------------------------
// Fail-loud guards
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_RegisterBulk_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewEnrollmentRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, err := r.RegisterBulk(ctx, tenantID, courseID, []string{bulkGCID1}, nil)
	if !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("RegisterBulk: expected ErrNotImplemented; got %v", err)
	}
}

func TestEnrollmentRepo_RegisterBulk_RejectsEmptyInputs(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.RegisterBulk(ctx, "  ", courseID, []string{bulkGCID1}, nil); !errors.Is(err, pg.ErrEnrollmentMissingTenant) {
		t.Fatalf("empty tenant: expected ErrEnrollmentMissingTenant; got %v", err)
	}
	if _, err := r.RegisterBulk(ctx, tenantID, "", []string{bulkGCID1}, nil); !errors.Is(err, pg.ErrEnrollmentMissingCourse) {
		t.Fatalf("empty course: expected ErrEnrollmentMissingCourse; got %v", err)
	}
	if _, err := r.RegisterBulk(ctx, tenantID, courseID, []string{bulkGCID1, "   "}, nil); !errors.Is(err, pg.ErrEnrollmentMissingGCID) {
		t.Fatalf("empty gcid in batch: expected ErrEnrollmentMissingGCID; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Happy path - N fresh enrolments: ONE tx, N enrolment INSERTs, N outbox hooks
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_RegisterBulk_AllFresh_OneTxInsertsAllAndTeesAll(t *testing.T) {
	t.Parallel()
	committed := &stubQuerier{}
	txr := &recordingTxRunner{
		committed: committed,
		newQ: func() *stubQuerier {
			return &stubQuerier{rowFn: freshEnrollmentRowFn}
		},
	}
	r := pg.NewEnrollmentRepo(txr)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	var teed []string
	onInserted := func(ctx context.Context, exec func(ctx context.Context, query string, args ...any) error, e *domain.Enrollment) error {
		teed = append(teed, e.GCID)
		// Write a marker row on the SAME tx so it is subject to commit/rollback.
		return exec(ctx, "INSERT INTO outbox_events /*bulk*/ (id) VALUES ($1)", e.ID)
	}

	inserted, err := r.RegisterBulk(ctx, tenantID, courseID, []string{bulkGCID1, bulkGCID2, bulkGCID3}, onInserted)
	if err != nil {
		t.Fatalf("RegisterBulk: %v", err)
	}
	if txr.runs != 1 {
		t.Fatalf("all inserts must share ONE RunInTx; got %d runs", txr.runs)
	}
	if len(inserted) != 3 {
		t.Fatalf("expected 3 inserted enrolments; got %d", len(inserted))
	}
	if len(teed) != 3 {
		t.Fatalf("outbox hook must fire once per NEW enrolment; got %d", len(teed))
	}
	// Committed store carries all N enrolment INSERTs + N outbox writes.
	if got := countContains(committed.sqls, "INSERT INTO course_enrollments"); got != 3 {
		t.Fatalf("committed enrolment INSERTs: want 3, got %d", got)
	}
	if got := countContains(committed.sqls, "INSERT INTO outbox_events"); got != 3 {
		t.Fatalf("committed outbox INSERTs: want 3, got %d", got)
	}
	if !strings.Contains(committed.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first committed SQL must be the RLS SET LOCAL; got %q", committed.sqls[0])
	}
}

// -----------------------------------------------------------------------------
// Idempotent - an already-active learner (empty RETURNING → fallback SELECT)
// is NOT re-emitted and NOT counted as inserted.
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_RegisterBulk_AlreadyActive_SkipsOutboxAndInserted(t *testing.T) {
	t.Parallel()
	committed := &stubQuerier{}
	txr := &recordingTxRunner{
		committed: committed,
		newQ: func() *stubQuerier {
			return &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
				if strings.Contains(sql, "INSERT INTO course_enrollments") {
					// Conflict - DO UPDATE WHERE deleted_at IS NOT NULL did not
					// fire (row already active): RETURNING is empty.
					return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
				}
				// Natural-key SELECT fallback returns the existing active row.
				return stubRow{scanFn: func(dest ...any) error {
					*(dest[0].(*string)) = "en-existing"
					*(dest[1].(*string)) = tenantID
					*(dest[2].(*string)) = courseID
					*(dest[3].(*string)) = bulkGCID1
					*(dest[4].(*time.Time)) = time.Now().UTC()
					*(dest[5].(**time.Time)) = nil
					*(dest[6].(*string)) = "active"
					*(dest[7].(**time.Time)) = nil
					*(dest[8].(**bool)) = nil
					return nil
				}}
			}}
		},
	}
	r := pg.NewEnrollmentRepo(txr)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	teed := 0
	onInserted := func(ctx context.Context, exec func(ctx context.Context, query string, args ...any) error, e *domain.Enrollment) error {
		teed++
		return exec(ctx, "INSERT INTO outbox_events /*bulk*/ (id) VALUES ($1)", e.ID)
	}

	inserted, err := r.RegisterBulk(ctx, tenantID, courseID, []string{bulkGCID1}, onInserted)
	if err != nil {
		t.Fatalf("RegisterBulk: %v", err)
	}
	if len(inserted) != 0 {
		t.Fatalf("already-active learner must NOT count as inserted; got %d", len(inserted))
	}
	if teed != 0 {
		t.Fatalf("already-active learner must NOT fire the outbox hook; got %d", teed)
	}
	if got := countContains(committed.sqls, "INSERT INTO outbox_events"); got != 0 {
		t.Fatalf("no outbox row for an already-active learner; got %d", got)
	}
}

// -----------------------------------------------------------------------------
// ALL-OR-NOTHING - a mid-batch outbox-hook failure rolls back the WHOLE batch:
// zero enrolment rows + zero outbox rows survive.
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_RegisterBulk_MidBatchOutboxFailure_RollsBackEverything(t *testing.T) {
	t.Parallel()
	committed := &stubQuerier{}
	txr := &recordingTxRunner{
		committed: committed,
		newQ: func() *stubQuerier {
			return &stubQuerier{rowFn: freshEnrollmentRowFn}
		},
	}
	r := pg.NewEnrollmentRepo(txr)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	sentinel := errors.New("boom: outbox INSERT failed on the tx")
	calls := 0
	onInserted := func(ctx context.Context, exec func(ctx context.Context, query string, args ...any) error, e *domain.Enrollment) error {
		calls++
		// Write the row on the tx first, THEN fail on the 2nd learner - proving
		// the already-written row-1 enrolment + outbox are discarded too.
		_ = exec(ctx, "INSERT INTO outbox_events /*bulk*/ (id) VALUES ($1)", e.ID)
		if calls == 2 {
			return sentinel
		}
		return nil
	}

	inserted, err := r.RegisterBulk(ctx, tenantID, courseID, []string{bulkGCID1, bulkGCID2, bulkGCID3}, onInserted)
	if !errors.Is(err, sentinel) {
		t.Fatalf("RegisterBulk must surface the outbox failure; got %v", err)
	}
	if inserted != nil {
		t.Fatalf("no enrolments may be returned on a rolled-back batch; got %d", len(inserted))
	}
	if txr.runs != 1 {
		t.Fatalf("batch must be a single tx; got %d runs", txr.runs)
	}
	// The 3rd learner is never reached (fail-fast on row 2).
	if calls != 2 {
		t.Fatalf("expected the batch to abort on learner 2; onInserted calls=%d", calls)
	}
	// ROLLBACK: nothing survives in the committed store.
	if got := countContains(committed.sqls, "INSERT INTO course_enrollments"); got != 0 {
		t.Fatalf("rolled-back batch must leave ZERO enrolment rows; got %d", got)
	}
	if got := countContains(committed.sqls, "INSERT INTO outbox_events"); got != 0 {
		t.Fatalf("rolled-back batch must leave ZERO outbox rows (no partial outbox); got %d", got)
	}
}

// -----------------------------------------------------------------------------
// ALL-OR-NOTHING - a mid-batch enrolment INSERT failure (conflict with an empty
// natural-key fallback) aborts + rolls back the whole batch.
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_RegisterBulk_MidBatchInsertFailure_RollsBackEverything(t *testing.T) {
	t.Parallel()
	committed := &stubQuerier{}
	txr := &recordingTxRunner{
		committed: committed,
		newQ: func() *stubQuerier {
			seen := 0
			return &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
				if strings.Contains(sql, "INSERT INTO course_enrollments") {
					seen++
					if seen == 2 {
						// 2nd learner: empty RETURNING (conflict) → forces fallback.
						return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
					}
					return freshEnrollmentRowFn(sql, args...)
				}
				// Fallback natural-key SELECT ALSO returns nothing → fatal error.
				return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
			}}
		},
	}
	r := pg.NewEnrollmentRepo(txr)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	onInserted := func(ctx context.Context, exec func(ctx context.Context, query string, args ...any) error, e *domain.Enrollment) error {
		return exec(ctx, "INSERT INTO outbox_events /*bulk*/ (id) VALUES ($1)", e.ID)
	}

	_, err := r.RegisterBulk(ctx, tenantID, courseID, []string{bulkGCID1, bulkGCID2, bulkGCID3}, onInserted)
	if err == nil {
		t.Fatalf("RegisterBulk must fail when a learner's INSERT conflicts with an empty natural-key lookup")
	}
	if got := countContains(committed.sqls, "INSERT INTO course_enrollments"); got != 0 {
		t.Fatalf("rolled-back batch must leave ZERO enrolment rows; got %d", got)
	}
	if got := countContains(committed.sqls, "INSERT INTO outbox_events"); got != 0 {
		t.Fatalf("rolled-back batch must leave ZERO outbox rows; got %d", got)
	}
}
