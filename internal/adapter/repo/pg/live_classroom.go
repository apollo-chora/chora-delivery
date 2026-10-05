// live_classroom.go — Postgres adapters for the ADR-168 LiveQuiz +
// LiveQuizSession aggregates (gap 1: true multi-pod). The aggregates are
// persisted as a JSONB snapshot (lossless — all fields exported) keyed by id,
// with tenant_id/state extracted for listing. A session resolves on ANY pod,
// so a learner's WebSocket no longer 404s when it lands on a non-owning pod
// (the Redis backplane was already cross-pod-correct).
//
// NO RLS (see migrations/0017_live_classroom.up.sql): the WS handler resolves a
// session by id alone (tenant-less), so RLS-by-tenant-GUC would break it.
// Tenant isolation stays handler-enforced. These adapters therefore do NOT call
// rls.ApplySession.
//
// Save returns an error (fail loud — a dropped Save loses a live session). Get
// returns its error — an infra/RLS failure is LOUD, never a silent miss.
// ok=false means a GENUINE absent row (CHO-2184: the two were once the same
// answer, and a dead read passed for an empty one), matching the
// classroom.SessionStore / QuizStore contract. Context: an internal
// Background+timeout (the store ports are ctx-less to stay a drop-in for the
// handler call sites).
package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// liveClassroomTimeout bounds each query when the store port carries no ctx.
const liveClassroomTimeout = 10 * time.Second

// -----------------------------------------------------------------------------
// SQL (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

const (
	SQLUpsertLiveQuiz = `
INSERT INTO live_quizzes (id, tenant_id, state, data, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
    state      = EXCLUDED.state,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at`

	SQLGetLiveQuiz = `SELECT data FROM live_quizzes WHERE id = $1 AND deleted_at IS NULL`

	SQLListLiveQuizzesByTenant = `
SELECT data FROM live_quizzes
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY id`

	SQLUpsertLiveQuizSession = `
INSERT INTO live_quiz_sessions (id, tenant_id, live_quiz_id, state, data, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
    state      = EXCLUDED.state,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at`

	SQLGetLiveQuizSession = `SELECT data FROM live_quiz_sessions WHERE id = $1 AND deleted_at IS NULL`

	SQLListLiveQuizSessionsByTenant = `
SELECT data FROM live_quiz_sessions
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY id`

	// L5.2 (ADR-179 ruling 6) — join-code resolve, ACTIVE sessions only.
	// Backed by the 0027 expression index on (tenant_id, data->>'join_code').
	SQLGetLiveQuizSessionByJoinCode = `
SELECT data FROM live_quiz_sessions
WHERE tenant_id = $1 AND data->>'join_code' = $2
  AND state IN ('ARMED','LIVE') AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 1`

	// L5.2 hardening — Mutate's row lock. Concurrent learner submits would
	// otherwise read-modify-write the same JSONB snapshot and drop responses.
	SQLGetLiveQuizSessionForUpdate = `
SELECT data FROM live_quiz_sessions WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`
)

// -----------------------------------------------------------------------------
// LiveQuizRepo
// -----------------------------------------------------------------------------

// LiveQuizRepo is the Postgres-backed classroom.QuizStore.
type LiveQuizRepo struct {
	tx TxRunner
}

// NewLiveQuizRepo constructs a pg LiveQuizRepo around a TxRunner. A nil
// TxRunner makes Save return ErrNotImplemented + reads return empty.
func NewLiveQuizRepo(tx TxRunner) *LiveQuizRepo { return &LiveQuizRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ classroom.QuizStore = (*LiveQuizRepo)(nil)

func (r *LiveQuizRepo) Save(q *classroom.LiveQuiz) error {
	if r.tx == nil {
		return ErrNotImplemented
	}
	if q == nil {
		return nil
	}
	data, err := json.Marshal(q)
	if err != nil {
		return fmt.Errorf("pg: marshal live_quiz: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveClassroomTimeout)
	defer cancel()
	return r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		if _, err := qx.Exec(ctx, SQLUpsertLiveQuiz, q.ID, q.TenantID, string(q.State), data, q.CreatedAt, q.UpdatedAt); err != nil {
			return fmt.Errorf("pg: upsert live_quiz: %w", err)
		}
		return nil
	})
}

func (r *LiveQuizRepo) Get(id string) (*classroom.LiveQuiz, bool, error) {
	if r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveClassroomTimeout)
	defer cancel()
	var out *classroom.LiveQuiz
	err := r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		var data []byte
		if err := qx.QueryRow(ctx, SQLGetLiveQuiz, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get live_quiz: %w", err)
		}
		var q classroom.LiveQuiz
		if err := json.Unmarshal(data, &q); err != nil {
			return fmt.Errorf("pg: unmarshal live_quiz: %w", err)
		}
		out = &q
		return nil
	})
	if err != nil {
		return nil, false, err // LOUD: a dead store is NOT an absent quiz (CHO-2184)
	}
	if out == nil {
		return nil, false, nil // genuine miss
	}
	return out, true, nil
}

