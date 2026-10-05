// student_module_progress.go — pg adapter for the StudentModuleProgress
// projection (WS-A W7, CHO-2074) per migration 0045_student_module_progress.
//
// Advance is the load-mutate-persist write path the projection subscribers call.
// It runs the whole unit under ONE tx: an ENSURE insert (ON CONFLICT DO NOTHING)
// guarantees the (tenant,gcid,module) row exists, then SELECT ... FOR UPDATE
// locks it so two concurrent completions of DIFFERENT items in the same module
// by the same learner serialize (no lost update — the aggregate mutation runs on
// the locked row). The domain aggregate's RecordItemCompletion runs inside the
// tx, so every invariant executes on the production path (no green-test stub).
//
// The false→true IsComplete transition additionally enqueues ONE
// chora.delivery.module_progress.completed.v1 outbox row through the SAME tx
// (CHO-2124) — atomic state-write + event-publish, mirroring chora-sharing's
// relationship_repo same-tx spine (ADR-230 D4 idiom). The existing outbox
// Dispatcher drains the row to Pub/Sub regardless of which writer inserted it.
// Replays, further completions while already complete, and true→false
// recomputes never enqueue (the edge is the ONLY publish trigger).
//
// Every read/write wraps rls.ApplySession first so the tenant_isolation policy on
// student_module_progress filters by chora.tenant_id. Cross-DB queries FORBIDDEN
// — this only touches chora_delivery.student_module_progress + outbox_events;
// module_id/course_id are bare cross-aggregate UUIDs (never an FK or cross-DB
// link).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	cgcenvelope "github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// ErrProgressMissingScope is returned when a required RLS-scope field is empty.
var ErrProgressMissingScope = errors.New("pg: student_module_progress requires non-empty tenant_id/gcid/module_id (RLS scope)")

// TopicModuleProgressCompleted is the canonical topic announcing a learner's
// per-module completion (CHO-2124) — consumed by chora-consumption's Wave-1 XP
// leg (F-I3, CHO-2090). JSON-schemaless BY DESIGN: protomarshal has no binary
// encoder for this topic and the topic is provisioned WITHOUT a Schema
// Registry schema, so the payload stays plain JSON on the wire (see the
// consumption-side contract in module_completed_exp_push_handler.go). Do NOT
// add a binary encoder without flipping the topic schema + the consumption
// decode registry in the same change.
const TopicModuleProgressCompleted = "chora.delivery.module_progress.completed.v1"

const progressSelectCols = `id, tenant_id, gcid, module_id, course_id, ` +
	`completed_content_item_ids, is_complete, completed_at, ` +
	`created_at, updated_at, deleted_at`

// SQLEnsureProgressRow inserts a fresh empty projection for (tenant,gcid,module)
// or does nothing if one already exists (the partial UNIQUE index). $6 seeds
// created_at + updated_at. This makes the subsequent SELECT ... FOR UPDATE always
// find a row to lock — closing the first-completion insert race.
const SQLEnsureProgressRow = `
INSERT INTO student_module_progress (
    id, tenant_id, gcid, module_id, course_id,
    completed_content_item_ids, is_complete, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, '[]'::jsonb, false, $6, $6)
ON CONFLICT (tenant_id, gcid, module_id) WHERE deleted_at IS NULL DO NOTHING
`

// SQLSelectProgressForUpdate row-locks the active projection for a learner+module.
const SQLSelectProgressForUpdate = `
SELECT ` + progressSelectCols + `
FROM student_module_progress
WHERE tenant_id = $1 AND gcid = $2 AND module_id = $3 AND deleted_at IS NULL
FOR UPDATE
`

// SQLSelectProgressByLearnerModule loads one active projection (no lock) — the
// GetByLearnerModule read path.
const SQLSelectProgressByLearnerModule = `
SELECT ` + progressSelectCols + `
FROM student_module_progress
WHERE tenant_id = $1 AND gcid = $2 AND module_id = $3 AND deleted_at IS NULL
`

// SQLUpdateProgress writes the recomputed completion state onto the locked row.
const SQLUpdateProgress = `
UPDATE student_module_progress
SET completed_content_item_ids = $1::jsonb, is_complete = $2,
    completed_at = $3, updated_at = $4
WHERE id = $5 AND deleted_at IS NULL
`

// SQLInsertModuleProgressCompletedOutbox enqueues the completion announcement
// through the projection's own transaction. Column list mirrors
// outbox.PostgresStore.Insert (migrations 0003_outbox + 0004_outbox_d6) so the
// existing Dispatcher drains the row identically to tee'd rows.
const SQLInsertModuleProgressCompletedOutbox = `
INSERT INTO outbox_events (
    id, tenant_id, gcid, aggregate_type, aggregate_id, event_type, topic,
    payload, envelope, idempotency_key, occurred_at, status
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, 'pending'
)`

