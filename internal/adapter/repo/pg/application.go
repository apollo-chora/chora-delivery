// Package pg is the Postgres adapter for chora-delivery's Application
// aggregate. Mirrors chora-consumption's pg adapter pattern: minimal Querier
// + Row + Rows shims so pgx can be wired without dragging it into go.mod.
//
// SCHEMA: see migrations/0002_applications.sql.
//
// SCAFFOLDING NOTE: This package is the M12+ entry point. SQL strings are
// reviewable here NOW; the runtime pgx code lands when chora-delivery wires
// the connection pool. Tests inject a stub Querier that asserts on the SQL
// shape + arg count + rls.ApplySession invocation order.
//
// Per feedback_no_inline_config: connection strings come from env vars at
// cmd/server boot — this package never reads env.
//
// All repo methods MUST call rls.ApplySession before user queries. The
// helper sets `chora.tenant_id` + `chora.user_gcid` SET LOCAL vars so the
// RLS policies on `applications` + `application_state_history` filter
// per-tenant + per-user.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

// Querier is the minimal contract from a pgx-shaped driver.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// Row is a single-row result.
type Row interface {
	Scan(dest ...any) error
}

// Rows is a multi-row result.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

// TxRunner abstracts pool.BeginTx so chora-delivery doesn't import pgx.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error
}

// ErrNotImplemented — production pgx wiring lands at M12.
var ErrNotImplemented = errors.New("pg: pgx adapter not yet wired (M12)")

// -----------------------------------------------------------------------------
// SQL templates
// -----------------------------------------------------------------------------

// SQLInsertApplication is the parametrised INSERT used by SubmitOrGet.
//
// Idempotency on (tenant_id, course_id, gcid) is enforced via the UNIQUE
// constraint in 0002_applications.sql. The repo layer detects the conflict
// and falls back to a SELECT-by-natural-key.
const SQLInsertApplication = `
INSERT INTO applications (
    application_id, tenant_id, course_id, class_id, gcid, status,
    funding_lines, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (tenant_id, course_id, gcid)
DO NOTHING
RETURNING application_id
`

// SQLSelectApplicationByID is the by-id SELECT used by Get.
//
// RLS ensures the row is only returned when chora.tenant_id matches; we
// still include the explicit tenant_id filter for defence-in-depth.
const SQLSelectApplicationByID = `
SELECT application_id, tenant_id, course_id, class_id, gcid, status,
       offer_expires_at, accepted_at, paid_at, enrolled_at, withdrawn_at,
       stripe_payment_intent_id, invoice_id, singpass_session_id,
       rejected_reason, withdrawn_reason, funding_lines,
       created_at, updated_at
FROM applications
WHERE application_id = $1
  AND tenant_id = $2
  AND deleted_at IS NULL
`

// SQLSelectApplicationByNaturalKey looks up by (tenant, course, gcid).
const SQLSelectApplicationByNaturalKey = `
SELECT application_id
FROM applications
WHERE tenant_id = $1 AND course_id = $2 AND gcid = $3
  AND deleted_at IS NULL
`

// SQLListApplicationsByGCID returns all of a user's applications.
const SQLListApplicationsByGCID = `
SELECT application_id, tenant_id, course_id, class_id, gcid, status,
       offer_expires_at, accepted_at, paid_at, enrolled_at, withdrawn_at,
       stripe_payment_intent_id, invoice_id, singpass_session_id,
       rejected_reason, withdrawn_reason, funding_lines,
       created_at, updated_at
FROM applications
WHERE tenant_id = $1
  AND gcid = $2
  AND deleted_at IS NULL
ORDER BY application_id DESC
LIMIT $3 OFFSET $4
`

// SQLListApplicationsByTenant returns the admin queue's tenant-wide list
// (newest-first), paginated. RLS scopes to the session tenant; the explicit
// tenant_id filter is defence-in-depth (mirrors the by-id / by-gcid SELECTs).
const SQLListApplicationsByTenant = `
SELECT application_id, tenant_id, course_id, class_id, gcid, status,
       offer_expires_at, accepted_at, paid_at, enrolled_at, withdrawn_at,
       stripe_payment_intent_id, invoice_id, singpass_session_id,
       rejected_reason, withdrawn_reason, funding_lines,
       created_at, updated_at
FROM applications
WHERE tenant_id = $1
  AND deleted_at IS NULL
ORDER BY application_id DESC
LIMIT $2 OFFSET $3
`