func (r *LiveQuizRepo) ListByTenant(tenantID string) []*classroom.LiveQuiz {
	if r.tx == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveClassroomTimeout)
	defer cancel()
	var out []*classroom.LiveQuiz
	_ = r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		rows, err := qx.Query(ctx, SQLListLiveQuizzesByTenant, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var q classroom.LiveQuiz
			if err := json.Unmarshal(data, &q); err != nil {
				return err
			}
			out = append(out, &q)
		}
		return rows.Err()
	})
	return out
}

// -----------------------------------------------------------------------------
// ClassroomSessionRepo
// -----------------------------------------------------------------------------

// ClassroomSessionRepo is the Postgres-backed classroom.SessionStore.
type ClassroomSessionRepo struct {
	tx TxRunner
}

// NewClassroomSessionRepo constructs a pg ClassroomSessionRepo around a TxRunner.
func NewClassroomSessionRepo(tx TxRunner) *ClassroomSessionRepo {
	return &ClassroomSessionRepo{tx: tx}
}

// Compile-time assertion: satisfies the domain port.
var _ classroom.SessionStore = (*ClassroomSessionRepo)(nil)

func (r *ClassroomSessionRepo) Save(s *classroom.LiveQuizSession) error {
	if r.tx == nil {
		return ErrNotImplemented
	}
	if s == nil {
		return nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("pg: marshal live_quiz_session: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveClassroomTimeout)
	defer cancel()
	return r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		if _, err := qx.Exec(ctx, SQLUpsertLiveQuizSession, s.ID, s.TenantID, s.LiveQuizID, string(s.State), data, s.CreatedAt, s.UpdatedAt); err != nil {
			return fmt.Errorf("pg: upsert live_quiz_session: %w", err)
		}
		return nil
	})
}

func (r *ClassroomSessionRepo) Get(id string) (*classroom.LiveQuizSession, bool, error) {
	if r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveClassroomTimeout)
	defer cancel()
	var out *classroom.LiveQuizSession
	err := r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		var data []byte
		if err := qx.QueryRow(ctx, SQLGetLiveQuizSession, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get live_quiz_session: %w", err)
		}
		var s classroom.LiveQuizSession
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("pg: unmarshal live_quiz_session: %w", err)
		}
		out = &s
		return nil
	})
	if err != nil {
		return nil, false, err // LOUD: a dead store is NOT an absent session (CHO-2184)
	}
	if out == nil {
		return nil, false, nil // genuine miss
	}
	return out, true, nil
}

// GetByJoinCode resolves a tenant's ACTIVE session by (already-normalised)
// join code. ok=false on miss or read error (handler 404s; client retries).
func (r *ClassroomSessionRepo) GetByJoinCode(tenantID, code string) (*classroom.LiveQuizSession, bool, error) {
	// A nil TxRunner is a WIRING bug (fail loud); an empty join code is simply
	// nothing to look up — a genuine miss. Do not conflate the two.
	if r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	if code == "" {
		return nil, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveClassroomTimeout)
	defer cancel()
	var out *classroom.LiveQuizSession
	err := r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		var data []byte
		if err := qx.QueryRow(ctx, SQLGetLiveQuizSessionByJoinCode, tenantID, code).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get live_quiz_session by join code: %w", err)
		}
		var s classroom.LiveQuizSession
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("pg: unmarshal live_quiz_session: %w", err)
		}
		out = &s
		return nil
	})
	if err != nil {
		return nil, false, err // LOUD: a dead store is NOT an absent session (CHO-2184)
	}
	if out == nil {
		return nil, false, nil // genuine miss
	}
	return out, true, nil
}

// Mutate loads the session under SELECT … FOR UPDATE, applies fn, and upserts
// the new snapshot inside the SAME transaction — concurrent mutators
// serialise on the row lock instead of clobbering each other's JSONB
// snapshot. fn errors abort the tx (no write) and propagate verbatim; an
// unknown id returns classroom.ErrSessionNotFound.
func (r *ClassroomSessionRepo) Mutate(id string, fn func(*classroom.LiveQuizSession) error) error {
	if r.tx == nil {
		return ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveClassroomTimeout)
	defer cancel()
	return r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		var data []byte
		if err := qx.QueryRow(ctx, SQLGetLiveQuizSessionForUpdate, id).Scan(&data); err != nil {
			return classroom.ErrSessionNotFound
		}
		var s classroom.LiveQuizSession
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("pg: unmarshal live_quiz_session: %w", err)
		}
		if err := fn(&s); err != nil {
			return err
		}
		next, err := json.Marshal(&s)
		if err != nil {
			return fmt.Errorf("pg: marshal live_quiz_session: %w", err)
		}
		if _, err := qx.Exec(ctx, SQLUpsertLiveQuizSession, s.ID, s.TenantID, s.LiveQuizID, string(s.State), next, s.CreatedAt, s.UpdatedAt); err != nil {
			return fmt.Errorf("pg: upsert live_quiz_session: %w", err)
		}
		return nil
	})
}

func (r *ClassroomSessionRepo) ListByTenant(tenantID string) []*classroom.LiveQuizSession {
	if r.tx == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveClassroomTimeout)
	defer cancel()
	var out []*classroom.LiveQuizSession
	_ = r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		rows, err := qx.Query(ctx, SQLListLiveQuizSessionsByTenant, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var s classroom.LiveQuizSession
			if err := json.Unmarshal(data, &s); err != nil {
				return err
			}
			out = append(out, &s)
		}
		return rows.Err()
	})
	return out
}
