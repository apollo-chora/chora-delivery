// skillsfutures.go — Postgres adapter for chora_delivery.skillsfutures_claims
// (R+ durability sweep).
//
// SCHEMA: see migrations/0023_skillsfutures_claims.up.sql
// (`skillsfutures_claims` table).
//
// Replaces the in-memory inmem.SkillsFuturesRepo. Before this adapter,
// SkillsFutures claims lived only in memory — rows were lost on pod restart.
// Unacceptable for a SSG (SkillsFutures Singapore) government funding queue
// whose claim rows are retained for audit. This is the durability close for
// the SkillsFuturesClaim aggregate (the sibling of the Exam / Booking sweep),
// per [[feedback-resilience-priority]].
//
// Storage = JSONB aggregate snapshot keyed by id, with tenant_id + state
// extracted for RLS scoping + listing — the exam.go / live_poll.go pattern
// (the SkillsFuturesClaim aggregate is a multi-field FSM record, so a
// flat-column mapping would be brittle; the JSONB snapshot round-trips
// losslessly since every claim field is exported). RLS is ENABLED: claims are
// admin-reviewed, always tenant-scoped, so rls.ApplySession enforces tenant
// isolation as defence-in-depth (the bookings / exams rationale).
//
// Save returns an error (fail loud — a dropped Save loses a funding claim).
// Get returns its error — an infra/RLS failure is LOUD, never a silent miss.
// ok=false means a GENUINE absent row (CHO-2184: the two were once the same
// answer, and a dead read passed for an empty one), matching
// skillsfutures.SkillsFuturesStore.
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/skillsfutures"
)

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

const (
	SQLUpsertSkillsFuturesClaim = `
INSERT INTO skillsfutures_claims (id, tenant_id, state, data, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
    state      = EXCLUDED.state,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at`

	SQLGetSkillsFuturesClaim = `SELECT data FROM skillsfutures_claims WHERE id = $1 AND deleted_at IS NULL`

	SQLListSkillsFuturesClaimsByTenant = `
SELECT data FROM skillsfutures_claims
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY id`

	SQLListSkillsFuturesClaimsByTenantState = `
SELECT data FROM skillsfutures_claims
WHERE tenant_id = $1 AND state = $2 AND deleted_at IS NULL
ORDER BY id`
)

// -----------------------------------------------------------------------------
// SkillsFuturesRepo
// -----------------------------------------------------------------------------

// SkillsFuturesRepo is the Postgres-backed skillsfutures.SkillsFuturesStore.
type SkillsFuturesRepo struct {
	tx TxRunner
}

// NewSkillsFuturesRepo constructs a SkillsFuturesRepo around a TxRunner. A nil
// TxRunner makes Save / ListByTenant return ErrNotImplemented (fail loud); Get
// returns ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184).
func NewSkillsFuturesRepo(tx TxRunner) *SkillsFuturesRepo { return &SkillsFuturesRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ skillsfutures.SkillsFuturesStore = (*SkillsFuturesRepo)(nil)

func (r *SkillsFuturesRepo) Save(ctx context.Context, c *skillsfutures.SkillsFuturesClaim) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if c == nil {
		return nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("pg: marshal skillsfutures claim: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertSkillsFuturesClaim, c.ID, c.TenantID, string(c.State), data, c.SubmittedAt, c.SubmittedAt); err != nil {
			return fmt.Errorf("pg: upsert skillsfutures claim: %w", err)
		}
		return nil
	})
}

func (r *SkillsFuturesRepo) Get(ctx context.Context, id string) (*skillsfutures.SkillsFuturesClaim, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *skillsfutures.SkillsFuturesClaim
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetSkillsFuturesClaim, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get skillsfutures claim: %w", err)
		}
		var c skillsfutures.SkillsFuturesClaim
		if err := json.Unmarshal(data, &c); err != nil {
			return fmt.Errorf("pg: unmarshal skillsfutures claim: %w", err)
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

func (r *SkillsFuturesRepo) ListByTenant(
	ctx context.Context,
	tenantID string,
	stateFilter skillsfutures.ClaimState,
) ([]*skillsfutures.SkillsFuturesClaim, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*skillsfutures.SkillsFuturesClaim
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var (
			rows Rows
			qErr error
		)
		if stateFilter == "" {
			rows, qErr = q.Query(ctx, SQLListSkillsFuturesClaimsByTenant, tenantID)
		} else {
			rows, qErr = q.Query(ctx, SQLListSkillsFuturesClaimsByTenantState, tenantID, string(stateFilter))
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
			var c skillsfutures.SkillsFuturesClaim
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
