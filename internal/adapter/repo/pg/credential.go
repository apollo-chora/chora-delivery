// credential.go — Postgres adapter for chora_delivery.credentials (ADR-216 WS-1,
// operator-curated Credential-with-competencies catalogue).
//
// SCHEMA: see migrations/0038_credentials.up.sql.
//
// Storage = JSONB aggregate snapshot keyed by id (the offering_sessions /
// offerings pattern — every Credential field, incl. the Competencies child
// collection, is exported, so json.Marshal round-trips losslessly). Extracted
// columns:
//   - tenant_id : RLS scoping
//   - title     : catalogue ordering (ORDER BY title)
//
// RLS is ENABLED (mirrors offerings / offering_sessions): credentials are
// operator CRUD, always tenant-scoped. rls.ApplySession runs before every query.
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
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/domain/credential"
)

const (
	SQLUpsertCredential = `
INSERT INTO credentials (id, tenant_id, title, data, created_at, updated_at, deleted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
    title      = EXCLUDED.title,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at,
    deleted_at = EXCLUDED.deleted_at`

	SQLGetCredential = `SELECT data FROM credentials WHERE id = $1 AND deleted_at IS NULL`

	SQLListCredentialsByTenant = `
SELECT data FROM credentials
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY title, id`
)

// CredentialRepo is the Postgres-backed credential.Store.
type CredentialRepo struct {
	tx TxRunner
}

// NewCredentialRepo constructs a repo around a TxRunner. A nil TxRunner makes
// the write + list methods return ErrNotImplemented (fail loud); Get returns
// ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184).
func NewCredentialRepo(tx TxRunner) *CredentialRepo { return &CredentialRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ credential.Store = (*CredentialRepo)(nil)

func (r *CredentialRepo) Save(ctx context.Context, c *credential.Credential) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if c == nil {
		return nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("pg: marshal credential: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertCredential,
			c.ID, c.TenantID, c.Title, data, c.CreatedAt, c.UpdatedAt, nullTimePtr(c.DeletedAt),
		); err != nil {
			return fmt.Errorf("pg: upsert credential: %w", err)
		}
		return nil
	})
}

func (r *CredentialRepo) Get(ctx context.Context, id string) (*credential.Credential, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *credential.Credential
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetCredential, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get credential: %w", err)
		}
		var c credential.Credential
		if err := json.Unmarshal(data, &c); err != nil {
			return fmt.Errorf("pg: unmarshal credential: %w", err)
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

func (r *CredentialRepo) ListByTenant(ctx context.Context, tenantID string) ([]*credential.Credential, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*credential.Credential
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListCredentialsByTenant, tenantID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var c credential.Credential
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