// SQLListApplicationsByTenantStatus is SQLListApplicationsByTenant with an
// additional status filter (the admin queue's ?state= facet).
const SQLListApplicationsByTenantStatus = `
SELECT application_id, tenant_id, course_id, class_id, gcid, status,
       offer_expires_at, accepted_at, paid_at, enrolled_at, withdrawn_at,
       stripe_payment_intent_id, invoice_id, singpass_session_id,
       rejected_reason, withdrawn_reason, funding_lines,
       created_at, updated_at
FROM applications
WHERE tenant_id = $1
  AND status = $2
  AND deleted_at IS NULL
ORDER BY application_id DESC
LIMIT $3 OFFSET $4
`

// SQLCountApplicationsByTenant / …Status compute the post-filter,
// pre-pagination total the admin queue's "N applications" pill needs.
const SQLCountApplicationsByTenant = `
SELECT COUNT(*) FROM applications WHERE tenant_id = $1 AND deleted_at IS NULL
`

const SQLCountApplicationsByTenantStatus = `
SELECT COUNT(*) FROM applications WHERE tenant_id = $1 AND status = $2 AND deleted_at IS NULL
`

// SQLUpdateApplicationStatus is the parametrised UPDATE used by Save.
//
// Updates ALL mutable fields in one statement (status, lifecycle timestamps,
// payment + invoice + Singpass refs, decision metadata, funding_lines).
const SQLUpdateApplicationStatus = `
UPDATE applications SET
    status                   = $2,
    offer_expires_at         = $3,
    accepted_at              = $4,
    paid_at                  = $5,
    enrolled_at              = $6,
    withdrawn_at             = $7,
    stripe_payment_intent_id = $8,
    invoice_id               = $9,
    singpass_session_id      = $10,
    rejected_reason          = $11,
    withdrawn_reason         = $12,
    funding_lines            = $13,
    updated_at               = now()
WHERE application_id = $1
  AND tenant_id = $14
  AND deleted_at IS NULL
`

// SQLInsertHistoryEntry appends to application_state_history (append-only).
const SQLInsertHistoryEntry = `
INSERT INTO application_state_history (
    history_id, application_id, tenant_id, from_status, to_status,
    reason, transitioned_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
`

// SQLListHistoryByApplication loads the append-only transition trail for an
// application (oldest-first) so Get can rehydrate Application.history — the
// detail-view DTO shows the same trail the in-memory adapter keeps in-process.
const SQLListHistoryByApplication = `
SELECT from_status, to_status, reason, transitioned_at
FROM application_state_history
WHERE application_id = $1
ORDER BY transitioned_at ASC, history_id ASC
`

// -----------------------------------------------------------------------------
// ApplicationRepo
// -----------------------------------------------------------------------------

// ApplicationRepo is the Postgres-backed Application repo.
type ApplicationRepo struct {
	tx TxRunner
}

// NewApplicationRepo constructs a pg ApplicationRepo around a TxRunner.
func NewApplicationRepo(tx TxRunner) *ApplicationRepo {
	return &ApplicationRepo{tx: tx}
}

// Compile-time assertion: satisfies the domain port.
var _ application.ApplicationPort = (*ApplicationRepo)(nil)

// SubmitOrGet implements the idempotent submit semantic.
//
// Wraps the INSERT … ON CONFLICT DO NOTHING pattern: if the natural key
// already maps to an application, fall back to a SELECT and load.
func (r *ApplicationRepo) SubmitOrGet(ctx context.Context, in application.SubmitInput) (*application.Application, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	app, err := application.NewApplication(application.NewApplicationInput{
		TenantID: in.TenantID,
		CourseID: in.CourseID,
		ClassID:  in.ClassID,
		GCID:     in.GCID,
	})
	if err != nil {
		return nil, false, err
	}
	if err := app.Transition(application.StatusSubmitted); err != nil {
		return nil, false, err
	}

	created := false
	err = r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		fl, _ := json.Marshal(app.FundingLines)
		row := q.QueryRow(ctx, SQLInsertApplication,
			app.ID, app.TenantID, app.CourseID, app.ClassID, app.GCID,
			string(app.Status), fl, app.CreatedAt, app.UpdatedAt,
		)
		var insertedID string
		if err := row.Scan(&insertedID); err != nil {
			// ON CONFLICT DO NOTHING + RETURNING returns no rows on conflict —
			// the fall-through path resolves the existing app.
			return nil
		}
		created = (insertedID != "")
		if created {
			// Persist the initial Draft->Submitted transition in the SAME tx so
			// the durable trail is complete from row one (without this, the
			// first history entry would only ever live in-process). PendingHistory
			// is exactly the just-constructed transitions.
			if err := insertHistoryRows(ctx, q, app, app.PendingHistory()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if created {
		app.MarkHistoryPersisted()
		return app, true, nil
	}
	// Conflict path — load existing.
	existing, ok, err := r.lookupByNaturalKey(ctx, in.TenantID, in.CourseID, in.GCID)
	if err != nil || !ok {
		return nil, false, err
	}
	return existing, false, nil
}

// Get returns an Application by id, RLS-scoped.
func (r *ApplicationRepo) Get(ctx context.Context, tenantID, applicationID string) (*application.Application, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var found *application.Application
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectApplicationByID, applicationID, tenantID)
		app, err := scanApplication(row.Scan)
		if err != nil {
			return nil // not found — return ok=false at outer
		}
		// Rehydrate the append-only trail so applicationDetailDTO shows the
		// same history the in-memory adapter keeps in-process. ReplaceHistory
		// marks it persisted, so a subsequent Save on this aggregate only
		// inserts NEW transitions (no duplication).
		hist, hErr := loadApplicationHistory(ctx, q, applicationID)
		if hErr != nil {
			return hErr
		}
		app.ReplaceHistory(hist)
		found = app
		return nil
	})
	if err != nil || found == nil {
		return nil, false, err
	}
	return found, true, nil
}

