// booking.go — Postgres adapter for chora_delivery.bookings.
//
// SCHEMA: see migrations/0019_bookings.up.sql (`bookings` table).
//
// Replaces the in-memory inmem.BookingRepo wiring. Before this adapter,
// bookings lived only in memory — rows never survived a pod restart and there
// was no GET-list, so the R+ FE (CHO-1620 T3) had to session-track created
// bookings (HANDOFF_RPLUS §6). This is the [[feedback-no-stubs-real-wiring]]
// + [[feedback-resilience-priority]] fix: durable, RLS-isolated, listable.
//
// Resilience-priority directive (`feedback_resilience_priority`):
//
//   - Idempotent: Save uses INSERT ... ON CONFLICT (id) DO UPDATE so the
//     create + every status-transition re-Save converges (no dup rows).
//   - Soft-delete-aware: every read filters `WHERE deleted_at IS NULL`.
//   - RLS: every read/write applies rls.ApplySession before the user query so
//     the row-level policy on bookings enforces tenant isolation (defence in
//     depth — the handler also checks b.TenantID). Caller MUST set tenant via
//     tracing.WithTenantID before invoking — rls.ApplySession reads from
//     tracing.TenantIDFromContext.
//   - PgBouncer-safe: SET LOCAL chora.tenant_id only inside RunInTx.
//
// Save returns an error (fail loud — a dropped Save loses a seat reservation).
// Get returns its error — an infra/RLS failure is LOUD, never a silent miss.
// ok=false means a GENUINE absent row (CHO-2184: the two were once the same
// answer, and a dead read passed for an empty one), matching the
// domain.BookingPort contract.
//
// Cross-DB queries forbidden — chora-delivery reads only chora_delivery.
package pg

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/apollo-chora/chora-common/rls"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

// SQLUpsertBooking idempotently inserts on the id PK. A re-Save after a status
// transition lands on the conflict branch and refreshes status + updated_at;
// the immutable columns (class/course/learner/created_at) stay put.
const SQLUpsertBooking = `
INSERT INTO bookings (
    id, tenant_id, class_id, course_id, learner_gcid, status, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
ON CONFLICT (id) DO UPDATE SET
    status     = EXCLUDED.status,
    updated_at = EXCLUDED.updated_at
`

// SQLSelectBookingByID — point read for the PATCH /status path. RLS still
// applies (the SET LOCAL chora.tenant_id wrapper) so only the caller's
// tenant's rows are visible even though the PK is globally unique.
const SQLSelectBookingByID = `
SELECT id, tenant_id, class_id, course_id, learner_gcid, status, created_at, updated_at, deleted_at
FROM bookings
WHERE id = $1
  AND deleted_at IS NULL
`

// SQLListBookingsByTenant powers GET /api/bookings — newest-first (the natural
// shape for an instructor's bookings panel). RLS scopes to the caller's
// tenant; the explicit tenant_id = $1 is belt-and-braces on the read path.
const SQLListBookingsByTenant = `
SELECT id, tenant_id, class_id, course_id, learner_gcid, status, created_at, updated_at, deleted_at
FROM bookings
WHERE tenant_id = $1
  AND deleted_at IS NULL
ORDER BY created_at DESC
`

// SQLAdvisoryXactLockBookingClass serialises concurrent seat reservations for
// ONE class (ADR-236 D2). The lock is transaction-scoped (auto-released at
// COMMIT/ROLLBACK) and keyed on a Go-side hash of the class id (see
// advisoryLockKey) so we do not rely on Postgres's undocumented hashtext().
// Two reservations for the SAME class block each other → the count-then-insert
// below can never race into an over-book; reservations for DIFFERENT classes do
// not contend. Advisory locks are not RLS rows, so the tenant is folded into
// the key to keep it opaque even though class ids are globally unique.
const SQLAdvisoryXactLockBookingClass = `SELECT pg_advisory_xact_lock($1)`

// SQLCountActiveBookingsByClass counts a class's live (non-withdrawn) seats.
// RLS already scopes rows to the caller's tenant; class_id = $1 narrows to the
// one class. Taken under the advisory lock above so the returned count is
// stable for the duration of the reservation txn.
const SQLCountActiveBookingsByClass = `
SELECT count(*)
FROM bookings
WHERE class_id = $1
  AND deleted_at IS NULL
`

// -----------------------------------------------------------------------------
// BookingRepo
// -----------------------------------------------------------------------------

// BookingRepo is the Postgres-backed domain.BookingPort impl.
type BookingRepo struct {
	tx TxRunner
}

