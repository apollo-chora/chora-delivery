// sitting.go — Postgres adapter for chora_delivery.exam_sittings (W4 Brick-B).
//
// SCHEMA: see migrations/0048_exam_sittings_invigilators_incidents.up.sql.
//
// Storage = JSONB aggregate snapshot keyed by id, with tenant_id + exam_id +
// state extracted for RLS scoping + the ListByExam roster query (the exam.go
// JSONB-snapshot pattern — every ExamSitting field is exported, so the
// round-trip is lossless). deleted_at is extracted so soft-delete filters at the
// SQL layer. RLS is ENABLED: exam_sittings is admin CRUD, always tenant-scoped,
// so rls.ApplySession (SET LOCAL chora.tenant_id) enforces tenant isolation.
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

const (
	SQLUpsertSitting = `
INSERT INTO exam_sittings (id, tenant_id, exam_id, state, data, created_at, updated_at, deleted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO UPDATE SET
    state      = EXCLUDED.state,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at,
    deleted_at = EXCLUDED.deleted_at`

	SQLGetSitting = `
SELECT data FROM exam_sittings
WHERE id = $1 AND deleted_at IS NULL`

	SQLListSittingsByExam = `
SELECT data FROM exam_sittings
WHERE tenant_id = $1 AND exam_id = $2 AND deleted_at IS NULL
ORDER BY id`
)

// SittingRepo is the Postgres-backed exam.ExamSittingStore.
type SittingRepo struct {
	tx TxRunner
}

// NewSittingRepo constructs a SittingRepo around a TxRunner. A nil TxRunner
// makes Save / ListByExam return ErrNotImplemented (fail loud); Get returns
// ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184).
func NewSittingRepo(tx TxRunner) *SittingRepo { return &SittingRepo{tx: tx} }

var _ exam.ExamSittingStore = (*SittingRepo)(nil)

func (r *SittingRepo) Save(ctx context.Context, s *exam.ExamSitting) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if s == nil {
		return nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("pg: marshal sitting: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertSitting,
			s.ID, s.TenantID, s.ExamID, string(s.State), data, s.CreatedAt, s.UpdatedAt, s.DeletedAt); err != nil {
			return fmt.Errorf("pg: upsert sitting: %w", err)
		}
		return nil
	})
}

func (r *SittingRepo) Get(ctx context.Context, id string) (*exam.ExamSitting, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *exam.ExamSitting
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetSitting, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get sitting: %w", err)
		}
		var s exam.ExamSitting
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("pg: unmarshal sitting: %w", err)
		}
		out = &s
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

func (r *SittingRepo) ListByExam(ctx context.Context, tenantID, examID string) ([]*exam.ExamSitting, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*exam.ExamSitting
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListSittingsByExam, tenantID, examID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var s exam.ExamSitting
			if err := json.Unmarshal(data, &s); err != nil {
				return err
			}
			out = append(out, &s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
