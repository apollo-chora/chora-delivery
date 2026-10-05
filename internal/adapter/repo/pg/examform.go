// examform.go — Postgres adapter for chora_delivery.exam_forms (W4 Brick-1).
//
// SCHEMA: see migrations/0046_exam_forms_results.up.sql (`exam_forms` table).
//
// Storage = JSONB aggregate snapshot keyed by id, with tenant_id + exam_id +
// state extracted for RLS scoping + listing — the 0020_exams / live_poll.go
// pattern (the ExamForm is a variable-shape FSM record carrying pinned items +
// a cut-score value object, so a flat-column mapping would be brittle; the JSONB
// snapshot round-trips losslessly since every ExamForm field is exported). RLS
// is ENABLED: exam-form CRUD is always tenant-scoped, so rls.ApplySession
// enforces tenant isolation as defence-in-depth (the exam.go / booking.go
// rationale).
//
// Save returns an error (fail loud — a dropped Save loses an exam form). Get +
// ListByExam return their errors (CHO-2184), matching exam.ExamFormStore.
package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

const (
	SQLUpsertExamForm = `
INSERT INTO exam_forms (id, tenant_id, exam_id, item_bank_id, state, data, created_at, updated_at, exposed_at, retired_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (id) DO UPDATE SET
    state      = EXCLUDED.state,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at,
    exposed_at = EXCLUDED.exposed_at,
    retired_at = EXCLUDED.retired_at`

	SQLGetExamForm = `SELECT data FROM exam_forms WHERE id = $1 AND deleted_at IS NULL`

	SQLListExamFormsByExam = `
SELECT data FROM exam_forms
WHERE tenant_id = $1 AND exam_id = $2 AND deleted_at IS NULL
ORDER BY id`
)

// -----------------------------------------------------------------------------
// ExamFormRepo
// -----------------------------------------------------------------------------

// ExamFormRepo is the Postgres-backed exam.ExamFormStore.
type ExamFormRepo struct {
	tx TxRunner
}

// NewExamFormRepo constructs an ExamFormRepo around a TxRunner. A nil TxRunner
// makes Save / ListByExam return ErrNotImplemented (fail loud); Get returns
// ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184).
func NewExamFormRepo(tx TxRunner) *ExamFormRepo { return &ExamFormRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ exam.ExamFormStore = (*ExamFormRepo)(nil)

func (r *ExamFormRepo) Save(ctx context.Context, f *exam.ExamForm) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if f == nil {
		return nil
	}
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("pg: marshal exam_form: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertExamForm,
			f.ID, f.TenantID, f.ExamID, f.ItemBankID, string(f.State), data,
			f.CreatedAt, f.UpdatedAt, nullTime2(f.ExposedAt), nullTime2(f.RetiredAt),
		); err != nil {
			return fmt.Errorf("pg: upsert exam_form: %w", err)
		}
		return nil
	})
}

func (r *ExamFormRepo) Get(ctx context.Context, id string) (*exam.ExamForm, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *exam.ExamForm
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetExamForm, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get exam form: %w", err)
		}
		var f exam.ExamForm
		if err := json.Unmarshal(data, &f); err != nil {
			return fmt.Errorf("pg: unmarshal exam_form: %w", err)
		}
		out = &f
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

func (r *ExamFormRepo) ListByExam(ctx context.Context, tenantID, examID string) ([]*exam.ExamForm, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*exam.ExamForm
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListExamFormsByExam, tenantID, examID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var f exam.ExamForm
			if err := json.Unmarshal(data, &f); err != nil {
				return err
			}
			out = append(out, &f)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// nullTime2 returns nil for a nil / zero *time.Time so Postgres stores NULL in
// the extracted exposed_at / retired_at columns (the authoritative value stays
// in the JSONB snapshot). Named distinctly from application.go's nullTime
// (which takes a value time.Time).
func nullTime2(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return *t
}
