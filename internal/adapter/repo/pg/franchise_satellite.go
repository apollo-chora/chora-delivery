// franchise_satellite.go: Postgres adapter for
// chora_delivery.franchise_satellite (ADR-192 D1, W5 bypass-free slice).
//
// SCHEMA: migrations/0056_franchise_satellite.up.sql. FLAT COLUMNS on purpose
// (not the JSONB-snapshot idiom of the exam aggregates): the future
// exam_owner_rollup policy subqueries
//
//	SELECT satellite_tenant_id FROM franchise_satellite
//	WHERE owner_tenant_id = NULLIF(current_setting('chora.exam_rollup_owner', true), '')::uuid
//
// in SQL, so the policy's keys must be real columns.
//
// RLS: tenant_isolation scopes rows to the OWNER tenant via chora.tenant_id;
// every method calls rls.ApplySession first (tenant from ctx, never a SQL
// arg). Soft delete only: Revoke is an explicit UPDATE stamping deleted_at
// (an upsert cannot soft-delete); Get/List filter deleted_at IS NULL.
package pg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/franchise"
)

// -----------------------------------------------------------------------------
// SQL templates
// -----------------------------------------------------------------------------

const (
	SQLInsertFranchiseSatellite = `
INSERT INTO franchise_satellite (id, owner_tenant_id, satellite_tenant_id, created_by_gcid, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)`

	SQLGetFranchiseSatellite = `
SELECT id, owner_tenant_id, satellite_tenant_id, created_by_gcid, created_at, updated_at
FROM franchise_satellite
WHERE id = $1 AND deleted_at IS NULL`

	SQLListFranchiseSatellitesByOwner = `
SELECT id, owner_tenant_id, satellite_tenant_id, created_by_gcid, created_at, updated_at
FROM franchise_satellite
WHERE owner_tenant_id = $1 AND deleted_at IS NULL
ORDER BY created_at, id`

	SQLRevokeFranchiseSatellite = `
UPDATE franchise_satellite
SET deleted_at = $2, updated_at = $2
WHERE id = $1 AND deleted_at IS NULL`
)

// -----------------------------------------------------------------------------
// FranchiseSatelliteRepo
// -----------------------------------------------------------------------------

// FranchiseSatelliteRepo is the Postgres-backed franchise.Store.
type FranchiseSatelliteRepo struct {
	tx TxRunner
}

// NewFranchiseSatelliteRepo constructs the repo around a TxRunner. A nil
// TxRunner makes every method return ErrNotImplemented (an unwired repo is a
// WIRING BUG, not an empty database, CHO-2184).
func NewFranchiseSatelliteRepo(tx TxRunner) *FranchiseSatelliteRepo {
	return &FranchiseSatelliteRepo{tx: tx}
}

// Compile-time assertion: satisfies the domain port.
var _ franchise.Store = (*FranchiseSatelliteRepo)(nil)

// Save inserts a live mapping. A 23505 on the live-pair partial unique index
// surfaces as franchise.ErrDuplicateMapping so the handler can 409.
func (r *FranchiseSatelliteRepo) Save(ctx context.Context, m *franchise.FranchiseSatellite) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if m == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLInsertFranchiseSatellite,
			m.ID, m.OwnerTenantID, m.SatelliteTenantID, m.CreatedByGCID, m.CreatedAt, m.UpdatedAt,
		); err != nil {
			if isFranchiseUniqueViolation(err) {
				return franchise.ErrDuplicateMapping
			}
			return fmt.Errorf("pg: insert franchise_satellite: %w", err)
		}
		return nil
	})
}

// Get resolves a live mapping by id (RLS already scopes to the caller's
// owner tenant; the deleted_at filter hides revoked rows).
func (r *FranchiseSatelliteRepo) Get(ctx context.Context, id string) (*franchise.FranchiseSatellite, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *franchise.FranchiseSatellite
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var m franchise.FranchiseSatellite
		if err := q.QueryRow(ctx, SQLGetFranchiseSatellite, id).Scan(
			&m.ID, &m.OwnerTenantID, &m.SatelliteTenantID, &m.CreatedByGCID, &m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			if isNoRows(err) {
				return nil // genuine miss: outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get franchise_satellite: %w", err)
		}
		out = &m
		return nil
	})
	if err != nil {
		return nil, false, err // LOUD: an infra/RLS failure is NOT a miss (CHO-2184)
	}
	if out == nil {
		return nil, false, nil
	}
	return out, true, nil
}

// ListByOwner returns the owner's live mappings, oldest first.
func (r *FranchiseSatelliteRepo) ListByOwner(ctx context.Context, ownerTenantID string) ([]*franchise.FranchiseSatellite, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*franchise.FranchiseSatellite
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListFranchiseSatellitesByOwner, ownerTenantID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var m franchise.FranchiseSatellite
			if err := rows.Scan(&m.ID, &m.OwnerTenantID, &m.SatelliteTenantID, &m.CreatedByGCID, &m.CreatedAt, &m.UpdatedAt); err != nil {
				return err
			}
			out = append(out, &m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Revoke soft-deletes a live mapping (explicit UPDATE, never a hard DELETE:
// the row is the audit trail of a grant that once existed). ok=false means no
// live row matched.
func (r *FranchiseSatelliteRepo) Revoke(ctx context.Context, id string, when time.Time) (bool, error) {
	if r == nil || r.tx == nil {
		return false, ErrNotImplemented
	}
	var revoked bool
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, SQLRevokeFranchiseSatellite, id, when.UTC())
		if err != nil {
			return fmt.Errorf("pg: revoke franchise_satellite: %w", err)
		}
		revoked = tag.RowsAffected > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return revoked, nil
}

// isFranchiseUniqueViolation matches Postgres SQLSTATE 23505 loosely (string
// match: the Querier abstraction hides the pg error type; same approach as
// chora-payments' isUniqueViolation).
func isFranchiseUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key value")
}
