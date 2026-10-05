// Package pg StudentModuleProgress repo tests — W7 completion-event emission
// (CHO-2124).
//
// Advance already folds a completion into the projection under one tx; these
// tests pin the NEW producer-side contract: the false→true IsComplete
// transition enqueues EXACTLY ONE chora.delivery.module_progress.completed.v1
// outbox row through the SAME Querier inside the SAME RunInTx closure as the
// projection UPDATE (atomic state-write + event-publish), and NO other
// recompute shape emits:
//
//   - replay of an already-recorded completion  → no row (aggregate no-op)
//   - further completions while already complete → no row
//   - true→false recompute (module grew)         → no row
//
// The stub Querier captures every SQL + arg binding so the assertions read
// the exact outbox row the production path would INSERT.
package pg_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
)

const (
	mptTenant = "01980000-0000-7000-8000-000000000001"
	mptGCID   = "01980000-0000-7000-8000-00000000abcd"
	mptCourse = "01980000-0000-7000-8000-000000000c01"
	mptRowID  = "01980000-0000-7000-8000-00000000f001"
	mptItem1  = "01980000-0000-7000-9000-0000000000a1"
	mptItem2  = "01980000-0000-7000-9000-0000000000a2"
)

// mptTxRunner runs fn with the supplied Querier and counts entries so the
// same-tx invariant (projection write + outbox insert in ONE closure) is
// structurally assertable.
type mptTxRunner struct {
	q    pg.Querier
	runs int
}

func (r *mptTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q pg.Querier) error) error {
	r.runs++
	return fn(ctx, r.q)
}

// failOnOutboxQuerier delegates to the embedded stubQuerier but fails the
// outbox INSERT — pinning that an enqueue failure surfaces (tx rollback)
// rather than being swallowed.
type failOnOutboxQuerier struct {
	*stubQuerier
	failErr error
}

func (f *failOnOutboxQuerier) Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error) {
	if strings.Contains(sql, "INSERT INTO outbox_events") {
		f.stubQuerier.sqls = append(f.stubQuerier.sqls, sql)
		f.stubQuerier.args = append(f.stubQuerier.args, args)
		return rls.CommandTag{}, f.failErr
	}
	return f.stubQuerier.Exec(ctx, sql, args...)
}

// mptModule builds an active module for mptTenant/mptCourse with the given
// content items and requirement.
func mptModule(t *testing.T, itemIDs []string, req *module.ModuleRequirement) *module.Module {
	t.Helper()
	m, err := module.New(module.NewParams{TenantID: mptTenant, CourseID: mptCourse, Title: "W7 module"})
	if err != nil {
		t.Fatalf("module.New: %v", err)
	}
	for _, id := range itemIDs {
		if _, err := m.AddItem(id); err != nil {
			t.Fatalf("AddItem(%s): %v", id, err)
		}
	}
	if req != nil {
		if err := m.SetRequirement(*req); err != nil {
			t.Fatalf("SetRequirement: %v", err)
		}
	}
	return m
}

// mptProgressRowFn scripts the SELECT ... FOR UPDATE result: the pre-state
// projection row Advance locks + mutates. Column order mirrors
// progressSelectCols.
func mptProgressRowFn(m *module.Module, completed []string, isComplete bool, completedAt *time.Time) func(sql string, args ...any) pg.Row {
	return func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error {
			completedB, err := json.Marshal(completed)
			if err != nil {
				return err
			}
			now := time.Now().UTC().Add(-time.Hour)
			*(dest[0].(*string)) = mptRowID
			*(dest[1].(*string)) = mptTenant
			*(dest[2].(*string)) = mptGCID
			*(dest[3].(*string)) = m.ID
			*(dest[4].(*string)) = mptCourse
			*(dest[5].(*[]byte)) = completedB
			*(dest[6].(*bool)) = isComplete
			*(dest[7].(**time.Time)) = completedAt
			*(dest[8].(*time.Time)) = now
			*(dest[9].(*time.Time)) = now
			*(dest[10].(**time.Time)) = nil
			return nil
		}}
	}
}

// outboxInserts returns the indices of captured outbox INSERT statements.
func outboxInserts(q *stubQuerier) []int {
	var idx []int
	for i, sql := range q.sqls {
		if strings.Contains(sql, "INSERT INTO outbox_events") {
			idx = append(idx, i)
		}
	}
	return idx
}