// NewBookingRepo constructs a BookingRepo around a TxRunner.
//
// Passing nil yields a Repo whose writes return ErrNotImplemented (fail-loud,
// matches Course / Application / Enrollment repos) and whose Get returns
// ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184). Production wires this via cmd/server/main.go behind the
// same pool gate as catalogue + enrollments.
func NewBookingRepo(tx TxRunner) *BookingRepo { return &BookingRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ domain.BookingPort = (*BookingRepo)(nil)

// Save upserts a booking idempotently on the id PK.
func (r *BookingRepo) Save(ctx context.Context, b *domain.Booking) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if b == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		_, err := q.Exec(ctx, SQLUpsertBooking,
			b.ID, b.TenantID, b.ClassID, b.CourseID, b.LearnerID,
			string(b.Status), b.CreatedAt, b.UpdatedAt,
		)
		return err
	})
}

// Get resolves a booking by id. (nil, false) on miss/infra error.
func (r *BookingRepo) Get(ctx context.Context, id string) (*domain.Booking, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var found *domain.Booking
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectBookingByID, id)
		b, scanErr := scanBooking(row.Scan)
		if scanErr != nil {
			if isNoRows(scanErr) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get booking: %w", scanErr)
		}
		found = b
		return nil
	})
	if err != nil {
		return nil, false, err // LOUD: infra/RLS failure is NOT a miss (CHO-2184)
	}
	if found == nil {
		return nil, false, nil // genuine miss
	}
	return found, true, nil
}

// ListByTenant returns the tenant's active bookings, newest-first.
func (r *BookingRepo) ListByTenant(ctx context.Context, tenantID string) ([]*domain.Booking, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*domain.Booking
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListBookingsByTenant, tenantID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			b, scanErr := scanBooking(rows.Scan)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReserveSeatAndSave enforces the class-capacity invariant durably and
// cross-pod-correctly (ADR-236 D2). In ONE serialised transaction it:
//
//  1. applies RLS (SET LOCAL chora.tenant_id) so the count sees only this
//     tenant's rows;
//  2. takes a transaction-scoped advisory lock on the class, so two concurrent
//     reservations for the same class serialise (the retired in-memory
//     Class.reserveSeat mutex was single-pod only — this holds across pods);
//  3. counts the class's active bookings;
//  4. inserts b iff count < maxCapacity, else returns ErrClassAtCapacity.
//
// The whole thing is atomic: the lock is held until COMMIT, so no other
// reservation can slip a row in between the count and the insert.
func (r *BookingRepo) ReserveSeatAndSave(ctx context.Context, classID string, maxCapacity int, b *domain.Booking) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if b == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLAdvisoryXactLockBookingClass, advisoryLockKey(b.TenantID, classID)); err != nil {
			return fmt.Errorf("pg: reserve seat advisory lock: %w", err)
		}
		var count int
		if err := q.QueryRow(ctx, SQLCountActiveBookingsByClass, classID).Scan(&count); err != nil {
			return fmt.Errorf("pg: count class bookings: %w", err)
		}
		if count >= maxCapacity {
			return domain.ErrClassAtCapacity
		}
		if _, err := q.Exec(ctx, SQLUpsertBooking,
			b.ID, b.TenantID, b.ClassID, b.CourseID, b.LearnerID,
			string(b.Status), b.CreatedAt, b.UpdatedAt,
		); err != nil {
			return err
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// advisoryLockKey hashes (tenant, class) into a stable int64 for
// pg_advisory_xact_lock. FNV-1a is deterministic across processes (no reliance
// on Postgres's undocumented hashtext), so all pods hash the same class to the
// same lock. The uint64→int64 wrap is intentional: the bigint advisory-lock key
// space is the full signed range. A hash collision only makes two unrelated
// classes briefly serialise (correctness preserved, negligible contention).
func advisoryLockKey(tenantID, classID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("delivery:booking:" + tenantID + ":" + classID))
	return int64(h.Sum64())
}

// scanBooking maps one row into the domain entity. Column order matches
// SQLSelectBookingByID + SQLListBookingsByTenant:
//
//	id, tenant_id, class_id, course_id, learner_gcid, status, created_at, updated_at, deleted_at
func scanBooking(scan func(dest ...any) error) (*domain.Booking, error) {
	var b domain.Booking
	var status string
	if err := scan(
		&b.ID, &b.TenantID, &b.ClassID, &b.CourseID, &b.LearnerID,
		&status, &b.CreatedAt, &b.UpdatedAt, &b.DeletedAt,
	); err != nil {
		return nil, err
	}
	b.Status = domain.BookingStatus(status)
	return &b, nil
}
