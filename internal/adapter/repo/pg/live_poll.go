// live_poll.go — Postgres adapter for the ADR-168 LivePoll aggregate
// (gap-1b: true multi-pod, the sibling of live_classroom.go's LiveQuiz +
// LiveQuizSession repos). The aggregate is persisted as a JSONB snapshot keyed
// by id, with tenant_id/state extracted for listing. A poll resolves on ANY
// pod, so a learner's vote / WS no longer 404s when it lands on a non-owning
// pod (the Redis backplane + tally were already cross-pod-correct).
//
// LOSSLESS SNAPSHOT — the crux for LivePoll: unlike LiveQuiz/LiveQuizSession
// (all fields exported), LivePoll carries an UNEXPORTED `voters` set that drives
// first-vote-wins idempotency. A default json.Marshal would DROP it (the field
// is invisible to encoding/json), so a rehydrated poll on another pod would let
// an already-voted learner vote AGAIN. THIS is why LivePoll was deferred from
// gap-1. The fix lives ON the aggregate: LivePoll.MarshalJSON/UnmarshalJSON emit
// + rehydrate `voters` as a JSON array (see domain/classroom/live_poll.go), so
// this adapter just calls json.Marshal/Unmarshal exactly like the quiz/session
// repos — NO voters-specific code leaks here, and `voters` stays unexported.
// We deliberately do NOT add a separate `voters` child table: polls resolve by
// id (voters need not be independently queryable) and a child table would
// diverge from the established Session/Quiz JSONB-snapshot pattern + add
// normalization debt.
//
// NO RLS (see migrations/0018_live_poll.up.sql): the WS handler resolves a poll
// by id alone (tenant-less), so RLS-by-tenant-GUC would break it. Tenant
// isolation stays handler-enforced. This adapter therefore does NOT call
// rls.ApplySession — identical rationale to live_classroom.go / 0017.
//
// Save returns an error (fail loud — a dropped Save loses a live poll's
// votes). Get returns its error — an infra/RLS failure is LOUD, never a silent
// miss. ok=false means a GENUINE absent row (CHO-2184: the two were once the
// same answer, and a dead read passed for an empty one), matching the
// classroom.PollStore contract. Context: an internal Background+timeout (the
// store ports are ctx-less to stay a drop-in for the handler call sites).
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// -----------------------------------------------------------------------------
// SQL (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

const (
	SQLUpsertLivePoll = `
INSERT INTO live_polls (id, tenant_id, state, data, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
    state      = EXCLUDED.state,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at`

	SQLGetLivePoll = `SELECT data FROM live_polls WHERE id = $1 AND deleted_at IS NULL`

	SQLListLivePollsByTenant = `
SELECT data FROM live_polls
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY id`
)

// -----------------------------------------------------------------------------
// LivePollRepo
// -----------------------------------------------------------------------------

// LivePollRepo is the Postgres-backed classroom.PollStore.
type LivePollRepo struct {
	tx TxRunner
}

// NewLivePollRepo constructs a pg LivePollRepo around a TxRunner. A nil
// TxRunner makes Save return ErrNotImplemented + reads return empty.
func NewLivePollRepo(tx TxRunner) *LivePollRepo { return &LivePollRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ classroom.PollStore = (*LivePollRepo)(nil)

func (r *LivePollRepo) Save(p *classroom.LivePoll) error {
	if r.tx == nil {
		return ErrNotImplemented
	}
	if p == nil {
		return nil
	}
	// LivePoll.MarshalJSON serializes the unexported voter set (lossless).
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("pg: marshal live_poll: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveClassroomTimeout)
	defer cancel()
	return r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		if _, err := qx.Exec(ctx, SQLUpsertLivePoll, p.ID, p.TenantID, string(p.State), data, p.CreatedAt, p.UpdatedAt); err != nil {
			return fmt.Errorf("pg: upsert live_poll: %w", err)
		}
		return nil
	})
}

func (r *LivePollRepo) Get(id string) (*classroom.LivePoll, bool, error) {
	if r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveClassroomTimeout)
	defer cancel()
	var out *classroom.LivePoll
	err := r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		var data []byte
		if err := qx.QueryRow(ctx, SQLGetLivePoll, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get live_poll: %w", err)
		}
		var p classroom.LivePoll
		// LivePoll.UnmarshalJSON rehydrates the unexported voter set.
		if err := json.Unmarshal(data, &p); err != nil {
			return fmt.Errorf("pg: unmarshal live_poll: %w", err)
		}
		out = &p
		return nil
	})
	if err != nil {
		return nil, false, err // LOUD: a dead store is NOT an absent poll (CHO-2184)
	}
	if out == nil {
		return nil, false, nil // genuine miss
	}
	return out, true, nil
}

func (r *LivePollRepo) ListByTenant(tenantID string) []*classroom.LivePoll {
	if r.tx == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveClassroomTimeout)
	defer cancel()
	var out []*classroom.LivePoll
	_ = r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		rows, err := qx.Query(ctx, SQLListLivePollsByTenant, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var p classroom.LivePoll
			if err := json.Unmarshal(data, &p); err != nil {
				return err
			}
			out = append(out, &p)
		}
		return rows.Err()
	})
	return out
}