func indexOfSQL(q *stubQuerier, needle string) int {
	for i, sql := range q.sqls {
		if strings.Contains(sql, needle) {
			return i
		}
	}
	return -1
}

// -----------------------------------------------------------------------------
// AC1 — false→true transition enqueues EXACTLY ONE completed.v1 outbox row in
// the SAME tx as the projection UPDATE, payload + envelope complete.
// -----------------------------------------------------------------------------

func TestProgressRepo_Advance_CompletionEdge_EnqueuesCompletedEventSameTx(t *testing.T) {
	t.Parallel()
	m := mptModule(t, []string{mptItem1}, nil) // all_items over one item
	q := &stubQuerier{rowFn: mptProgressRowFn(m, nil, false, nil)}
	tx := &mptTxRunner{q: q}
	fixedNow := time.Date(2026, 7, 11, 3, 4, 5, 0, time.UTC)
	testStart := time.Now().UTC().Add(-time.Second)

	r := pg.NewProgressRepo(tx, pg.ProgressRepoOptions{
		SourceProject: "proj-under-test",
		SourceService: "svc-under-test",
		Now:           func() time.Time { return fixedNow },
	})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	changed, err := r.Advance(ctx, mptTenant, mptGCID, m.ID, mptCourse, mptItem1, m)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true on the completion edge")
	}
	if tx.runs != 1 {
		t.Fatalf("expected ONE RunInTx entry (same-tx write+enqueue); got %d", tx.runs)
	}

	inserts := outboxInserts(q)
	if len(inserts) != 1 {
		t.Fatalf("expected EXACTLY ONE outbox insert; got %d (sqls=%v)", len(inserts), q.sqls)
	}
	updIdx := indexOfSQL(q, "UPDATE student_module_progress")
	if updIdx < 0 {
		t.Fatalf("projection UPDATE not executed")
	}
	if inserts[0] < updIdx {
		t.Fatalf("outbox insert ran before the projection UPDATE (insert=%d update=%d)", inserts[0], updIdx)
	}
	if !strings.Contains(q.sqls[inserts[0]], "'pending'") {
		t.Fatalf("outbox insert must enqueue status='pending'; sql=%s", q.sqls[inserts[0]])
	}

	args := q.args[inserts[0]]
	if len(args) != 11 {
		t.Fatalf("outbox insert arg count = %d, want 11", len(args))
	}
	eventID, _ := args[0].(string)
	if eventID == "" {
		t.Fatalf("outbox row id (event_id) empty")
	}
	if got := args[1]; got != mptTenant {
		t.Fatalf("outbox tenant_id = %v, want %s", got, mptTenant)
	}
	if got := args[2]; got != mptGCID {
		t.Fatalf("outbox gcid = %v, want %s", got, mptGCID)
	}
	if got := args[3]; got != "student_module_progress" {
		t.Fatalf("aggregate_type = %v, want student_module_progress", got)
	}
	if got := args[4]; got != mptRowID {
		t.Fatalf("aggregate_id = %v, want the projection row id %s", got, mptRowID)
	}
	if got := args[5]; got != "module_progress.completed" {
		t.Fatalf("event_type = %v, want module_progress.completed", got)
	}
	if got := args[6]; got != pg.TopicModuleProgressCompleted {
		t.Fatalf("topic = %v, want %s", got, pg.TopicModuleProgressCompleted)
	}
	if pg.TopicModuleProgressCompleted != "chora.delivery.module_progress.completed.v1" {
		t.Fatalf("topic const = %q, want chora.delivery.module_progress.completed.v1", pg.TopicModuleProgressCompleted)
	}

	// Payload — the consumption-side JSON-schemaless contract, exactly.
	payloadB, ok := args[7].([]byte)
	if !ok {
		t.Fatalf("payload arg is %T, want []byte", args[7])
	}
	var payload map[string]string
	if err := json.Unmarshal(payloadB, &payload); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, payloadB)
	}
	if len(payload) != 5 {
		t.Fatalf("payload has %d fields, want exactly 5 (contract): %v", len(payload), payload)
	}
	if payload["module_id"] != m.ID || payload["course_id"] != mptCourse ||
		payload["learner_gcid"] != mptGCID || payload["tenant_id"] != mptTenant {
		t.Fatalf("payload identity fields wrong: %v", payload)
	}
	completedAt, err := time.Parse(time.RFC3339Nano, payload["completed_at"])
	if err != nil {
		t.Fatalf("payload completed_at not RFC3339Nano: %v", err)
	}
	if completedAt.Before(testStart) {
		t.Fatalf("payload completed_at %s predates the test", payload["completed_at"])
	}

	// Envelope — the mandatory attribute set, mirroring delivery's outbox tee.
	envStr, ok := args[8].(string)
	if !ok {
		t.Fatalf("envelope arg is %T, want string (JSONB)", args[8])
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(envStr), &env); err != nil {
		t.Fatalf("envelope not JSON: %v", err)
	}
	for _, key := range []string{
		"event_id", "idempotency_key", "tenant_id", "gcid", "occurred_at",
		"published_at", "traceparent", "source_project", "source_service",
		"schema_version",
	} {
		if env[key] == "" {
			t.Fatalf("envelope %s empty: %v", key, env)
		}
	}
	for _, key := range []string{"tracestate", "correlation_id", "causation_id"} {
		if _, present := env[key]; !present {
			t.Fatalf("envelope missing key %s (delivery attribute-set parity): %v", key, env)
		}
	}
	if env["event_id"] != eventID {
		t.Fatalf("envelope event_id %s != row id %s", env["event_id"], eventID)
	}
	if env["tenant_id"] != mptTenant || env["gcid"] != mptGCID {
		t.Fatalf("envelope identity wrong: %v", env)
	}
	if env["source_project"] != "proj-under-test" || env["source_service"] != "svc-under-test" {
		t.Fatalf("envelope source_* did not flow from options: %v", env)
	}
	if env["schema_version"] != "1" {
		t.Fatalf("schema_version = %s, want 1", env["schema_version"])
	}
	if env["occurred_at"] != payload["completed_at"] {
		t.Fatalf("envelope occurred_at %s != payload completed_at %s", env["occurred_at"], payload["completed_at"])
	}
	if env["published_at"] != fixedNow.Format(time.RFC3339Nano) {
		t.Fatalf("published_at = %s, want injected Now %s", env["published_at"], fixedNow.Format(time.RFC3339Nano))
	}

	// Idempotency key: deterministic per completion instance.
	idemKey, _ := args[9].(string)
	if idemKey == "" || idemKey != env["idempotency_key"] {
		t.Fatalf("idempotency_key arg %q != envelope %q", idemKey, env["idempotency_key"])
	}
	if !strings.HasPrefix(idemKey, mptRowID+":module_progress.completed") {
		t.Fatalf("idempotency_key %q not derived from the projection row id", idemKey)
	}

	occurredArg, ok := args[10].(time.Time)
	if !ok {
		t.Fatalf("occurred_at arg is %T, want time.Time", args[10])
	}
	if !occurredArg.Equal(completedAt) {
		t.Fatalf("occurred_at arg %s != completion instant %s", occurredArg, completedAt)
	}
}

