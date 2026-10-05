// wbl.go — Postgres adapter for chora_delivery.wbl_placements (R+ durability
// sweep).
//
// SCHEMA: see migrations/0021_wbl.up.sql (`wbl_placements` table).
//
// Replaces the in-memory inmem.WblRepo. Before this adapter, WBL placements
// lived only in memory — rows were lost on pod restart. This is the durability
// close for the Wbl Placement aggregate (the sibling of 0020 Exams), per
// [[feedback-resilience-priority]].
//
// Storage = JSONB aggregate snapshot keyed by id, with tenant_id + state
// extracted for RLS scoping + listing — the exam.go / live_poll.go pattern (the
// Placement aggregate is a multi-field FSM record, so a flat-column mapping
// would be brittle; the JSONB snapshot round-trips losslessly since every
// Placement field is exported). RLS is ENABLED (exams rationale): wbl_placements
// is admin CRUD, always tenant-scoped, so rls.ApplySession enforces tenant
// isolation as defence-in-depth.
//
// ListByTenant mirrors inmem.WblRepo state semantics EXACTLY:
//   - empty state  → all tenant rows EXCEPT WITHDRAWN (the default scope; WBL
//     soft-delete IS the WITHDRAWN transition — there is no deleted_at flag).
//   - explicit state → exactly that state (including WITHDRAWN when asked).
//
// (Note: this is stricter than a naive "empty == all rows" — the WITHDRAWN
// exclusion is the domain's soft-delete default and must match the inmem repo.)
//
// Save returns an error (fail loud — a dropped Save loses a placement). Get
// returns its error — an infra/RLS failure is LOUD, never a silent miss.
// ok=false means a GENUINE absent row (CHO-2184: the two were once the same
// answer, and a dead read passed for an empty one), matching wbl.WblStore.
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	wbl "github.com/apollo-chora/chora-delivery/internal/domain/wbl"
)

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

const (
	SQLUpsertWblPlacement = `
INSERT INTO wbl_placements (id, tenant_id, state, data, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
    state      = EXCLUDED.state,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at`

	SQLGetWblPlacement = `SELECT data FROM wbl_placements WHERE id = $1 AND deleted_at IS NULL`

	// Default scope: every tenant row EXCEPT WITHDRAWN (soft-delete state).
	SQLListWblPlacementsByTenant = `
SELECT data FROM wbl_placements
WHERE tenant_id = $1 AND state <> 'WITHDRAWN' AND deleted_at IS NULL
ORDER BY id`

	// Explicit-state scope: exactly the requested state.
	SQLListWblPlacementsByTenantState = `
SELECT data FROM wbl_placements
WHERE tenant_id = $1 AND state = $2 AND deleted_at IS NULL
ORDER BY id`
)

// -----------------------------------------------------------------------------
// WblRepo
// -----------------------------------------------------------------------------

// WblRepo is the Postgres-backed wbl.WblStore.
type WblRepo struct {
	tx TxRunner
}

// NewWblRepo constructs a WblRepo around a TxRunner. A nil TxRunner makes Save
// / ListByTenant return ErrNotImplemented (fail loud); Get returns
// ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184).
func NewWblRepo(tx TxRunner) *WblRepo { return &WblRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ wbl.WblStore = (*WblRepo)(nil)

func (r *WblRepo) Save(ctx context.Context, p *wbl.Placement) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if p == nil {
		return nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("pg: marshal wbl placement: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertWblPlacement, p.ID, p.TenantID, string(p.State), data, p.CreatedAt, p.UpdatedAt); err != nil {
			return fmt.Errorf("pg: upsert wbl placement: %w", err)
		}
		return nil
	})
}

func (r *WblRepo) Get(ctx context.Context, id string) (*wbl.Placement, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *wbl.Placement
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetWblPlacement, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get placement: %w", err)
		}
		var p wbl.Placement
		if err := json.Unmarshal(data, &p); err != nil {
			return fmt.Errorf("pg: unmarshal wbl placement: %w", err)
		}
		out = &p
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

func (r *WblRepo) ListByTenant(ctx context.Context, tenantID string, state wbl.PlacementState) ([]*wbl.Placement, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*wbl.Placement
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var (
			rows Rows
			qErr error
		)
		if state == "" {
			// Default scope: every tenant row EXCEPT WITHDRAWN — mirrors
			// inmem.WblRepo (soft-delete IS the WITHDRAWN transition).
			rows, qErr = q.Query(ctx, SQLListWblPlacementsByTenant, tenantID)
		} else {
			rows, qErr = q.Query(ctx, SQLListWblPlacementsByTenantState, tenantID, string(state))
		}
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var p wbl.Placement
			if err := json.Unmarshal(data, &p); err != nil {
				return err
			}
			out = append(out, &p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
