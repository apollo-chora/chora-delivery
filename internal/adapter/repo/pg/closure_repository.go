// closure_repository.go — pgx-backed, durable implementation of the
// federated account-closure saga's ClosureRepository port (Tier 3 D11 /
// ADR-184 / ADR-186; W0-F1 durability + W0-F5 error-honesty, CHO-2198).
//
// Replaces events.NewInMemoryClosureRepo — the process-local map whose ack
// state (which (tenant, gcid) pairs this domain has already pseudonymised)
// was lost on every pod restart. That in-memory repo was wired UNGATED:
// gated on `pubsubClient != nil` only, never on pool health.
//
// Adapted from the chora-notifications reference (commit 79d683afa, the
// 9-service NewInMemoryClosureRepo class's reference implementation):
// chora-delivery's pg adapters live under internal/adapter/repo/pg (not
// internal/adapter/pg) and follow the TxRunner/Querier + rls.ApplySession
// seam every other repo in this package uses (see application.go — the
// Querier/Row/Rows/TxRunner shape + the qToExecer adapter — which is the
// UNIVERSAL call style across this package's ~35 repo files), NOT
// notifications' simpler Querier-only + local WithTenantID(ctx, tenantID)
// shape. The concrete pgx wiring for TxRunner also lives OUTSIDE this
// package (cmd/server/pgx_txrunner.go), a further divergence from
// chora-consumption's sibling pg package (which owns NewPgxTxRunner
// in-package) — this repo stays pgx-import-free like every other file
// here; cmd/server supplies the concrete TxRunner at construction.
//
// Because rls.ApplySession sources the tenant id from ctx via
// tracing.TenantIDFromContext — never from a SQL bind parameter
// (reusable_gotcha_rls_tenant_from_ctx_not_param) — and the closure
// subscriber's ctx arrives from a bare Pub/Sub pull loop with no HTTP
// middleware to stamp tenant context first (see closure_pull.go /
// ClosurePullHandler, which calls s.Handle(ctx, p) directly on the pull
// loop's ctx), this adapter stamps ctx = tracing.WithTenantID(ctx,
// tenantID) itself before opening the transaction — the direct equivalent
// of the reference's local `ctx = WithTenantID(ctx, tenantID)` step.
//
// SQL contract:
//
//   - Pseudonymise: INSERT ... ON CONFLICT (tenant_id, gcid) DO NOTHING
//     RETURNING id. A returned row means THIS call performed the durable
//     ack (fresh); isNoRows(err) (assessment_helpers.go's shared
//     sql.ErrNoRows / "no rows in result set" detector — the UNIVERSAL
//     not-found idiom across this package's ~24 call sites; this package
//     has NO local ErrNoRows sentinel the way chora-consumption's sibling
//     pg package does) means the conflict fired — another call already
//     acked this (tenant, gcid) pair, so this call is an idempotent no-op
//     (mirrors the in-memory repo's `if r.pseudonymed[key] { return 0, nil
//     }` contract, but race-safe: uniqueness is enforced by Postgres, not
//     a Go-side check-then-set two concurrent goroutines/pods could both
//     pass).
//   - IsPseudonymised: SELECT 1 ... LIMIT 1. A real backing-store error is
//     returned as an error — NEVER coerced into `false`. This is the
//     entire point of the widened port signature (see
//     closure_subscriber.go's ClosureRepository doc comment): the original
//     `IsPseudonymised(gcid string) bool` had no way to report a DB error,
//     so ANY pg-backed implementation of that old signature would have had
//     to swallow a connection error into "not yet pseudonymised" — a guard
//     read that fails OPEN.
//
// What this adapter does NOT do: it does not execute any per-table UPDATE
// against certifications / bookings / course_enrollment /
// rostering_assignment / skillsfutures_claims / project_groups /
// classroom_attendance_log (see config/PII_Closure_Map.yaml). Like the
// in-memory repo it replaces, `rows_touched` is a declared-intent count
// derived from the PII_Closure_Map.yaml spec (sum of columns), NOT an
// actual per-row UPDATE-affected count. Real per-table redaction is a
// separate, deeper gap — confirmed identical across all 9 closure-saga
// services (CHO-2198 durability report "no-op Pseudonymise" finding), not
// something this migration/adapter implements.
//
// RLS: closure_pseudonymisation_state is tenant-scoped (migration
// 0049_closure_pseudonymisation_state); every query threads tenantID
// through ctx via tracing.WithTenantID + rls.ApplySession so the
// tenant_isolation policy's `current_setting('chora.tenant_id')` GUC is set
// BEFORE the query runs — never a bare `WHERE tenant_id = $1` trusted alone.
//
// Cross-DB queries forbidden — this repo reads only chora_delivery.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/config"
)

