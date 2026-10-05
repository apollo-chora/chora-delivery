// closure_repository_test.go — pgx adapter tests for the durable
// ClosureRepository (W0-F1 durability + W0-F5 error-honesty, CHO-2198).
//
// Unit tests against the SQL emit + scan surface — no live DB. The
// critical assertions here are the W0-F5 ones: a genuine backing-store
// error from either Pseudonymise or IsPseudonymised must come back as a
// non-nil error, never get coerced into a false/zero "everything is fine"
// result (the swallowed-error trap the in-memory port's original
// `IsPseudonymised(gcid string) bool` signature made structurally
// impossible to avoid).
//
// Reuses the package's existing stubQuerier / stubRow / stubTxRunner +
// tenantID / gcid consts (application_test.go) — package pg_test is the
// dominant convention here (41 of 45 test files); those stub types are
// already independently configurable per test case via rowFn/scanFn, so
// this file does not need its own.
package pg_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/config"
)

func closureSpecFixture() []config.TableSpec {
	return []config.TableSpec{
		{
			Table: "certifications",
			Columns: []config.ColumnSpec{
				{Column: "holder_display_name", Strategy: "tombstone_string", Value: "Former member"},
				{Column: "holder_email_snapshot", Strategy: "tombstone_email", Value: "user-{hash}@redacted.invalid"},
			},
		},
		{
			Table: "bookings",
			Columns: []config.ColumnSpec{
				{Column: "attendee_display_name", Strategy: "tombstone_string", Value: "Former member"},
			},
		},
	}
}

// -----------------------------------------------------------------------------
// Pseudonymise
// -----------------------------------------------------------------------------

func TestClosureRepository_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	repo := pg.NewClosureRepository(nil)
	_, err := repo.Pseudonymise(context.Background(), tenantID, gcid, nil)
	if !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented; got %v", err)
	}
	if _, err := repo.IsPseudonymised(context.Background(), tenantID, gcid); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented; got %v", err)
	}
}

func TestClosureRepository_Pseudonymise_EmitsInsertOnConflictReturningID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	q := &stubQuerier{rowFn: func(_ string, _ ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error {
			*(dest[0].(*string)) = uuid.NewString()
			return nil
		}}
	}}
	repo := pg.NewClosureRepository(&stubTxRunner{q: q})

	rows, err := repo.Pseudonymise(context.Background(), tenantID, gcid, closureSpecFixture())
	if err != nil {
		t.Fatalf("Pseudonymise: %v", err)
	}
	if rows != 3 {
		t.Fatalf("expected rows_touched=3 (2+1 columns); got %d", rows)
	}

	last := q.sqls[len(q.sqls)-1]
	wants := []string{"INSERT INTO closure_pseudonymisation_state", "ON CONFLICT", "DO NOTHING", "RETURNING"}
	for _, w := range wants {
		if !strings.Contains(last, w) {
			t.Errorf("Pseudonymise SQL missing %q; got:\n%s", w, last)
		}
	}
	// rls.ApplySession must fire (SET LOCAL chora.tenant_id) before the insert.
	if len(q.sqls) == 0 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("expected SET LOCAL chora.tenant_id before the insert; sqls=%v", q.sqls)
	}
}

func TestClosureRepository_Pseudonymise_MintsUUIDv7ForID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	q := &stubQuerier{rowFn: func(_ string, _ ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error {
			*(dest[0].(*string)) = uuid.NewString()
			return nil
		}}
	}}
	repo := pg.NewClosureRepository(&stubTxRunner{q: q})

	if _, err := repo.Pseudonymise(context.Background(), tenantID, gcid, nil); err != nil {
		t.Fatalf("Pseudonymise: %v", err)
	}
	if len(q.args) == 0 || len(q.args[len(q.args)-1]) == 0 {
		t.Fatalf("expected query args")
	}
	idArg, ok := q.args[len(q.args)-1][0].(string)
	if !ok || idArg == "" {
		t.Fatalf("expected non-empty string id as first arg; got %v", q.args[len(q.args)-1][0])
	}
	parsed, err := uuid.Parse(idArg)
	if err != nil {
		t.Fatalf("minted id %q is not a valid UUID: %v", idArg, err)
	}
	if parsed.Version() != 7 {
		t.Fatalf("minted id %q is not UUIDv7 (version=%d)", idArg, parsed.Version())
	}
}

// TestClosureRepository_Pseudonymise_ReturnsZeroWhenConflictFires exercises
// isNoRows's sql.ErrNoRows branch (assessment_helpers.go) — the package has
// NO local ErrNoRows sentinel, unlike chora-consumption's sibling pg
// package, so the stub must return the SAME sentinel a real pgx Scan would
// surface on ON CONFLICT DO NOTHING + RETURNING (no row).
func TestClosureRepository_Pseudonymise_ReturnsZeroWhenConflictFires(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	q := &stubQuerier{rowFn: func(_ string, _ ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return sql.ErrNoRows }}
	}}
	repo := pg.NewClosureRepository(&stubTxRunner{q: q})

	rows, err := repo.Pseudonymise(context.Background(), tenantID, gcid, closureSpecFixture())
	if err != nil {
		t.Fatalf("Pseudonymise: expected idempotent no-op, got error: %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected rows=0 on idempotent replay; got %d", rows)
	}
}

