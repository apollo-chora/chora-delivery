// campus.go - Postgres adapter for chora_delivery.campuses (CHO-2293).
//
// SCHEMA: see migrations/0058_campuses.up.sql.
//
// Mirrors room.go. Campus persists as REAL, queryable columns (not a JSONB
// snapshot) so the R+ Campus Operations list can scope and sort in SQL.
//
// RLS is ENABLED: campuses are admin CRUD, always tenant-scoped.
// rls.ApplySession runs before every query. Save returns an error (fail loud).
// GetForTenant returns its error, so an infra or RLS failure is LOUD and never a
// silent miss; ok=false means a GENUINE absent / soft-deleted / cross-tenant row
// (CHO-2184). A nil TxRunner makes every method fail loud with ErrNotImplemented,
// because an unwired repo is a WIRING BUG, not an empty database.
package pg

import (
	"context"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

const (
	// tenant_id + created_at are NOT touched on conflict: a campus cannot change
	// tenant, and created_at is immutable (the rooms / offering_sessions idiom).
	SQLUpsertCampus = `
INSERT INTO campuses (id, tenant_id, name, address_l1, address_l2, city, country, created_at, updated_at, deleted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (id) DO UPDATE SET
    name       = EXCLUDED.name,
    address_l1 = EXCLUDED.address_l1,
    address_l2 = EXCLUDED.address_l2,
    city       = EXCLUDED.city,
    country    = EXCLUDED.country,
    updated_at = EXCLUDED.updated_at,
    deleted_at = EXCLUDED.deleted_at`

	SQLGetCampus = `
SELECT id, tenant_id, name, address_l1, address_l2, city, country, created_at, updated_at, deleted_at
FROM campuses WHERE id = $1 AND deleted_at IS NULL`

	SQLListCampusesByTenant = `
SELECT id, tenant_id, name, address_l1, address_l2, city, country, created_at, updated_at, deleted_at
FROM campuses
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY created_at, id`
)

// CampusRepo is the Postgres-backed campusops.CampusStore.
type CampusRepo struct {
	tx TxRunner
}

// NewCampusRepo constructs a repo around a TxRunner. A nil TxRunner makes every
// method return ErrNotImplemented (fail loud); an unwired repo is a WIRING BUG,
// not an empty database (CHO-2184).
func NewCampusRepo(tx TxRunner) *CampusRepo { return &CampusRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ campusops.CampusStore = (*CampusRepo)(nil)

func (r *CampusRepo) Save(ctx context.Context, c *campusops.Campus) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if c == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertCampus,
			c.ID, c.TenantID, c.Name, c.AddressL1, c.AddressL2, c.City, c.Country,
			c.CreatedAt, c.UpdatedAt, nullTimePtr(c.DeletedAt),
		); err != nil {
			return err
		}
		return nil
	})
}

func (r *CampusRepo) GetForTenant(ctx context.Context, tenantID, id string) (*campusops.Campus, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *campusops.Campus
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		c, scanErr := scanCampus(q.QueryRow(ctx, SQLGetCampus, id).Scan)
		if scanErr != nil {
			if isNoRows(scanErr) {
				return nil // genuine miss: outer returns ok=false, err=nil
			}
			return scanErr
		}
		out = c
		return nil
	})
	if err != nil {
		return nil, false, err // LOUD: infra/RLS failure is NOT a miss (CHO-2184)
	}
	if out == nil {
		return nil, false, nil // genuine miss
	}
	// Belt-and-braces: RLS already scopes tenant, but never leak a cross-tenant
	// row even if a policy is misconfigured (mirrors RoomRepo).
	if out.TenantID != tenantID {
		return nil, false, nil
	}
	return out, true, nil
}

func (r *CampusRepo) ListByTenant(ctx context.Context, tenantID string) ([]*campusops.Campus, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	// Non-nil zero-length so the HTTP layer renders [] and never null.
	out := make([]*campusops.Campus, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListCampusesByTenant, tenantID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCampus(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanCampus consumes a row scanner into a fresh Campus aggregate. The nullable
// column (deleted_at) scans into a pointer type; the address/city columns are
// NOT NULL with a ” default, so they scan as plain strings.
func scanCampus(scan func(...any) error) (*campusops.Campus, error) {
	var (
		id, tenantID, name              string
		addressL1, addressL2, city, ctr string
		createdAt, updatedAt            time.Time
		deletedAt                       *time.Time
	)
	if err := scan(
		&id, &tenantID, &name, &addressL1, &addressL2, &city, &ctr,
		&createdAt, &updatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	return &campusops.Campus{
		ID:        id,
		TenantID:  tenantID,
		Name:      name,
		AddressL1: addressL1,
		AddressL2: addressL2,
		City:      city,
		Country:   ctr,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		DeletedAt: deletedAt,
	}, nil
}
