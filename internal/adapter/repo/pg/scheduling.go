// scheduling.go — Postgres adapter for chora_delivery.scheduled_classes
// (R+ durability sweep Wave 2 follow-up, CHO-1626).
//
// SCHEMA: see migrations/0025_scheduled_classes.up.sql.
//
// Replaces the in-memory inmem.SchedulingRepo. ScheduledClass gains a real
// write path (create / reschedule / cancel) in this follow-up, so the
// week-view list is no longer always-empty — and these rows must survive a
// pod restart, hence the pg adapter.
//
// Storage = JSONB aggregate snapshot keyed by id (the exam.go / survey.go
// pattern — every ScheduledClass data field is exported, only the unexported
// sync.Mutex is skipped, so json.Marshal round-trips losslessly). Extracted
// columns:
//   - tenant_id            : RLS scoping
//   - iso_year + iso_week  : the week-view filter (exact parity with the
//     in-memory InISOWeek check — c.StartsAt.ISOWeek())
//   - starts_at            : kept for ordering / future range queries
//
// RLS is ENABLED (mirrors 0019_bookings / 0020_exams / 0024_surveys):
// scheduling is admin CRUD, always tenant-scoped (create/list/get/reschedule/
// cancel all carry X-Tenant-Id). rls.ApplySession runs before every query.
//
// Save returns an error (fail loud). Get returns its error — an infra/RLS
// failure is LOUD, never a silent miss. ok=false means a GENUINE absent row
// (CHO-2184: the two were once the same answer, and a dead read passed for an
// empty one).
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

const (
	// CHO-2299: room_id + ends_at are EXTRACTED columns, written alongside the
	// JSONB snapshot, because the mig-0059 double-book EXCLUDE reads columns and
	// a gate on a column nothing writes can never fire. Both are refreshed on
	// conflict so a reschedule moves the guarded range instead of leaving a
	// stale one behind.
	SQLUpsertScheduledClass = `
INSERT INTO scheduled_classes (id, tenant_id, iso_year, iso_week, starts_at, ends_at, room_id, data, created_at, updated_at, deleted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO UPDATE SET
    iso_year   = EXCLUDED.iso_year,
    iso_week   = EXCLUDED.iso_week,
    starts_at  = EXCLUDED.starts_at,
    ends_at    = EXCLUDED.ends_at,
    room_id    = EXCLUDED.room_id,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at,
    deleted_at = EXCLUDED.deleted_at`

	SQLGetScheduledClass = `SELECT data FROM scheduled_classes WHERE id = $1 AND deleted_at IS NULL`

	SQLListScheduledClassesByTenantWeek = `
SELECT data FROM scheduled_classes
WHERE tenant_id = $1 AND iso_year = $2 AND iso_week = $3 AND deleted_at IS NULL
ORDER BY id`

	SQLListScheduledClassesByTenant = `
SELECT data FROM scheduled_classes
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY id`
)

// -----------------------------------------------------------------------------
// SchedulingRepo
// -----------------------------------------------------------------------------

// SchedulingRepo is the Postgres-backed scheduling.SchedulingStore.
type SchedulingRepo struct {
	tx TxRunner
}

// NewSchedulingRepo constructs a SchedulingRepo around a TxRunner. A nil
// TxRunner makes the write + list methods return ErrNotImplemented (fail loud)
// Get returns ErrNotImplemented too: an unwired repo is a WIRING BUG, not an
// empty database (CHO-2184).
func NewSchedulingRepo(tx TxRunner) *SchedulingRepo { return &SchedulingRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ scheduling.SchedulingStore = (*SchedulingRepo)(nil)

func (r *SchedulingRepo) Save(ctx context.Context, c *scheduling.ScheduledClass) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if c == nil {
		return nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("pg: marshal scheduled_class: %w", err)
	}
	isoYear, isoWeek := c.StartsAt.UTC().ISOWeek()
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertScheduledClass,
			c.ID, c.TenantID, isoYear, isoWeek, c.StartsAt, c.EndsAt, nullStr(c.RoomID),
			data, c.CreatedAt, c.UpdatedAt, nullTimePtr(c.DeletedAt),
		); err != nil {
			// A real room clash must surface as the DOMAIN sentinel so the handler
			// maps it to 409 without matching on driver text (mirrors
			// offering_session.go; ADR-237 O3 / CHO-2299).
			if isExclusionViolation(err) {
				return scheduling.ErrRoomDoubleBooked
			}
			return fmt.Errorf("pg: upsert scheduled_class: %w", err)
		}
		return nil
	})
}

func (r *SchedulingRepo) Get(ctx context.Context, id string) (*scheduling.ScheduledClass, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *scheduling.ScheduledClass
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetScheduledClass, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get scheduled class: %w", err)
		}
		var c scheduling.ScheduledClass
		if err := json.Unmarshal(data, &c); err != nil {
			return fmt.Errorf("pg: unmarshal scheduled_class: %w", err)
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

func (r *SchedulingRepo) ListByTenantWeek(ctx context.Context, tenantID string, year, week int) ([]*scheduling.ScheduledClass, error) {
	return r.listScheduled(ctx, SQLListScheduledClassesByTenantWeek, tenantID, year, week)
}

func (r *SchedulingRepo) ListByTenant(ctx context.Context, tenantID string) ([]*scheduling.ScheduledClass, error) {
	return r.listScheduled(ctx, SQLListScheduledClassesByTenant, tenantID)
}

// listScheduled runs a tenant-scoped (RLS-first) SELECT of `data` columns and
// rehydrates each JSONB snapshot. args are the positional bind params after
// the SQL (tenantID alone, or tenantID+year+week for the week query).
func (r *SchedulingRepo) listScheduled(ctx context.Context, sql string, args ...any) ([]*scheduling.ScheduledClass, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*scheduling.ScheduledClass
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, sql, args...)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var c scheduling.ScheduledClass
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
