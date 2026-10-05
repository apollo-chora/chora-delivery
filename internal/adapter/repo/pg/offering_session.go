// offering_session.go — Postgres adapter for chora_delivery.offering_sessions
// (R+ Four-Mode, Schedule & Rooms tab).
//
// SCHEMA: see migrations/0036_offering_sessions.up.sql.
//
// Storage = JSONB aggregate snapshot keyed by id (the scheduled_classes /
// offerings pattern — every OfferingSession field is exported, so json.Marshal
// round-trips losslessly). Extracted columns:
//   - tenant_id   : RLS scoping
//   - offering_id : the schedule list filter
//   - starts_at   : ordering
//   - ends_at     : the room_id double-book range key (mig 0052)
//   - room_id     : the room_id double-book EXCLUDE key (mig 0052, ratified)
//
// RLS is ENABLED (mirrors scheduled_classes / offerings): sessions are admin
// CRUD, always tenant-scoped. rls.ApplySession runs before every query.
//
// Save returns an error (fail loud). Get returns its error — an infra/RLS
// failure is LOUD, never a silent miss. ok=false means a GENUINE absent row
// (CHO-2184: the two were once the same answer, and a dead read passed for an
// empty one). A nil TxRunner makes the write + list methods return
// ErrNotImplemented — fail loud, never a fake empty list.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/apollo-chora/chora-common/rls"
	offeringsession "github.com/apollo-chora/chora-delivery/internal/domain/offering_session"
)

// isExclusionViolation reports whether err is a Postgres exclusion_violation
// (SQLSTATE 23P01) — the room_id-overlap EXCLUDE gate (mig 0052) breaching. Kept
// here (not a stub) so a real double-book surfaces as ErrRoomDoubleBooked → 409
// rather than a masked 500.
func isExclusionViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23P01"
}

const (
	// ends_at + room_id are EXTRACTED columns (mig 0052) so the ratified
	// room_id-keyed EXCLUDE no-double-book gate can see them; the JSONB `data`
	// stays the lossless source of truth (it still carries the Room display
	// name). The slice-1 `room` string column is no longer written (superseded).
	SQLUpsertOfferingSession = `
INSERT INTO offering_sessions (id, tenant_id, offering_id, starts_at, ends_at, room_id, data, created_at, updated_at, deleted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (id) DO UPDATE SET
    offering_id = EXCLUDED.offering_id,
    starts_at   = EXCLUDED.starts_at,
    ends_at     = EXCLUDED.ends_at,
    room_id     = EXCLUDED.room_id,
    data        = EXCLUDED.data,
    updated_at  = EXCLUDED.updated_at,
    deleted_at  = EXCLUDED.deleted_at`

	SQLGetOfferingSession = `SELECT data FROM offering_sessions WHERE id = $1 AND deleted_at IS NULL`

	SQLListOfferingSessionsByOffering = `
SELECT data FROM offering_sessions
WHERE tenant_id = $1 AND offering_id = $2 AND deleted_at IS NULL
ORDER BY starts_at, id`
)

// OfferingSessionRepo is the Postgres-backed offeringsession.Store.
type OfferingSessionRepo struct {
	tx TxRunner
}

// NewOfferingSessionRepo constructs a repo around a TxRunner. A nil TxRunner
// makes the write + list methods return ErrNotImplemented (fail loud); Get
// returns ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184).
func NewOfferingSessionRepo(tx TxRunner) *OfferingSessionRepo { return &OfferingSessionRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ offeringsession.Store = (*OfferingSessionRepo)(nil)

func (r *OfferingSessionRepo) Save(ctx context.Context, s *offeringsession.OfferingSession) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if s == nil {
		return nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("pg: marshal offering_session: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertOfferingSession,
			s.ID, s.TenantID, s.OfferingID, s.StartsAt, s.EndsAt, nullStr(s.RoomID), data, s.CreatedAt, s.UpdatedAt, nullTimePtr(s.DeletedAt),
		); err != nil {
			if isExclusionViolation(err) {
				return offeringsession.ErrRoomDoubleBooked
			}
			return fmt.Errorf("pg: upsert offering_session: %w", err)
		}
		return nil
	})
}

func (r *OfferingSessionRepo) Get(ctx context.Context, id string) (*offeringsession.OfferingSession, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *offeringsession.OfferingSession
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetOfferingSession, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get offering session: %w", err)
		}
		var s offeringsession.OfferingSession
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("pg: unmarshal offering_session: %w", err)
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

func (r *OfferingSessionRepo) ListByOffering(ctx context.Context, tenantID, offeringID string) ([]*offeringsession.OfferingSession, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*offeringsession.OfferingSession
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListOfferingSessionsByOffering, tenantID, offeringID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var s offeringsession.OfferingSession
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
