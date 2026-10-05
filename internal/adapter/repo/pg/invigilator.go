// invigilator.go — Postgres adapter for chora_delivery.exam_invigilators
// (W4 Brick-B).
//
// SCHEMA: see migrations/0048_exam_sittings_invigilators_incidents.up.sql.
//
// Storage = JSONB aggregate snapshot keyed by id, with tenant_id + sitting_id +
// invigilator_gcid + rank extracted for RLS scoping, the ListBySitting roster,
// and — critically — the partial-UNIQUE chief index (WHERE rank =
// 'chief_invigilator' AND deleted_at IS NULL) that backstops the single-chief
// invariant (ADR-191 O1). RLS is ENABLED (rls.ApplySession before every query).
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

const (
	SQLUpsertInvigilator = `
INSERT INTO exam_invigilators (id, tenant_id, sitting_id, invigilator_gcid, rank, data, assigned_at, updated_at, deleted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO UPDATE SET
    rank       = EXCLUDED.rank,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at,
    deleted_at = EXCLUDED.deleted_at`

	SQLGetInvigilator = `
SELECT data FROM exam_invigilators
WHERE id = $1 AND deleted_at IS NULL`

	SQLListInvigilatorsBySitting = `
SELECT data FROM exam_invigilators
WHERE tenant_id = $1 AND sitting_id = $2 AND deleted_at IS NULL
ORDER BY id`
)

// InvigilatorRepo is the Postgres-backed exam.ExamInvigilatorStore.
type InvigilatorRepo struct {
	tx TxRunner
}

// NewInvigilatorRepo constructs an InvigilatorRepo around a TxRunner.
func NewInvigilatorRepo(tx TxRunner) *InvigilatorRepo { return &InvigilatorRepo{tx: tx} }

var _ exam.ExamInvigilatorStore = (*InvigilatorRepo)(nil)

func (r *InvigilatorRepo) Save(ctx context.Context, iv *exam.ExamInvigilator) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if iv == nil {
		return nil
	}
	data, err := json.Marshal(iv)
	if err != nil {
		return fmt.Errorf("pg: marshal invigilator: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertInvigilator,
			iv.ID, iv.TenantID, iv.SittingID, iv.InvigilatorGCID, string(iv.Rank), data, iv.AssignedAt, iv.UpdatedAt, iv.DeletedAt); err != nil {
			return fmt.Errorf("pg: upsert invigilator: %w", err)
		}
		return nil
	})
}

func (r *InvigilatorRepo) Get(ctx context.Context, id string) (*exam.ExamInvigilator, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *exam.ExamInvigilator
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetInvigilator, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get invigilator: %w", err)
		}
		var iv exam.ExamInvigilator
		if err := json.Unmarshal(data, &iv); err != nil {
			return fmt.Errorf("pg: unmarshal invigilator: %w", err)
		}
		out = &iv
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

func (r *InvigilatorRepo) ListBySitting(ctx context.Context, tenantID, sittingID string) ([]*exam.ExamInvigilator, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*exam.ExamInvigilator
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListInvigilatorsBySitting, tenantID, sittingID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var iv exam.ExamInvigilator
			if err := json.Unmarshal(data, &iv); err != nil {
				return err
			}
			out = append(out, &iv)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
