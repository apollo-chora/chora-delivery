// offering_attendance.go — Postgres adapter for chora_delivery.offering_attendance_records
// (R+ Four-Mode, Attendance tab).
//
// SCHEMA: see migrations/0037_offering_attendance_records.up.sql.
//
// Storage = JSONB aggregate snapshot keyed by id, with extracted columns
// tenant_id / session_id / gcid / recorded_at. The UNIQUE(tenant_id, session_id,
// gcid) natural key makes Upsert idempotent: a re-mark ON CONFLICT DO UPDATE
// replaces the snapshot + recorded_at (latest wins) rather than inserting a
// duplicate.
//
// RLS ENABLED (mirrors offering_sessions / offerings). rls.ApplySession runs
// before every query. Nil TxRunner → fail loud (ErrNotImplemented).
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	offeringattendance "github.com/apollo-chora/chora-delivery/internal/domain/offering_attendance"
)

const (
	SQLUpsertAttendanceRecord = `
INSERT INTO offering_attendance_records (id, tenant_id, session_id, gcid, recorded_at, data, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now(), now())
ON CONFLICT (tenant_id, session_id, gcid) DO UPDATE SET
    recorded_at = EXCLUDED.recorded_at,
    data        = EXCLUDED.data,
    updated_at  = now()`

	SQLListAttendanceBySession = `
SELECT data FROM offering_attendance_records
WHERE tenant_id = $1 AND session_id = $2
ORDER BY gcid`
)

// OfferingAttendanceRepo is the Postgres-backed offeringattendance.Store.
type OfferingAttendanceRepo struct {
	tx TxRunner
}

// NewOfferingAttendanceRepo constructs a repo around a TxRunner. A nil TxRunner
// makes Upsert + ListBySession return ErrNotImplemented (fail loud).
func NewOfferingAttendanceRepo(tx TxRunner) *OfferingAttendanceRepo {
	return &OfferingAttendanceRepo{tx: tx}
}

// Compile-time assertion: satisfies the domain port.
var _ offeringattendance.Store = (*OfferingAttendanceRepo)(nil)

func (r *OfferingAttendanceRepo) Upsert(ctx context.Context, rec *offeringattendance.Record) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if rec == nil {
		return nil
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("pg: marshal attendance_record: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertAttendanceRecord,
			rec.ID, rec.TenantID, rec.SessionID, rec.GCID, rec.RecordedAt, data,
		); err != nil {
			return fmt.Errorf("pg: upsert attendance_record: %w", err)
		}
		return nil
	})
}

func (r *OfferingAttendanceRepo) ListBySession(ctx context.Context, tenantID, sessionID string) ([]*offeringattendance.Record, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*offeringattendance.Record
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListAttendanceBySession, tenantID, sessionID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var rec offeringattendance.Record
			if err := json.Unmarshal(data, &rec); err != nil {
				return err
			}
			out = append(out, &rec)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