// SQLSelectProgressByModuleIDs returns all active projections for the given
// modules (tenant-scoped) — the read handler's cohort/learner source.
const SQLSelectProgressByModuleIDs = `
SELECT ` + progressSelectCols + `
FROM student_module_progress
WHERE tenant_id = $1 AND module_id = ANY($2::uuid[]) AND deleted_at IS NULL
ORDER BY module_id ASC, gcid ASC
`

// ProgressRepoOptions tunes the completion-event envelope stamps at
// construction time (mirrors chora-sharing's RelationshipRepoOptions — the
// same-tx outbox precedent).
type ProgressRepoOptions struct {
	// SourceProject / SourceService stamp the event envelopes. Defaults:
	// chora-489812 / chora-delivery (same defaults as outbox.PublisherConfig).
	SourceProject string
	SourceService string

	// Now is injectable for tests; defaults to time.Now().UTC.
	Now func() time.Time
}

// ProgressRepo is the Postgres-backed moduleprogress.ProgressPort impl.
type ProgressRepo struct {
	tx   TxRunner
	opts ProgressRepoOptions
}

// NewProgressRepo constructs a ProgressRepo around a TxRunner. A nil TxRunner
// degrades every method to ErrNotImplemented (fail-loud, matches sibling repos).
func NewProgressRepo(tx TxRunner, opts ProgressRepoOptions) *ProgressRepo {
	if opts.SourceProject == "" {
		opts.SourceProject = "chora-489812"
	}
	if opts.SourceService == "" {
		opts.SourceService = "chora-delivery"
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &ProgressRepo{tx: tx, opts: opts}
}

// Compile-time assertion.
var _ moduleprogress.ProgressPort = (*ProgressRepo)(nil)

// Advance atomically load-or-creates + records + persists the learner's module
// progress under a row lock. Returns changed=true iff state changed.
func (r *ProgressRepo) Advance(ctx context.Context, tenantID, gcid, moduleID, courseID, contentItemID string, m *module.Module) (bool, error) {
	if r == nil || r.tx == nil {
		return false, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(gcid) == "" || strings.TrimSpace(moduleID) == "" {
		return false, ErrProgressMissingScope
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	changed := false
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		// 1. Ensure the row exists so the lock has something to hold.
		seed, err := moduleprogress.New(moduleprogress.NewParams{
			TenantID: tenantID, GCID: gcid, ModuleID: moduleID, CourseID: courseID,
		})
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLEnsureProgressRow,
			seed.ID, tenantID, gcid, moduleID, courseID, seed.CreatedAt.UTC()); err != nil {
			return fmt.Errorf("pg: ensure progress row: %w", err)
		}
		// 2. Lock + load the active row (always present after the ensure).
		prog, err := scanProgress(q.QueryRow(ctx, SQLSelectProgressForUpdate, tenantID, gcid, moduleID).Scan)
		if err != nil {
			return fmt.Errorf("pg: lock progress row: %w", err)
		}
		// 3. Apply the domain mutation on the locked aggregate. wasComplete
		// captures the pre-mutation flag so the false→true completion edge —
		// and ONLY that edge — triggers the event enqueue below.
		wasComplete := prog.IsComplete
		didChange, err := prog.RecordItemCompletion(contentItemID, m)
		if err != nil {
			return err
		}
		if !didChange {
			return nil
		}
		// 4. Persist the recomputed completion state.
		idsJSON, err := marshalRequiredItemIDs(prog.CompletedContentItemIDs)
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpdateProgress,
			idsJSON, prog.IsComplete, prog.CompletedAt, prog.UpdatedAt.UTC(), prog.ID); err != nil {
			return fmt.Errorf("pg: update progress: %w", err)
		}
		// 5. Announce the completion transition through the SAME tx (CHO-2124).
		// An enqueue failure aborts the whole tx — the projection flip and its
		// event land together or not at all (fail-loud; Pub/Sub redelivers).
		if !wasComplete && prog.IsComplete {
			if err := r.enqueueCompletedEvent(ctx, q, prog); err != nil {
				return err
			}
		}
		changed = true
		return nil
	})
	return changed, err
}