// Save persists the aggregate's mutable state + appends ONLY the history rows
// added since this aggregate was loaded/created (PendingHistory). Writing the
// full History() here would duplicate the rows Get rehydrated, so the pending
// window is the unit of work. MarkHistoryPersisted runs only after the tx
// commits — a failed commit leaves the rows pending for the next Save.
func (r *ApplicationRepo) Save(ctx context.Context, app *application.Application) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	pending := app.PendingHistory()
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		fl, _ := json.Marshal(app.FundingLines)
		_, err := q.Exec(ctx, SQLUpdateApplicationStatus,
			app.ID,
			string(app.Status),
			nullTime(app.OfferExpiresAt),
			nullTime(app.AcceptedAt),
			nullTime(app.PaidAt),
			nullTime(app.EnrolledAt),
			nullTime(app.WithdrawnAt),
			nullStr(app.StripePaymentIntentID),
			nullStr(app.InvoiceID),
			nullStr(app.SingpassSessionID),
			nullStr(app.RejectedReason),
			nullStr(app.WithdrawnReason),
			fl,
			app.TenantID,
		)
		if err != nil {
			return fmt.Errorf("pg: update application: %w", err)
		}
		return insertHistoryRows(ctx, q, app, pending)
	})
	if err != nil {
		return err
	}
	app.MarkHistoryPersisted()
	return nil
}

// insertHistoryRows appends the given (already-pending) transitions to
// application_state_history within the caller's transaction.
func insertHistoryRows(ctx context.Context, q Querier, app *application.Application, rows []application.HistoryEntry) error {
	for _, h := range rows {
		if _, err := q.Exec(ctx, SQLInsertHistoryEntry,
			newUUIDv7(), app.ID, app.TenantID,
			string(h.From), string(h.To), nullStr(h.Reason), h.At,
		); err != nil {
			return fmt.Errorf("pg: insert history: %w", err)
		}
	}
	return nil
}

