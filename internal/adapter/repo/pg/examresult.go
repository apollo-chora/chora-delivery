// examresult.go — Postgres adapter for chora_delivery.exam_results (W4 Brick-1).
//
// SCHEMA: see migrations/0046_exam_forms_results.up.sql (`exam_results` table).
//
// The durable exam_results row is the SOURCE OF TRUTH for a candidate's
// compliance outcome (ADR-190 D2 cut-score → pass/fail → certificate chain),
// so Save is a write-once idempotent INSERT (ON CONFLICT (id) DO NOTHING) — a
// scored result is immutable and never overwritten. Storage = JSONB aggregate
// snapshot + extracted columns (tenant_id, exam_id, exam_form_id, candidate_ref,
// outcome, raw_score, max_score) for RLS scoping + keying/listing, the 0020_exams
// pattern. RLS ENABLED; rls.ApplySession runs before every query.
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// -----------------------------------------------------------------------------
// SQL templates
// -----------------------------------------------------------------------------

const (
	SQLInsertExamResult = `
INSERT INTO exam_results (id, tenant_id, exam_id, exam_form_id, candidate_ref, outcome, raw_score, max_score, data, scored_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO NOTHING`

	SQLGetExamResult = `SELECT data FROM exam_results WHERE id = $1 AND deleted_at IS NULL`

	SQLListExamResultsByForm = `
SELECT data FROM exam_results
WHERE tenant_id = $1 AND exam_form_id = $2 AND deleted_at IS NULL
ORDER BY id`

	SQLListExamResultsByExam = `
SELECT data FROM exam_results
WHERE tenant_id = $1 AND exam_id = $2 AND deleted_at IS NULL
ORDER BY scored_at DESC, id DESC`
)

// -----------------------------------------------------------------------------
// ExamResultRepo
// -----------------------------------------------------------------------------

// ExamResultRepo is the Postgres-backed exam.ExamResultStore.
type ExamResultRepo struct {
	tx TxRunner
}

// NewExamResultRepo constructs an ExamResultRepo around a TxRunner. A nil
// TxRunner makes Save / ListByForm return ErrNotImplemented (fail loud); Get
// returns ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184).
func NewExamResultRepo(tx TxRunner) *ExamResultRepo { return &ExamResultRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ exam.ExamResultStore = (*ExamResultRepo)(nil)

func (r *ExamResultRepo) Save(ctx context.Context, res *exam.ExamResult) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if res == nil {
		return nil
	}
	data, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("pg: marshal exam_result: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLInsertExamResult,
			res.ID, res.TenantID, res.ExamID, res.ExamFormID, res.CandidateRef,
			string(res.Outcome), res.RawScore, res.MaxScore, data, res.ScoredAt, res.CreatedAt,
		); err != nil {
			return fmt.Errorf("pg: insert exam_result: %w", err)
		}
		return nil
	})
}

func (r *ExamResultRepo) Get(ctx context.Context, id string) (*exam.ExamResult, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *exam.ExamResult
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetExamResult, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get exam result: %w", err)
		}
		var res exam.ExamResult
		if err := json.Unmarshal(data, &res); err != nil {
			return fmt.Errorf("pg: unmarshal exam_result: %w", err)
		}
		out = &res
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

func (r *ExamResultRepo) ListByForm(ctx context.Context, tenantID, examFormID string) ([]*exam.ExamResult, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*exam.ExamResult
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListExamResultsByForm, tenantID, examFormID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var res exam.ExamResult
			if err := json.Unmarshal(data, &res); err != nil {
				return err
			}
			out = append(out, &res)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListByExam returns the tenant's results across ALL forms of one exam,
// most-recent first (CHO-2104 — the R+ Results roster).
func (r *ExamResultRepo) ListByExam(ctx context.Context, tenantID, examID string) ([]*exam.ExamResult, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*exam.ExamResult
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListExamResultsByExam, tenantID, examID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var res exam.ExamResult
			if err := json.Unmarshal(data, &res); err != nil {
				return err
			}
			out = append(out, &res)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
