// project_group.go — Postgres adapter for chora_delivery.project_groups
// (R+ durability sweep).
//
// SCHEMA: see migrations/0022_project_groups.up.sql (`project_groups` table).
//
// Replaces the in-memory inmem.ProjectGroupRepo. Before this adapter, project
// groups lived only in memory — rows were lost on pod restart. This is the
// durability close for the ProjectGroup aggregate (the sibling of CHO-1580
// Exams), per [[feedback-resilience-priority]].
//
// Storage = JSONB aggregate snapshot keyed by id, with tenant_id + state +
// course_id extracted for RLS scoping + listing — the exam.go / live_poll.go
// pattern (the ProjectGroup aggregate is a multi-field FSM record with a
// nested members slice, so a flat-column mapping would be brittle; the JSONB
// snapshot round-trips losslessly since every ProjectGroup field is exported).
// course_id is extracted in addition to tenant_id/state so ListByCourse can
// filter on it (the inmem repo filtered course_id in Go; here it is a SQL
// predicate). RLS is ENABLED: project_groups is admin CRUD, always
// tenant-scoped, so rls.ApplySession enforces tenant isolation as
// defence-in-depth (the exams / bookings rationale).
//
// Save returns an error (fail loud — a dropped Save loses a group). Get +
// ListByTenant + ListByCourse return their errors (CHO-2184), matching
// project_group.ProjectGroupStore.
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	pgdomain "github.com/apollo-chora/chora-delivery/internal/domain/project_group"
)

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

const (
	SQLUpsertProjectGroup = `
INSERT INTO project_groups (id, tenant_id, course_id, state, data, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
    state      = EXCLUDED.state,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at`

	SQLGetProjectGroup = `SELECT data FROM project_groups WHERE id = $1 AND deleted_at IS NULL`

	SQLListProjectGroupsByTenant = `
SELECT data FROM project_groups
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY id`

	SQLListProjectGroupsByCourse = `
SELECT data FROM project_groups
WHERE tenant_id = $1 AND course_id = $2 AND deleted_at IS NULL
ORDER BY id`
)

// -----------------------------------------------------------------------------
// ProjectGroupRepo
// -----------------------------------------------------------------------------

// ProjectGroupRepo is the Postgres-backed project_group.ProjectGroupStore.
type ProjectGroupRepo struct {
	tx TxRunner
}

// NewProjectGroupRepo constructs a ProjectGroupRepo around a TxRunner. A nil
// TxRunner makes Save / ListByTenant / ListByCourse return ErrNotImplemented
// (fail loud); Get returns ErrNotImplemented too: an unwired repo is a WIRING
// BUG, not an empty database (CHO-2184).
func NewProjectGroupRepo(tx TxRunner) *ProjectGroupRepo { return &ProjectGroupRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ pgdomain.ProjectGroupStore = (*ProjectGroupRepo)(nil)

func (r *ProjectGroupRepo) Save(ctx context.Context, g *pgdomain.ProjectGroup) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if g == nil {
		return nil
	}
	data, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("pg: marshal project_group: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertProjectGroup, g.ID, g.TenantID, g.CourseID, string(g.State), data, g.CreatedAt, g.UpdatedAt); err != nil {
			return fmt.Errorf("pg: upsert project_group: %w", err)
		}
		return nil
	})
}

func (r *ProjectGroupRepo) Get(ctx context.Context, id string) (*pgdomain.ProjectGroup, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *pgdomain.ProjectGroup
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetProjectGroup, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get project group: %w", err)
		}
		var g pgdomain.ProjectGroup
		if err := json.Unmarshal(data, &g); err != nil {
			return fmt.Errorf("pg: unmarshal project_group: %w", err)
		}
		out = &g
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

func (r *ProjectGroupRepo) ListByTenant(ctx context.Context, tenantID string) ([]*pgdomain.ProjectGroup, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	return r.list(ctx, SQLListProjectGroupsByTenant, tenantID)
}

func (r *ProjectGroupRepo) ListByCourse(ctx context.Context, tenantID, courseID string) ([]*pgdomain.ProjectGroup, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	return r.list(ctx, SQLListProjectGroupsByCourse, tenantID, courseID)
}

// list runs an RLS-scoped JSONB-snapshot SELECT and rehydrates each row.
func (r *ProjectGroupRepo) list(ctx context.Context, sql string, args ...any) ([]*pgdomain.ProjectGroup, error) {
	var out []*pgdomain.ProjectGroup
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
			var g pgdomain.ProjectGroup
			if err := json.Unmarshal(data, &g); err != nil {
				return err
			}
			out = append(out, &g)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
