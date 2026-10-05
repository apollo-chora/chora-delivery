// room.go — Postgres adapter for chora_delivery.rooms (CHO-2191 SP1).
//
// SCHEMA: see migrations/0051_rooms.up.sql.
//
// Unlike the JSONB-snapshot adapters (offering_sessions / scheduled_classes),
// Room persists as REAL, queryable columns — the ratified room_id-keyed
// double-book / over-capacity gate (SP2) must SELECT / constrain on tenant_id,
// capacity, etc. campus_id + branch_id are nullable (a scheduling room needs no
// campus) — bound via the nullStr NULL-coercion helper.
//
// RLS is ENABLED: rooms are admin CRUD, always tenant-scoped. rls.ApplySession
// runs before every query. Save returns an error (fail loud). GetForTenant
// returns its error — an infra/RLS failure is LOUD, never a silent miss.
// ok=false means a GENUINE absent / soft-deleted / cross-tenant row (CHO-2184).
// A nil TxRunner makes every method fail loud with ErrNotImplemented — an
// unwired repo is a WIRING BUG, not an empty database.
package pg

import (
	"context"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

const (
	// tenant_id + created_at are NOT touched on conflict — a room can't change
	// tenant, and created_at is immutable (the offering_sessions upsert pattern).
	SQLUpsertRoom = `
INSERT INTO rooms (id, tenant_id, campus_id, branch_id, name, capacity, created_at, updated_at, deleted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO UPDATE SET
    campus_id  = EXCLUDED.campus_id,
    branch_id  = EXCLUDED.branch_id,
    name       = EXCLUDED.name,
    capacity   = EXCLUDED.capacity,
    updated_at = EXCLUDED.updated_at,
    deleted_at = EXCLUDED.deleted_at`

	SQLGetRoom = `
SELECT id, tenant_id, campus_id, branch_id, name, capacity, created_at, updated_at, deleted_at
FROM rooms WHERE id = $1 AND deleted_at IS NULL`

	SQLListRoomsByTenant = `
SELECT id, tenant_id, campus_id, branch_id, name, capacity, created_at, updated_at, deleted_at
FROM rooms
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY created_at, id`
)

// RoomRepo is the Postgres-backed campusops.RoomStore.
type RoomRepo struct {
	tx TxRunner
}

// NewRoomRepo constructs a repo around a TxRunner. A nil TxRunner makes every
// method return ErrNotImplemented (fail loud); an unwired repo is a WIRING BUG,
// not an empty database (CHO-2184).
func NewRoomRepo(tx TxRunner) *RoomRepo { return &RoomRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ campusops.RoomStore = (*RoomRepo)(nil)

func (r *RoomRepo) Save(ctx context.Context, rm *campusops.Room) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if rm == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertRoom,
			rm.ID, rm.TenantID, nullStr(rm.CampusID), nullStr(rm.BranchID),
			rm.Name, rm.Capacity, rm.CreatedAt, rm.UpdatedAt, nullTimePtr(rm.DeletedAt),
		); err != nil {
			return err
		}
		return nil
	})
}

func (r *RoomRepo) GetForTenant(ctx context.Context, tenantID, id string) (*campusops.Room, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *campusops.Room
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rm, scanErr := scanRoom(q.QueryRow(ctx, SQLGetRoom, id).Scan)
		if scanErr != nil {
			if isNoRows(scanErr) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return scanErr
		}
		out = rm
		return nil
	})
	if err != nil {
		return nil, false, err // LOUD: infra/RLS failure is NOT a miss (CHO-2184)
	}
	if out == nil {
		return nil, false, nil // genuine miss
	}
	// Belt-and-braces: RLS already scopes tenant, but never leak a cross-tenant
	// row even if a policy is misconfigured (mirrors the offering handler's
	// explicit TenantID compare).
	if out.TenantID != tenantID {
		return nil, false, nil
	}
	return out, true, nil
}

func (r *RoomRepo) ListByTenant(ctx context.Context, tenantID string) ([]*campusops.Room, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*campusops.Room
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListRoomsByTenant, tenantID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			rm, err := scanRoom(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, rm)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanRoom consumes a row scanner into a fresh Room aggregate. Nullable columns
// (campus_id, branch_id, deleted_at) scan into pointer types and dereference
// only when present — the scanApplication idiom.
func scanRoom(scan func(...any) error) (*campusops.Room, error) {
	var (
		id, tenantID, name   string
		campusID, branchID   *string
		capacity             int
		createdAt, updatedAt time.Time
		deletedAt            *time.Time
	)
	if err := scan(
		&id, &tenantID, &campusID, &branchID, &name, &capacity,
		&createdAt, &updatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	rm := &campusops.Room{
		ID:        id,
		TenantID:  tenantID,
		Name:      name,
		Capacity:  capacity,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		DeletedAt: deletedAt,
	}
	if campusID != nil {
		rm.CampusID = *campusID
	}
	if branchID != nil {
		rm.BranchID = *branchID
	}
	return rm, nil
}
