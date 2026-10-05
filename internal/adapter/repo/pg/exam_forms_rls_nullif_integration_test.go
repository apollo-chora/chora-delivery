//go:build integration

// exam_forms_rls_nullif_integration_test.go: the acceptance check for
// migration 0055_exam_forms_results_rls_nullif.
//
// THE DEFECT (mig 0046 lines 67 + 96): the exam_forms / exam_results
// tenant_isolation policies cast the tenant GUC without a NULLIF guard:
//
//	USING (tenant_id = current_setting('chora.tenant_id', true)::uuid)
//
// current_setting(..., true) yields NULL when the GUC was never SET, and NULL
// casts harmlessly. A POOLED connection is the problem: it reverts the GUC to
// the EMPTY STRING, and casting an empty string to uuid raises 22P02. The policy
// then THROWS instead of filtering, on a connection whose only sin is having
// been reused. exam_results
// carries the PASS/FAIL outcome the certification lane reads from a Pub/Sub
// subscriber, where a 22P02 is a NACK and a redelivery loop, not a visible 400.
//
// ⚠ REQUIRES migration 0055 TO BE APPLIED. Against the 0046 schema these tests
// FAIL with 22P02, and that failure IS the bug: it is the RED this migration
// answers. Run them after the (owner-gated) migrate lane applies 0055.
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration -run TestIntegration_ExamForms_EmptyGUC ./services/chora-delivery/internal/adapter/repo/pg/...
package pg_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// seedExamForm inserts one exam_forms row under a tenant-scoped GUC and returns
// its id, registering cleanup. It runs as app_rw (the DSN role): NOBYPASSRLS and
// a non-owner, so the policy genuinely binds (the same posture as production).
func seedExamForm(t *testing.T, tenant uuid.UUID) uuid.UUID {
	t.Helper()
	pool := liveDB(t)
	id, _ := uuid.NewV7()
	exam, _ := uuid.NewV7()
	bank, _ := uuid.NewV7()

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant)); err != nil {
		t.Fatalf("SET LOCAL: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO exam_forms (id, tenant_id, exam_id, item_bank_id, state, data)
		 VALUES ($1, $2, $3, $4, 'DRAFT', '{}'::jsonb)`,
		id, tenant, exam, bank); err != nil {
		t.Fatalf("seed INSERT: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		ctx2, err := pool.Begin(c)
		if err != nil {
			return
		}
		_, _ = ctx2.Exec(c, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant))
		_, _ = ctx2.Exec(c, `DELETE FROM exam_forms WHERE id = $1`, id)
		_ = ctx2.Commit(c)
	})
	return id
}

// TestIntegration_ExamForms_EmptyGUC_DeniesWithoutThrowing pins the fix: an
// EMPTY tenant GUC (what a pooled connection actually leaves behind) must match
// zero rows, not raise 22P02.
//
// The CONTROL runs FIRST and is what makes the zero-row assertion mean anything:
// it proves the row is really there and really readable under a tenant-bearing
// GUC. Without it, "0 rows" could just as well be an empty table, and the test
// would pass over a database that never held the row at all.
func TestIntegration_ExamForms_EmptyGUC_DeniesWithoutThrowing(t *testing.T) {
	pool := liveDB(t)
	tenant, _ := uuid.NewV7()
	id := seedExamForm(t, tenant)
	ctx := context.Background()

	// CONTROL: tenant GUC set → the row is visible.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant)); err != nil {
		t.Fatalf("SET LOCAL: %v", err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM exam_forms WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("control read: %v", err)
	}
	_ = tx.Rollback(ctx)
	if n != 1 {
		t.Fatalf("CONTROL FAILED: seeded row not readable under its own tenant (count=%d), so the zero-row assertion below would be a false negative", n)
	}

	// SUBJECT: GUC reverted to '' by connection reuse → deny, do not throw.
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx2.Rollback(ctx) }()
	if _, err := tx2.Exec(ctx, `SET LOCAL chora.tenant_id = ''`); err != nil {
		t.Fatalf("SET LOCAL '': %v", err)
	}
	var got int
	err = tx2.QueryRow(ctx, `SELECT count(*) FROM exam_forms WHERE id = $1`, id).Scan(&got)
	if err != nil {
		if strings.Contains(err.Error(), "22P02") || strings.Contains(err.Error(), "invalid input syntax for type uuid") {
			t.Fatalf("22P02 on an EMPTY tenant GUC, so migration 0055 is NOT applied to this database: %v", err)
		}
		t.Fatalf("empty-GUC read: %v", err)
	}
	if got != 0 {
		t.Fatalf("empty GUC must match no rows, got %d", got)
	}
}

// TestIntegration_ExamResults_EmptyGUC_DeniesWithoutThrowing covers the second
// table 0055 hardens. exam_results is the one that matters most: it holds the
// durable PASS/FAIL, and its reader is a subscriber, so a throw there is a
// silent redelivery loop rather than an error anybody sees.
func TestIntegration_ExamResults_EmptyGUC_DeniesWithoutThrowing(t *testing.T) {
	pool := liveDB(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL chora.tenant_id = ''`); err != nil {
		t.Fatalf("SET LOCAL '': %v", err)
	}
	// No seed needed: the 22P02 fires while EVALUATING the policy, so it throws
	// on any read, empty table or not. A clean count (0 or more) means the
	// NULLIF-guarded policy evaluated instead of exploding.
	var got int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM exam_results`).Scan(&got)
	if err != nil {
		if strings.Contains(err.Error(), "22P02") || strings.Contains(err.Error(), "invalid input syntax for type uuid") {
			t.Fatalf("22P02 on an EMPTY tenant GUC, so migration 0055 is NOT applied to this database: %v", err)
		}
		t.Fatalf("empty-GUC read: %v", err)
	}
	if got != 0 {
		t.Fatalf("empty GUC must match no rows, got %d", got)
	}
}
