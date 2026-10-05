// candidate.go — Postgres adapter for chora_delivery.exam_candidates
// (ADR-190 D2 identity-verified exam admission).
//
// SCHEMA: see migrations/0047_exam_candidates.up.sql (`exam_candidates` table).
//
// Storage = JSONB aggregate snapshot keyed by id, with tenant_id + exam_id +
// gcid + state + verification_status extracted for RLS scoping + roster
// listing + the (tenant, exam, gcid) admission lookup — the exam.go pattern
// (the Candidate is an FSM record, so the JSONB snapshot round-trips
// losslessly since every field is exported). RLS is ENABLED: exam_candidates
// is admin CRUD, always tenant-scoped, so rls.ApplySession (SET LOCAL
// chora.tenant_id) enforces tenant isolation as defence-in-depth.
//
// Save returns an error (fail loud — a dropped Save loses a candidate
// allocation). GetByExamAndGCID returns its error (CHO-2184); ListByExam
// returns the error — matching exam.CandidateStore.
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
	SQLUpsertCandidate = `
INSERT INTO exam_candidates (id, tenant_id, exam_id, gcid, state, verification_status, data, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO UPDATE SET
    state               = EXCLUDED.state,
    verification_status = EXCLUDED.verification_status,
    data                = EXCLUDED.data,
    updated_at          = EXCLUDED.updated_at`

	SQLGetCandidateByExamAndGCID = `
SELECT data FROM exam_candidates
WHERE tenant_id = $1 AND exam_id = $2 AND gcid = $3 AND deleted_at IS NULL`

	SQLListCandidatesByExam = `
SELECT data FROM exam_candidates
WHERE tenant_id = $1 AND exam_id = $2 AND deleted_at IS NULL
ORDER BY id`
)

// -----------------------------------------------------------------------------
// CandidateRepo
// -----------------------------------------------------------------------------

// CandidateRepo is the Postgres-backed exam.CandidateStore.
type CandidateRepo struct {
	tx TxRunner
}

// NewCandidateRepo constructs a CandidateRepo around a TxRunner. A nil
// TxRunner makes Save / ListByExam return ErrNotImplemented (fail loud); Get
// returns ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184).
func NewCandidateRepo(tx TxRunner) *CandidateRepo { return &CandidateRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ exam.CandidateStore = (*CandidateRepo)(nil)

func (r *CandidateRepo) Save(ctx context.Context, c *exam.Candidate) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if c == nil {
		return nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("pg: marshal candidate: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertCandidate,
			c.ID, c.TenantID, c.ExamID, c.GCID, string(c.State), string(c.VerificationStatus), data, c.CreatedAt, c.UpdatedAt); err != nil {
			return fmt.Errorf("pg: upsert candidate: %w", err)
		}
		return nil
	})
}

func (r *CandidateRepo) GetByExamAndGCID(ctx context.Context, tenantID, examID, gcid string) (*exam.Candidate, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *exam.Candidate
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetCandidateByExamAndGCID, tenantID, examID, gcid).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get candidate: %w", err)
		}
		var c exam.Candidate
		if err := json.Unmarshal(data, &c); err != nil {
			return fmt.Errorf("pg: unmarshal candidate: %w", err)
		}
		out = &c
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

func (r *CandidateRepo) ListByExam(ctx context.Context, tenantID, examID string) ([]*exam.Candidate, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*exam.Candidate
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListCandidatesByExam, tenantID, examID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var c exam.Candidate
			if err := json.Unmarshal(data, &c); err != nil {
				return err
			}
			out = append(out, &c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