// enqueueCompletedEvent INSERTs the module_progress.completed.v1 outbox row
// through the tx Querier the projection UPDATE just ran on. Envelope minting
// rides cgcenvelope.Build (UUIDv7 event_id, ctx traceparent or a fresh mint per
// the OTLP-everywhere mandate) with the identity + instant pinned from the
// aggregate; the persisted attribute map mirrors the outbox tee lane
// (TransactionalOutboxPublisher.teeRow) so the Dispatcher reconstructs an
// identical envelope. The payload is plain JSON per the consumption contract —
// see the TopicModuleProgressCompleted doc for why there is deliberately no
// binary encoder.
func (r *ProgressRepo) enqueueCompletedEvent(ctx context.Context, q Querier, prog *moduleprogress.StudentModuleProgress) error {
	if prog.CompletedAt == nil {
		// The aggregate stamps CompletedAt on the exact transition this is
		// called for — a nil here is a domain-invariant breach, not a skip.
		return errors.New("pg: module progress completed transition missing completed_at")
	}
	occurred := prog.CompletedAt.UTC()
	env := cgcenvelope.Build(ctx, cgcenvelope.BuildOpts{
		SchemaVersion: 1,
		SourceProject: r.opts.SourceProject,
		SourceService: r.opts.SourceService,
		// Deterministic per completion INSTANCE: a re-completion after a
		// true→false recompute (module grew, learner caught up) is a distinct
		// legitimate event, so the instant disambiguates while replays of the
		// same completion never reach this insert (the aggregate no-ops them).
		IdempotencyKey: fmt.Sprintf("%s:module_progress.completed:%d", prog.ID, occurred.UnixNano()),
		Now:            r.opts.Now,
	})
	env.TenantID = prog.TenantID
	env.GCID = prog.GCID
	env.OccurredAt = occurred
	if err := cgcenvelope.Validate(env); err != nil {
		return fmt.Errorf("pg: module_progress.completed envelope: %w", err)
	}

	payload := map[string]any{
		"module_id":    prog.ModuleID,
		"course_id":    prog.CourseID,
		"learner_gcid": prog.GCID,
		"tenant_id":    prog.TenantID,
		"completed_at": occurred.Format(time.RFC3339Nano),
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("pg: marshal module_progress.completed payload: %w", err)
	}
	envJSON, err := json.Marshal(map[string]string{
		"event_id":        env.EventID,
		"idempotency_key": env.IdempotencyKey,
		"tenant_id":       env.TenantID,
		"gcid":            env.GCID,
		"occurred_at":     env.OccurredAt.UTC().Format(time.RFC3339Nano),
		"published_at":    env.PublishedAt.UTC().Format(time.RFC3339Nano),
		"traceparent":     env.Traceparent,
		"tracestate":      env.Tracestate,
		"source_project":  env.SourceProject,
		"source_service":  env.SourceService,
		"schema_version":  strconv.Itoa(int(env.SchemaVersion)),
		"correlation_id":  env.CorrelationID,
		"causation_id":    env.CausationID,
	})
	if err != nil {
		return fmt.Errorf("pg: marshal module_progress.completed envelope: %w", err)
	}

	if _, err := q.Exec(ctx, SQLInsertModuleProgressCompletedOutbox,
		env.EventID, prog.TenantID, prog.GCID,
		"student_module_progress", prog.ID,
		"module_progress.completed", TopicModuleProgressCompleted,
		payloadJSON, string(envJSON), env.IdempotencyKey, occurred,
	); err != nil {
		return fmt.Errorf("pg: enqueue module_progress.completed outbox row: %w", err)
	}
	return nil
}

// GetByLearnerModule loads one learner's active projection for a module.
func (r *ProgressRepo) GetByLearnerModule(ctx context.Context, tenantID, gcid, moduleID string) (*moduleprogress.StudentModuleProgress, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var out *moduleprogress.StudentModuleProgress
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		p, err := scanProgress(q.QueryRow(ctx, SQLSelectProgressByLearnerModule, tenantID, gcid, moduleID).Scan)
		if err != nil {
			if isNoRows(err) {
				return nil
			}
			return err
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if out == nil {
		return nil, false, nil
	}
	return out, true, nil
}

// ListByModuleIDs returns active projections for the given modules (tenant-scoped).
func (r *ProgressRepo) ListByModuleIDs(ctx context.Context, tenantID string, moduleIDs []string) ([]*moduleprogress.StudentModuleProgress, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if len(moduleIDs) == 0 {
		return []*moduleprogress.StudentModuleProgress{}, nil
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	out := make([]*moduleprogress.StudentModuleProgress, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rs, err := q.Query(ctx, SQLSelectProgressByModuleIDs, tenantID, moduleIDs)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			p, scanErr := scanProgress(rs.Scan)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, p)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanProgress scans one row (via a Scan func) into a StudentModuleProgress,
// unmarshalling the completed_content_item_ids JSONB array.
func scanProgress(scan func(dest ...any) error) (*moduleprogress.StudentModuleProgress, error) {
	var (
		p           moduleprogress.StudentModuleProgress
		completedB  []byte
		completedAt *time.Time
		deletedAt   *time.Time
	)
	if err := scan(
		&p.ID, &p.TenantID, &p.GCID, &p.ModuleID, &p.CourseID,
		&completedB, &p.IsComplete, &completedAt,
		&p.CreatedAt, &p.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	if len(completedB) > 0 {
		if err := json.Unmarshal(completedB, &p.CompletedContentItemIDs); err != nil {
			return nil, fmt.Errorf("pg: unmarshal completed_content_item_ids: %w", err)
		}
	}
	p.CompletedAt = completedAt
	p.DeletedAt = deletedAt
	return &p, nil
}
