// exam.go — Postgres adapter for chora_delivery.exams (R+ durability sweep).
//
// SCHEMA: see migrations/0020_exams.up.sql (`exams` table).
//
// Replaces the in-memory inmem.ExamRepo. Before this adapter, exams lived only
// in memory — rows were lost on pod restart. This is the durability close for
// the Exam aggregate (the sibling of CHO-1622 Bookings), per
// [[feedback-resilience-priority]].
//
// Storage = JSONB aggregate snapshot keyed by id, with tenant_id + state
// extracted for RLS scoping + listing — the live_poll.go pattern (the Exam
// aggregate is a 20-field FSM record, so a flat-column mapping like booking.go
// would be brittle; the JSONB snapshot round-trips losslessly since every Exam
// field is exported). RLS is ENABLED (unlike live_poll/0017/0018 whose
// tenant-less WS by-id resolve forced handler-only isolation): exams is admin
// CRUD, always tenant-scoped, so rls.ApplySession enforces tenant isolation as
// defence-in-depth (the booking.go rationale).
//
// Save returns an error (fail loud — a dropped Save loses an exam sitting).
// Get returns its error — an infra/RLS failure is LOUD, never a silent miss.
// ok=false means a GENUINE absent row (CHO-2184: the two were once the same
// answer, and a dead read passed for an empty one), matching exam.ExamStore.
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

const (
	SQLUpsertExam = `
INSERT INTO exams (id, tenant_id, state, data, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
    state      = EXCLUDED.state,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at`

	SQLGetExam = `SELECT data FROM exams WHERE id = $1 AND deleted_at IS NULL`

	SQLListExamsByTenant = `
SELECT data FROM exams
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY id`
)

// -----------------------------------------------------------------------------
// ExamRepo
// -----------------------------------------------------------------------------

// ExamRepo is the Postgres-backed exam.ExamStore.
type ExamRepo struct {
	tx TxRunner
}

// NewExamRepo constructs an ExamRepo around a TxRunner. A nil TxRunner makes
// Save / ListByTenant return ErrNotImplemented (fail loud); Get returns
// ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184).
func NewExamRepo(tx TxRunner) *ExamRepo { return &ExamRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ exam.ExamStore = (*ExamRepo)(nil)

func (r *ExamRepo) Save(ctx context.Context, e *exam.Exam) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if e == nil {
		return nil
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("pg: marshal exam: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertExam, e.ID, e.TenantID, string(e.State), data, e.CreatedAt, e.UpdatedAt); err != nil {
			return fmt.Errorf("pg: upsert exam: %w", err)
		}
		return nil
	})
}

func (r *ExamRepo) Get(ctx context.Context, id string) (*exam.Exam, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *exam.Exam
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetExam, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get exam: %w", err)
		}
		var e exam.Exam
		if err := json.Unmarshal(data, &e); err != nil {
			return fmt.Errorf("pg: unmarshal exam: %w", err)
		}
		out = &e
		return nil
	})
	if err != nil {
		return nil, false, err // LOUD: infra/RLS failure is NOT a miss (CHO-2184)
	}
	if out == nil {
		return nil, false, nil // genuine miss
	}
	return out, true, nil
}

func (r *ExamRepo) ListByTenant(ctx context.Context, tenantID string) ([]*exam.Exam, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*exam.Exam
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListExamsByTenant, tenantID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var e exam.Exam
			if err := json.Unmarshal(data, &e); err != nil {
				return err
			}
			out = append(out, &e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
