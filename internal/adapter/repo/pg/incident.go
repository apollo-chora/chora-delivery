// incident.go — Postgres adapter for chora_delivery.incident_reports
// (W4 Brick-B). APPEND-ONLY: Append issues a plain INSERT (never an UPDATE);
// the migration grants the app role SELECT + INSERT only (no UPDATE/DELETE), so
// append-only is enforced at the DB, not merely by convention.
//
// SCHEMA: see migrations/0048_exam_sittings_invigilators_incidents.up.sql.
//
// Storage = JSONB aggregate snapshot keyed by id, with tenant_id + sitting_id +
// reported_by_gcid + kind extracted for RLS scoping + the ListBySitting audit
// trail (candidate_ref rides in the JSONB snapshot only — optional, not a query
// axis). RLS is ENABLED (rls.ApplySession before every query).
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

const (
	// Append-only — a plain INSERT with NO ON CONFLICT / DO UPDATE clause.
	SQLAppendIncident = `
INSERT INTO incident_reports (id, tenant_id, sitting_id, reported_by_gcid, kind, data, occurred_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	SQLGetIncident = `
SELECT data FROM incident_reports
WHERE id = $1`

	SQLListIncidentsBySitting = `
SELECT data FROM incident_reports
WHERE tenant_id = $1 AND sitting_id = $2
ORDER BY id`
)

// IncidentRepo is the Postgres-backed exam.IncidentReportStore.
type IncidentRepo struct {
	tx TxRunner
}

// NewIncidentRepo constructs an IncidentRepo around a TxRunner.
func NewIncidentRepo(tx TxRunner) *IncidentRepo { return &IncidentRepo{tx: tx} }

var _ exam.IncidentReportStore = (*IncidentRepo)(nil)

func (r *IncidentRepo) Append(ctx context.Context, ir *exam.IncidentReport) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if ir == nil {
		return nil
	}
	data, err := json.Marshal(ir)
	if err != nil {
		return fmt.Errorf("pg: marshal incident: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLAppendIncident,
			ir.ID, ir.TenantID, ir.SittingID, ir.ReportedByGCID, string(ir.Kind), data, ir.OccurredAt, ir.CreatedAt); err != nil {
			return fmt.Errorf("pg: append incident: %w", err)
		}
		return nil
	})
}

func (r *IncidentRepo) Get(ctx context.Context, id string) (*exam.IncidentReport, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *exam.IncidentReport
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetIncident, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get incident report: %w", err)
		}
		var ir exam.IncidentReport
		if err := json.Unmarshal(data, &ir); err != nil {
			return fmt.Errorf("pg: unmarshal incident: %w", err)
		}
		out = &ir
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

func (r *IncidentRepo) ListBySitting(ctx context.Context, tenantID, sittingID string) ([]*exam.IncidentReport, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*exam.IncidentReport
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListIncidentsBySitting, tenantID, sittingID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var ir exam.IncidentReport
			if err := json.Unmarshal(data, &ir); err != nil {
				return err
			}
			out = append(out, &ir)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