// loadApplicationHistory reads the append-only transition trail for an
// application (oldest-first), RLS already applied by the caller.
func loadApplicationHistory(ctx context.Context, q Querier, applicationID string) ([]application.HistoryEntry, error) {
	rows, err := q.Query(ctx, SQLListHistoryByApplication, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []application.HistoryEntry
	for rows.Next() {
		var (
			from, to string
			reason   *string
			at       time.Time
		)
		if err := rows.Scan(&from, &to, &reason, &at); err != nil {
			return nil, err
		}
		e := application.HistoryEntry{
			From: application.Status(from),
			To:   application.Status(to),
			At:   at,
		}
		if reason != nil {
			e.Reason = *reason
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListByGCID returns all of a user's applications.
func (r *ApplicationRepo) ListByGCID(ctx context.Context, tenantID, gcid string, offset, limit int) ([]*application.Application, int, error) {
	if r == nil || r.tx == nil {
		return nil, 0, ErrNotImplemented
	}
	var out []*application.Application
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, err := q.Query(ctx, SQLListApplicationsByGCID, tenantID, gcid, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			app, err := scanApplication(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, app)
		}
		return rows.Err()
	})
	return out, len(out), err
}

// ListByTenant returns the admin queue's tenant-wide list (optional status
// filter + pagination), newest-first. Total is the post-filter,
// pre-pagination count (computed in the same RLS-scoped transaction as the
// page query). Mirrors inmem.ApplicationRepo.ListByTenant.
func (r *ApplicationRepo) ListByTenant(ctx context.Context, in application.ListByTenantInput) ([]*application.Application, int, error) {
	if r == nil || r.tx == nil {
		return nil, 0, ErrNotImplemented
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	var (
		out   []*application.Application
		total int
	)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return fmt.Errorf("pg: list applications: apply rls session: %w", err)
		}
		// total first (post-filter, pre-pagination) for the FE count pill.
		// Errors are wrapped with the failing step so the admin handler's
		// 500 log names WHICH query failed (count vs list vs scan): the
		// difference between diagnosing a transient connection fault and a
		// bad-row decode when the queue 500s intermittently (CHO-2337).
		if in.Status == "" {
			if err := q.QueryRow(ctx, SQLCountApplicationsByTenant, in.TenantID).Scan(&total); err != nil {
				return fmt.Errorf("pg: count applications by tenant: %w", err)
			}
		} else {
			if err := q.QueryRow(ctx, SQLCountApplicationsByTenantStatus, in.TenantID, string(in.Status)).Scan(&total); err != nil {
				return fmt.Errorf("pg: count applications by tenant+status %q: %w", in.Status, err)
			}
		}
		var (
			rows Rows
			qErr error
		)
		if in.Status == "" {
			rows, qErr = q.Query(ctx, SQLListApplicationsByTenant, in.TenantID, limit, in.Offset)
		} else {
			rows, qErr = q.Query(ctx, SQLListApplicationsByTenantStatus, in.TenantID, string(in.Status), limit, in.Offset)
		}
		if qErr != nil {
			return fmt.Errorf("pg: list applications by tenant: %w", qErr)
		}
		defer rows.Close()
		for rows.Next() {
			app, err := scanApplication(rows.Scan)
			if err != nil {
				return fmt.Errorf("pg: scan application row: %w", err)
			}
			out = append(out, app)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("pg: iterate application rows: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// lookupByNaturalKey is the fall-through for conflict-on-natural-key.
func (r *ApplicationRepo) lookupByNaturalKey(ctx context.Context, tenantID, courseID, gcid string) (*application.Application, bool, error) {
	var existingID string
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectApplicationByNaturalKey, tenantID, courseID, gcid)
		return row.Scan(&existingID)
	})
	if err != nil {
		return nil, false, err
	}
	if existingID == "" {
		return nil, false, nil
	}
	return r.Get(ctx, tenantID, existingID)
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// qToExecer adapts Querier to rls.Execer.
type qExecer struct{ q Querier }

func (q qExecer) Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error) {
	return q.q.Exec(ctx, sql, args...)
}

func qToExecer(q Querier) rls.Execer { return qExecer{q: q} }

// scanApplication consumes a row scanner into a fresh Application aggregate.
func scanApplication(scan func(...any) error) (*application.Application, error) {
	var (
		id, tenantID, courseID, gcid, status string
		classID, paymentIntent, invoiceID    *string
		singpassSession, rejected, withdrawn *string
		offerExp, accepted, paid             *time.Time
		enrolled, withdrawnAt                *time.Time
		fundingJSON                          []byte
		createdAt, updatedAt                 time.Time
	)
	if err := scan(
		&id, &tenantID, &courseID, &classID, &gcid, &status,
		&offerExp, &accepted, &paid, &enrolled, &withdrawnAt,
		&paymentIntent, &invoiceID, &singpassSession,
		&rejected, &withdrawn, &fundingJSON,
		&createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	app := &application.Application{
		ID:        id,
		TenantID:  tenantID,
		CourseID:  courseID,
		GCID:      gcid,
		Status:    application.Status(status),
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
	if classID != nil {
		app.ClassID = *classID
	}
	if offerExp != nil {
		app.OfferExpiresAt = *offerExp
	}
	if accepted != nil {
		app.AcceptedAt = *accepted
	}
	if paid != nil {
		app.PaidAt = *paid
	}
	if enrolled != nil {
		app.EnrolledAt = *enrolled
	}
	if withdrawnAt != nil {
		app.WithdrawnAt = *withdrawnAt
	}
	if paymentIntent != nil {
		app.StripePaymentIntentID = *paymentIntent
	}
	if invoiceID != nil {
		app.InvoiceID = *invoiceID
	}
	if singpassSession != nil {
		app.SingpassSessionID = *singpassSession
	}
	if rejected != nil {
		app.RejectedReason = *rejected
	}
	if withdrawn != nil {
		app.WithdrawnReason = *withdrawn
	}
	if len(fundingJSON) > 0 {
		_ = json.Unmarshal(fundingJSON, &app.FundingLines)
	}
	return app, nil
}

// nullStr returns nil for empty strings (so Postgres NULLs match).
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullTime returns nil for zero times.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// newUUIDv7 mirrors application.newUUIDv7 but kept package-local so this
// adapter has no dependency on a specific UUID lib.
func newUUIDv7() string {
	return application.NewUUIDv7()
}