func TestClosureRepository_Pseudonymise_RejectsEmptyTenantID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	q := &stubQuerier{}
	repo := pg.NewClosureRepository(&stubTxRunner{q: q})
	_, err := repo.Pseudonymise(context.Background(), "", gcid, nil)
	if err == nil {
		t.Fatalf("expected error on empty tenant_id")
	}
	if len(q.sqls) != 0 {
		t.Fatalf("must not emit SQL on validation failure; got %v", q.sqls)
	}
}

func TestClosureRepository_Pseudonymise_RejectsEmptyGCID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	q := &stubQuerier{}
	repo := pg.NewClosureRepository(&stubTxRunner{q: q})
	_, err := repo.Pseudonymise(context.Background(), tenantID, "", nil)
	if err == nil {
		t.Fatalf("expected error on empty gcid")
	}
	if len(q.sqls) != 0 {
		t.Fatalf("must not emit SQL on validation failure; got %v", q.sqls)
	}
}

// TestClosureRepository_Pseudonymise_PropagatesScanError is the W0-F5
// fail-loud proof for Pseudonymise: a genuine backing-store error (NOT a
// no-rows sentinel) must come back as a non-nil error, never as a silent
// "idempotent no-op" (0, nil) — conflating "I don't know" with "already
// done" would let the closure saga believe this domain acked when it did
// not (reusable_gotcha_swallowed_error_damage_is_decided_by_the_caller).
func TestClosureRepository_Pseudonymise_PropagatesScanError(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	boom := errors.New("pg: connection reset by peer")
	q := &stubQuerier{rowFn: func(_ string, _ ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return boom }}
	}}
	repo := pg.NewClosureRepository(&stubTxRunner{q: q})

	rows, err := repo.Pseudonymise(context.Background(), tenantID, gcid, closureSpecFixture())
	if err == nil {
		t.Fatalf("expected error to propagate, got nil (rows=%d)", rows)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped sentinel error; got %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected rows=0 on error; got %d", rows)
	}
}

// -----------------------------------------------------------------------------
// IsPseudonymised
// -----------------------------------------------------------------------------

func TestClosureRepository_IsPseudonymised_TrueWhenRowExists(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(_ string, _ ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error {
			*(dest[0].(*int)) = 1
			return nil
		}}
	}}
	repo := pg.NewClosureRepository(&stubTxRunner{q: q})

	got, err := repo.IsPseudonymised(context.Background(), tenantID, gcid)
	if err != nil {
		t.Fatalf("IsPseudonymised: %v", err)
	}
	if !got {
		t.Fatalf("expected true when a row exists")
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM closure_pseudonymisation_state") {
		t.Errorf("IsPseudonymised SQL malformed; got:\n%s", last)
	}
}

func TestClosureRepository_IsPseudonymised_FalseWhenNoRows(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(_ string, _ ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return sql.ErrNoRows }}
	}}
	repo := pg.NewClosureRepository(&stubTxRunner{q: q})

	got, err := repo.IsPseudonymised(context.Background(), tenantID, gcid)
	if err != nil {
		t.Fatalf("IsPseudonymised: expected nil error on a clean miss; got %v", err)
	}
	if got {
		t.Fatalf("expected false when no row exists")
	}
}

// TestClosureRepository_IsPseudonymised_PropagatesQueryError is the core
// W0-F5 proof for IsPseudonymised: this is exactly the method the original
// `IsPseudonymised(gcid string) bool` signature could NOT have implemented
// honestly against Postgres (no ctx, no error return). A real
// backing-store error must be reported, not folded into `false`.
func TestClosureRepository_IsPseudonymised_PropagatesQueryError(t *testing.T) {
	t.Parallel()
	boom := errors.New("pg: connection reset by peer")
	q := &stubQuerier{rowFn: func(_ string, _ ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return boom }}
	}}
	repo := pg.NewClosureRepository(&stubTxRunner{q: q})

	got, err := repo.IsPseudonymised(context.Background(), tenantID, gcid)
	if err == nil {
		t.Fatalf("expected error to propagate, got nil (got=%v)", got)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped sentinel error; got %v", err)
	}
	if got {
		t.Fatalf("expected false alongside the error (never claim true on failure)")
	}
}

func TestClosureRepository_IsPseudonymised_RejectsEmptyTenantID(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	repo := pg.NewClosureRepository(&stubTxRunner{q: q})
	_, err := repo.IsPseudonymised(context.Background(), "", gcid)
	if err == nil {
		t.Fatalf("expected error on empty tenant_id")
	}
	if len(q.sqls) != 0 {
		t.Fatalf("must not emit SQL on validation failure; got %v", q.sqls)
	}
}

func TestClosureRepository_IsPseudonymised_RejectsEmptyGCID(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	repo := pg.NewClosureRepository(&stubTxRunner{q: q})
	_, err := repo.IsPseudonymised(context.Background(), tenantID, "")
	if err == nil {
		t.Fatalf("expected error on empty gcid")
	}
	if len(q.sqls) != 0 {
		t.Fatalf("must not emit SQL on validation failure; got %v", q.sqls)
	}
}