// -----------------------------------------------------------------------------
// AC2 — replaying an already-recorded completion is a no-op: no UPDATE, no
// second publish.
// -----------------------------------------------------------------------------

func TestProgressRepo_Advance_ReplayOfCompletedItem_NoSecondPublish(t *testing.T) {
	t.Parallel()
	m := mptModule(t, []string{mptItem1}, nil)
	doneAt := time.Now().UTC().Add(-time.Minute)
	q := &stubQuerier{rowFn: mptProgressRowFn(m, []string{mptItem1}, true, &doneAt)}
	tx := &mptTxRunner{q: q}
	r := pg.NewProgressRepo(tx, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	changed, err := r.Advance(ctx, mptTenant, mptGCID, m.ID, mptCourse, mptItem1, m)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if changed {
		t.Fatalf("expected changed=false on idempotent replay")
	}
	if idx := indexOfSQL(q, "UPDATE student_module_progress"); idx >= 0 {
		t.Fatalf("no projection UPDATE expected on replay; sqls=%v", q.sqls)
	}
	if got := outboxInserts(q); len(got) != 0 {
		t.Fatalf("replay must not publish; got %d outbox inserts", len(got))
	}
}

// -----------------------------------------------------------------------------
// AC3 — a further completion while is_complete stays true changes the set but
// must NOT publish (no false→true edge).
// -----------------------------------------------------------------------------

func TestProgressRepo_Advance_FurtherCompletionWhileComplete_NoPublish(t *testing.T) {
	t.Parallel()
	req := &module.ModuleRequirement{Kind: module.RequirementNOfM, ThresholdN: 1}
	m := mptModule(t, []string{mptItem1, mptItem2}, req)
	doneAt := time.Now().UTC().Add(-time.Minute)
	q := &stubQuerier{rowFn: mptProgressRowFn(m, []string{mptItem1}, true, &doneAt)}
	tx := &mptTxRunner{q: q}
	r := pg.NewProgressRepo(tx, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	changed, err := r.Advance(ctx, mptTenant, mptGCID, m.ID, mptCourse, mptItem2, m)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true (completed set grew)")
	}
	if idx := indexOfSQL(q, "UPDATE student_module_progress"); idx < 0 {
		t.Fatalf("projection UPDATE expected; sqls=%v", q.sqls)
	}
	if got := outboxInserts(q); len(got) != 0 {
		t.Fatalf("already-complete progress must not re-publish; got %d outbox inserts", len(got))
	}
}

// -----------------------------------------------------------------------------
// AC4 — the true→false recompute (module grew past the learner's set) writes
// the demotion but must NOT publish.
// -----------------------------------------------------------------------------

func TestProgressRepo_Advance_TrueToFalseRecompute_NoPublish(t *testing.T) {
	t.Parallel()
	m := mptModule(t, []string{mptItem1, mptItem2}, nil) // all_items over two items
	doneAt := time.Now().UTC().Add(-time.Minute)
	// Stale state: complete on the old one-item module shape.
	q := &stubQuerier{rowFn: mptProgressRowFn(m, []string{mptItem1}, true, &doneAt)}
	tx := &mptTxRunner{q: q}
	r := pg.NewProgressRepo(tx, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	changed, err := r.Advance(ctx, mptTenant, mptGCID, m.ID, mptCourse, mptItem1, m)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true on the true→false recompute")
	}
	updIdx := indexOfSQL(q, "UPDATE student_module_progress")
	if updIdx < 0 {
		t.Fatalf("projection UPDATE expected; sqls=%v", q.sqls)
	}
	if got, _ := q.args[updIdx][1].(bool); got {
		t.Fatalf("recompute should demote is_complete to false")
	}
	if got := outboxInserts(q); len(got) != 0 {
		t.Fatalf("true→false must not publish; got %d outbox inserts", len(got))
	}
}

// -----------------------------------------------------------------------------
// Fail-loud — an outbox enqueue failure aborts the tx (state flip + event are
// atomic BOTH ways: neither lands without the other).
// -----------------------------------------------------------------------------

func TestProgressRepo_Advance_OutboxInsertFailure_AbortsTx(t *testing.T) {
	t.Parallel()
	m := mptModule(t, []string{mptItem1}, nil)
	inner := &stubQuerier{rowFn: mptProgressRowFn(m, nil, false, nil)}
	boom := errors.New("outbox unavailable")
	tx := &mptTxRunner{q: &failOnOutboxQuerier{stubQuerier: inner, failErr: boom}}
	r := pg.NewProgressRepo(tx, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	changed, err := r.Advance(ctx, mptTenant, mptGCID, m.ID, mptCourse, mptItem1, m)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the enqueue failure to surface; got %v", err)
	}
	if changed {
		t.Fatalf("changed must be false when the tx aborts")
	}
}

// -----------------------------------------------------------------------------
// Defaults — zero options stamp the canonical source project/service.
// -----------------------------------------------------------------------------

func TestProgressRepo_Advance_DefaultOptions_StampSourceProjectService(t *testing.T) {
	t.Parallel()
	m := mptModule(t, []string{mptItem1}, nil)
	q := &stubQuerier{rowFn: mptProgressRowFn(m, nil, false, nil)}
	tx := &mptTxRunner{q: q}
	r := pg.NewProgressRepo(tx, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	if _, err := r.Advance(ctx, mptTenant, mptGCID, m.ID, mptCourse, mptItem1, m); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	inserts := outboxInserts(q)
	if len(inserts) != 1 {
		t.Fatalf("expected one outbox insert; got %d", len(inserts))
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(q.args[inserts[0]][8].(string)), &env); err != nil {
		t.Fatalf("envelope not JSON: %v", err)
	}
	if env["source_project"] != "chora-489812" || env["source_service"] != "chora-delivery" {
		t.Fatalf("default source stamps wrong: %v", env)
	}
}
