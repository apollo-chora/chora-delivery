// survey.go — Postgres adapter for chora_delivery.surveys +
// chora_delivery.survey_responses (R+ durability sweep Wave 2, CHO-1580).
//
// SCHEMA: see migrations/0024_surveys.up.sql.
//
// Replaces the in-memory inmem.SurveyRepo. Before this adapter, surveys +
// their responses lived only in memory — rows were lost on pod restart. This
// is the durability close for the Survey + SurveyResponse aggregates (the
// siblings of the Exam pg-back, commit 48502e9d), per
// [[feedback-resilience-priority]].
//
// Storage = JSONB aggregate snapshot keyed by id (the exam.go pattern — both
// aggregates are all-exported-field records, so json.Marshal round-trips
// losslessly), with the columns RLS + listing need extracted:
//   - surveys:          tenant_id + state
//   - survey_responses: survey_id + gcid (for HasResponse + the per-survey
//     list) + tenant_id (for RLS — SurveyResponse carries
//     no tenant in the domain struct, so it is sourced from
//     the RLS session tenant on the ctx, which is the same
//     value SET LOCAL chora.tenant_id enforces).
//
// RLS is ENABLED on both tables (mirrors 0019_bookings / 0020_exams): surveys
// are admin CRUD, always tenant-scoped (every create/list/get/publish/close
// carries X-Tenant-Id), and response submit/list run under the same tenant —
// so rls.ApplySession enforces tenant isolation as defence-in-depth, unlike
// the tenant-less WS by-id resolve in live_poll/0017/0018 that forced
// handler-only isolation.
//
// Save / SaveResponse return an error (fail loud — a dropped write loses a
// survey or a learner's submission). Get returns its error — an infra/RLS
// failure is LOUD, never a silent miss. ok=false means a GENUINE absent row
// (CHO-2184: the two were once the same answer, and a dead read passed for an
// empty one), matching survey.SurveyStore.
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/survey"
)

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

const (
	SQLUpsertSurvey = `
INSERT INTO surveys (id, tenant_id, state, data, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
    state      = EXCLUDED.state,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at`

	SQLGetSurvey = `SELECT data FROM surveys WHERE id = $1 AND deleted_at IS NULL`

	SQLListSurveysByTenant = `
SELECT data FROM surveys
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC, id`

	SQLListSurveysByTenantState = `
SELECT data FROM surveys
WHERE tenant_id = $1 AND state = $2 AND deleted_at IS NULL
ORDER BY created_at DESC, id`

	// Responses are append-only — ON CONFLICT (id) DO NOTHING makes a
	// re-Save (idempotent replay) a no-op rather than a mutation.
	SQLInsertSurveyResponse = `
INSERT INTO survey_responses (id, survey_id, tenant_id, gcid, data, submitted_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO NOTHING`

	SQLHasSurveyResponse = `
SELECT EXISTS (
    SELECT 1 FROM survey_responses WHERE survey_id = $1 AND gcid = $2
)`

	SQLListResponsesBySurvey = `
SELECT data FROM survey_responses
WHERE survey_id = $1
ORDER BY submitted_at ASC, id`
)

// -----------------------------------------------------------------------------
// SurveyRepo
// -----------------------------------------------------------------------------

// SurveyRepo is the Postgres-backed survey.SurveyStore.
type SurveyRepo struct {
	tx TxRunner
}

// NewSurveyRepo constructs a SurveyRepo around a TxRunner. A nil TxRunner
// makes the write + list methods return ErrNotImplemented (fail loud); Get
// returns ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184).
func NewSurveyRepo(tx TxRunner) *SurveyRepo { return &SurveyRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ survey.SurveyStore = (*SurveyRepo)(nil)

func (r *SurveyRepo) Save(ctx context.Context, s *survey.Survey) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if s == nil {
		return nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("pg: marshal survey: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertSurvey, s.ID, s.TenantID, string(s.State), data, s.CreatedAt, s.UpdatedAt); err != nil {
			return fmt.Errorf("pg: upsert survey: %w", err)
		}
		return nil
	})
}

func (r *SurveyRepo) Get(ctx context.Context, id string) (*survey.Survey, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *survey.Survey
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetSurvey, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get survey: %w", err)
		}
		var s survey.Survey
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("pg: unmarshal survey: %w", err)
		}
		out = &s
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

func (r *SurveyRepo) ListByTenant(ctx context.Context, tenantID string, stateFilter survey.SurveyState) ([]*survey.Survey, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*survey.Survey
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var (
			rows Rows
			qErr error
		)
		if stateFilter == "" {
			rows, qErr = q.Query(ctx, SQLListSurveysByTenant, tenantID)
		} else {
			rows, qErr = q.Query(ctx, SQLListSurveysByTenantState, tenantID, string(stateFilter))
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
			var s survey.Survey
			if err := json.Unmarshal(data, &s); err != nil {
				return err
			}
			out = append(out, &s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *SurveyRepo) SaveResponse(ctx context.Context, resp *survey.SurveyResponse) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if resp == nil {
		return nil
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("pg: marshal survey response: %w", err)
	}
	// SurveyResponse carries no tenant in the domain struct; the row's
	// tenant_id MUST equal the RLS session tenant or the tenant_isolation
	// WITH CHECK rejects the INSERT. Source it from ctx (the handler
	// decorates ctx via tracing.WithTenantID before the call).
	tenantID := tracing.TenantIDFromContext(ctx)
	if tenantID == "" {
		return rls.ErrNoTenantContext
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLInsertSurveyResponse, resp.ID, resp.SurveyID, tenantID, resp.GCID, data, resp.SubmittedAt); err != nil {
			return fmt.Errorf("pg: insert survey response: %w", err)
		}
		return nil
	})
}

func (r *SurveyRepo) HasResponse(ctx context.Context, surveyID, gcid string) (bool, error) {
	if r == nil || r.tx == nil {
		return false, ErrNotImplemented
	}
	var exists bool
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		return q.QueryRow(ctx, SQLHasSurveyResponse, surveyID, gcid).Scan(&exists)
	})
	if err != nil {
		return false, err
	}
	return exists, nil
}

func (r *SurveyRepo) ListResponsesBySurvey(ctx context.Context, surveyID string) ([]*survey.SurveyResponse, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*survey.SurveyResponse
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListResponsesBySurvey, surveyID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var resp survey.SurveyResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				return err
			}
			out = append(out, &resp)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
