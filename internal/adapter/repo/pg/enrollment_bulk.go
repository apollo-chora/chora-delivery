// enrollment_bulk.go - RegisterBulk: the ATOMIC (all-or-nothing) offering
// roster bulk-enrol path for chora_delivery.course_enrollments.
//
// Why a dedicated method (vs looping Register):
//
// The single-enrol path (Register) commits the enrolment row in its OWN
// RunInTx, then the caller emits chora.delivery.enrollment.created.v1 AFTER the
// commit via a publisher that tees the outbox row on a SEPARATE connection
// (deliveryoutbox.Store.Insert / a nil-tx Recorder.Record). A crash between the
// committed enrolment and that outbox write silently loses the event - the
// classic non-transactional-outbox gap.
//
// RegisterBulk is a TRUE transactional outbox: it inserts ALL N enrolment rows
// AND invokes the per-learner outbox hook inside ONE RunInTx, so each enrolment
// row and its enrollment.created outbox row commit (or roll back) atomically.
// The hook writes the outbox row on the SAME live tx via the supplied `exec`
// (which runs against the transaction's Querier). Any error - an enrolment
// INSERT failure OR an outbox-write failure - aborts the whole batch: pgx rolls
// the transaction back, so NO enrolment row and NO partial outbox row survives.
//
// Idempotency: reuses SQLInsertEnrollment's ON CONFLICT (course_id, gcid) revive
// semantics. A fresh insert or a revived-from-tombstone row RETURNS a row and
// fires the outbox hook; an already-active learner returns no row (the caller's
// natural-key SELECT confirms it exists) and is skipped - no re-emit, mirroring
// the single path's `!existed` guard. Re-submitting a batch is therefore safe.
package pg

import (
	"context"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-common/rls"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// BulkEnrollOutboxFn is invoked INSIDE the persistence transaction for each
// NEWLY inserted (fresh or revived) enrolment, so the caller can write the
// enrollment.created.v1 transactional-outbox row on the SAME transaction.
//
// `exec` runs a statement against the live tx (the enrolment row + its event
// commit atomically). Returning an error rolls the WHOLE batch back - the
// enrolment INSERT and any earlier learners' inserts/outbox rows are discarded.
//
// A raw (unnamed) func type is used so callers in other adapter packages (the
// httpapi handler) can satisfy the shape structurally without importing this
// package.
type BulkEnrollOutboxFn = func(ctx context.Context, exec func(ctx context.Context, query string, args ...any) error, e *domain.Enrollment) error

// RegisterBulk registers every learner in `gcids` on `courseID` for `tenantID`
// in ONE transaction, firing `onInserted` for each newly inserted enrolment so
// its outbox row is written on the same tx (a TRUE transactional outbox).
//
// Returns the newly inserted enrolments (fresh or revived); already-active
// learners are skipped (idempotent, not re-emitted). Any error rolls the whole
// batch back and returns (nil, err) - all-or-nothing.
//
// Inputs are validated at the boundary (fail-loud) BEFORE the tx opens: a nil
// TxRunner is ErrNotImplemented; empty tenant/course is its dedicated sentinel;
// a blank GCID anywhere in the batch is ErrEnrollmentMissingGCID. Duplicate
// GCIDs within the batch are de-duplicated (order-preserving) so a repeated
// learner enrols once.
func (r *EnrollmentRepo) RegisterBulk(ctx context.Context, tenantID, courseID string, gcids []string, onInserted BulkEnrollOutboxFn) ([]*domain.Enrollment, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrEnrollmentMissingTenant
	}
	if strings.TrimSpace(courseID) == "" {
		return nil, ErrEnrollmentMissingCourse
	}
	deduped := make([]string, 0, len(gcids))
	seen := make(map[string]struct{}, len(gcids))
	for _, g := range gcids {
		if strings.TrimSpace(g) == "" {
			return nil, ErrEnrollmentMissingGCID
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		deduped = append(deduped, g)
	}

	var inserted []*domain.Enrollment
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		// exec runs an outbox-hook statement on THIS transaction's Querier so
		// the outbox row commits atomically with the enrolment rows.
		exec := func(ctx context.Context, query string, args ...any) error {
			_, e := q.Exec(ctx, query, args...)
			return e
		}
		for _, gcid := range deduped {
			candidate, err := domain.NewEnrollment(tenantID, courseID, gcid)
			if err != nil {
				return err
			}
			row := q.QueryRow(ctx, SQLInsertEnrollment,
				candidate.ID, candidate.TenantID, candidate.CourseID, candidate.GCID,
				candidate.EnrolledAt,
			)
			e, scanErr := scanEnrollment(row.Scan)
			if scanErr == nil {
				// Fresh insert or revived tombstone → a NEW enrolment: tee its
				// event on the tx, then record it in the result.
				if onInserted != nil {
					if err := onInserted(ctx, exec, e); err != nil {
						return err
					}
				}
				inserted = append(inserted, e)
				continue
			}
			// Empty RETURNING → the row is already active (the revive guard's
			// WHERE was false). Confirm via the natural-key SELECT; a genuine
			// miss here is a fatal inconsistency that must abort the batch.
			existingRow := q.QueryRow(ctx, SQLSelectEnrollmentByNaturalKey, tenantID, courseID, gcid)
			if _, lookupErr := scanEnrollment(existingRow.Scan); lookupErr != nil {
				return fmt.Errorf("pg: enrollment register bulk: conflict but natural-key lookup empty (gcid=%s): %w", gcid, lookupErr)
			}
			// Already active → idempotent no-op (no outbox, not counted).
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return inserted, nil
}