// ErrClosureExecutorUnbuilt is returned by Pseudonymise until a real per-table
// executor exists.
//
// ⚠ WHY THIS FAILS CLOSED (disarm, 2026-08-14). Pseudonymise executes no
// per-table UPDATE at all: it writes one durable ack row and reports
// rows_touched as a DECLARED-INTENT count, the sum of columns listed in
// PII_Closure_Map.yaml. Measured across the platform: 9 services carry a closure
// repo, 0 of them contain any UPDATE, over 73 declared tables and 123 columns.
// Measured as never exercised: lifetime inserts on closure_pseudonymisation_state
// are 0.
//
// That is worse than dead code, because closure IS reachable from the admin
// account-lifecycle surface. On the first real closure the subscriber would
// publish status "ok" with a non-zero rows_touched under chora_imda_dimension
// "accountability", and the account would reach ACCOUNT_STATE_PSEUDONYMIZED,
// which the contract defines as "PII fields tokenized per PII_Closure_Map",
// having tokenised nothing. A false compliance attestation; absent code would at
// least have failed loudly.
//
// So the saga fails CLOSED rather than reaching a pseudonymised end state on the
// strength of work nobody did. Real per-table redaction is a separate, deeper
// gap (CHO-2198 "no-op Pseudonymise"); when it lands, this error goes with it.
var ErrClosureExecutorUnbuilt = errors.New(
	"closure: per-table PII_Closure_Map executor is not implemented; " +
		"refusing to report a pseudonymisation that did not happen")

// closureExecutorBuilt arms the real per-table PII_Closure_Map executor. It is
// false until CHO-2198 lands; the executor code below the guard is kept live
// (not unreachable) so it compiles and can be enabled by flipping this flag.
const closureExecutorBuilt = false

// ClosureRepository is the pgx-backed, durable implementation of the
// events.ClosureRepository port.
type ClosureRepository struct {
	tx TxRunner
}

// NewClosureRepository wraps a TxRunner. Tests inject a stub; production
// wires the cmd/server-local pgxTxRunner (see cmd/server/pgx_txrunner.go).
func NewClosureRepository(tx TxRunner) *ClosureRepository {
	return &ClosureRepository{tx: tx}
}

// Column order matches
// migrations/0049_closure_pseudonymisation_state.up.sql: id, tenant_id,
// gcid, rows_touched.
const insertClosurePseudonymisationStateSQL = `
        INSERT INTO closure_pseudonymisation_state (id, tenant_id, gcid, rows_touched)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (tenant_id, gcid) DO NOTHING
        RETURNING id
    `

const selectClosurePseudonymisationStateSQL = `SELECT 1 FROM closure_pseudonymisation_state WHERE tenant_id = $1 AND gcid = $2 LIMIT 1`

// Pseudonymise durably acks that (tenantID, gcid) has been processed by
// this domain's closure subscriber. Idempotent: a replay (same tenant,
// gcid) is a no-op that returns (0, nil), never a duplicate row or an
// error.
func (r *ClosureRepository) Pseudonymise(ctx context.Context, tenantID, gcid string, spec []config.TableSpec) (int, error) {
	// Disarmed until a real executor exists: see ErrClosureExecutorUnbuilt.
	// Placed FIRST so no ack row is written and no success is published. The
	// guard is a named constant (not a bare return) so the intended executor
	// below stays live code rather than an unreachable block.
	if !closureExecutorBuilt {
		return 0, ErrClosureExecutorUnbuilt
	}

	if r == nil || r.tx == nil {
		return 0, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return 0, errors.New("pg.ClosureRepository.Pseudonymise: tenant_id required")
	}
	if strings.TrimSpace(gcid) == "" {
		return 0, errors.New("pg.ClosureRepository.Pseudonymise: gcid required")
	}

	id, err := uuid.NewV7()
	if err != nil {
		return 0, fmt.Errorf("pg.ClosureRepository.Pseudonymise: uuidv7: %w", err)
	}

	// Declared-intent row count from the PII map — see package doc: this
	// adapter does not itself touch certifications/bookings/etc.
	rows := 0
	for _, t := range spec {
		rows += len(t.Columns)
	}

	// RLS tenant scope (see package doc): stamp ctx since this call path
	// (Pub/Sub pull loop) has no HTTP middleware to have done it already.
	ctx = tracing.WithTenantID(ctx, tenantID)

	var insertedID string
	err = r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, insertClosurePseudonymisationStateSQL, id.String(), tenantID, gcid, rows)
		return row.Scan(&insertedID)
	})
	if err != nil {
		if isNoRows(err) {
			// ON CONFLICT DO NOTHING fired: another call already durably
			// acked this (tenant, gcid) pair. Idempotent no-op — fail loud
			// only on a GENUINE backing-store error (below).
			return 0, nil
		}
		return 0, fmt.Errorf("pg.ClosureRepository.Pseudonymise: %w", err)
	}
	return rows, nil
}

// IsPseudonymised reports whether (tenantID, gcid) has already been
// durably acked. A real backing-store error is returned as a non-nil error
// and MUST NOT be treated as `false` by any caller — see the
// ClosureRepository port doc comment in closure_subscriber.go.
func (r *ClosureRepository) IsPseudonymised(ctx context.Context, tenantID, gcid string) (bool, error) {
	if r == nil || r.tx == nil {
		return false, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return false, errors.New("pg.ClosureRepository.IsPseudonymised: tenant_id required")
	}
	if strings.TrimSpace(gcid) == "" {
		return false, errors.New("pg.ClosureRepository.IsPseudonymised: gcid required")
	}

	ctx = tracing.WithTenantID(ctx, tenantID)

	found := false
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, selectClosurePseudonymisationStateSQL, tenantID, gcid)
		var probe int
		if scanErr := row.Scan(&probe); scanErr != nil {
			if isNoRows(scanErr) {
				return nil
			}
			return scanErr
		}
		found = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("pg.ClosureRepository.IsPseudonymised: %w", err)
	}
	return found, nil
}
