// testset_test.go — unit tests for the pgx-backed TestSetRepo.
//
// Mirrors course_test.go + application_test.go. Stubs the Querier so the SQL
// surface is exercised without a live DB; the SET LOCAL chora.tenant_id RLS
// contract is asserted via the stub call ordering.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	tsAuthorGCID = "00000000-0000-7000-8000-000000001999"
	tsQuestionID = "01985e7f-1234-7abc-8def-000000000001"
)

// -----------------------------------------------------------------------------
// Tests — TestSetRepo
// -----------------------------------------------------------------------------

func TestTestSetRepo_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewTestSetRepo(nil)
	if err := r.Save(context.Background(), &domain.TestSet{}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, _, err := r.Get(context.Background(), tenantID, "nope"); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Get: expected ErrNotImplemented; got %v", err)
	}
}

func TestTestSetRepo_Save_RejectsNil(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err := r.Save(ctx, nil)
	if !errors.Is(err, pg.ErrInvalidTestSet) {
		t.Fatalf("expected ErrInvalidTestSet on nil; got %v", err)
	}
}

func TestTestSetRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:   tenantID,
		AuthorGCID: tsAuthorGCID,
		Title:      "Phyllis Math Test Set 1",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, ts); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 {
		t.Fatalf("expected at least 2 SQLs (SET LOCAL + UPSERT); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	// Look for UPSERT on test_sets.
	foundUpsert := false
	for _, s := range q.sqls {
		if strings.Contains(s, "INSERT INTO test_sets") &&
			strings.Contains(s, "ON CONFLICT (test_set_id)") {
			foundUpsert = true
			break
		}
	}
	if !foundUpsert {
		t.Fatalf("expected UPSERT INTO test_sets; got SQLs: %v", q.sqls)
	}
}

func TestTestSetRepo_Save_PersistsLiveQuestions(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	ts, _ := domain.NewTestSet(domain.NewTestSetInput{
		TenantID: tenantID, AuthorGCID: tsAuthorGCID, Title: "T",
	})
	if _, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: tsQuestionID, QuestionID: tsQuestionID, QuestionType: "mcq", Points: 2.0,
	}); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}

	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, ts); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Must produce a write for the question row too.
	foundQuestionWrite := false
	for _, s := range q.sqls {
		if strings.Contains(s, "test_set_questions") &&
			(strings.Contains(s, "INSERT INTO") || strings.Contains(s, "UPDATE")) {
			foundQuestionWrite = true
			break
		}
	}
	if !foundQuestionWrite {
		t.Fatalf("expected at least one test_set_questions write SQL; got: %v", q.sqls)
	}
}

func TestTestSetRepo_Get_NoRow_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows in result set")
			}}
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	ts, ok, err := r.Get(ctx, tenantID, "does-not-exist")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok || ts != nil {
		t.Fatalf("expected ok=false / nil; got ok=%v ts=%v", ok, ts)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
}

func TestTestSetRepo_SaveQuestionRemoval_AppliesRLSAndSoftDeletes(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)
	ts, _ := domain.NewTestSet(domain.NewTestSetInput{
		TenantID: tenantID, AuthorGCID: tsAuthorGCID, Title: "T",
	})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.SaveQuestionRemoval(ctx, ts, "tsq-1"); err != nil {
		t.Fatalf("SaveQuestionRemoval: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	foundSoftDelete := false
	for _, s := range q.sqls {
		if strings.Contains(s, "UPDATE test_set_questions") &&
			strings.Contains(s, "deleted_at") {
			foundSoftDelete = true
			break
		}
	}
	if !foundSoftDelete {
		t.Fatalf("expected UPDATE test_set_questions SET deleted_at; got: %v", q.sqls)
	}
}

func TestTestSetRepo_SaveQuestionRemoval_RejectsNil(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.SaveQuestionRemoval(ctx, nil, "x"); !errors.Is(err, pg.ErrInvalidTestSet) {
		t.Fatalf("expected ErrInvalidTestSet; got %v", err)
	}
}

func TestTestSetRepo_SaveQuestionRemoval_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewTestSetRepo(nil)
	ts, _ := domain.NewTestSet(domain.NewTestSetInput{
		TenantID: tenantID, AuthorGCID: tsAuthorGCID, Title: "T",
	})
	if err := r.SaveQuestionRemoval(context.Background(), ts, "x"); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented; got %v", err)
	}
}

// TestTestSetRepo_Save_BindsSnapshotColumn — Fix-F: Save must bind
// payload_snapshot + snapshot_at args ($11 + $12) on the test_set_questions
// UPSERT so the migration 0011 columns get the canonical chora-creation
// payload that PublishWithSnapshot captures. Post-LEG3-D R3 (migration
// 0013) the column list is 12-wide with `question_id` added at $4.
func TestTestSetRepo_Save_BindsSnapshotColumn(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	ts, _ := domain.NewTestSet(domain.NewTestSetInput{
		TenantID: tenantID, AuthorGCID: tsAuthorGCID, Title: "T",
	})
	qrow, _ := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: tsQuestionID, QuestionID: tsQuestionID, QuestionType: "mcq", Points: 2.0,
	})
	// Simulate the post-PublishWithSnapshot state — payload_snapshot is set.
	qrow.PayloadSnapshot = `{"options":[{"option_id":"opt-a","is_correct":true}]}`
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, ts); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Find the test_set_questions UPSERT call and verify the payload_snapshot
	// bind. LEG3-D R3 Option B added `question_id` ($4); the column list is
	// now 12-wide: test_set_question_id, test_set_id, question_atom_id,
	// question_id, question_type, display_order, points, created_at,
	// updated_at, deleted_at, payload_snapshot, snapshot_at. payload_snapshot
	// = args[10], snapshot_at = args[11].
	var found bool
	for i, s := range q.sqls {
		if strings.Contains(s, "INSERT INTO test_set_questions") {
			args := q.args[i]
			if len(args) < 12 {
				t.Fatalf("test_set_questions UPSERT must bind ≥ 12 args (question_id + snapshot columns added); got %d", len(args))
			}
			if args[10] == nil {
				t.Errorf("payload_snapshot bind ($11): expected non-nil for snapshot-set row, got nil")
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected test_set_questions UPSERT call; got SQLs: %v", q.sqls)
	}
}

// TestTestSetRepo_Save_StoresAuthorSafeSnapshotVerbatim — ATOM-2.
// Asserts that the AUTHOR-SAFE payload_snapshot (full content + is_correct
// + explainer + model_answer + rubric — the shape B6 emits from
// chora-creation's SnapshotQuestionByID) is bound verbatim into the
// JSONB column WITHOUT any projection / strip at WRITE time. The
// LEARNER-SAFE projection happens at READ time on the chora-delivery
// HTTP boundary — chora-delivery is the boundary that splits.
//
// Per `feedback_resilience_priority` + ATOM-2 §"Snapshot UPSERT — verify
// AUTHOR-SAFE is stored verbatim": projection at READ time is the only
// correct boundary because the same row serves BOTH the pre-RELEASE
// learner-safe canvas AND the post-RELEASE author-safe reveal.
func TestTestSetRepo_Save_StoresAuthorSafeSnapshotVerbatim(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	ts, _ := domain.NewTestSet(domain.NewTestSetInput{
		TenantID: tenantID, AuthorGCID: tsAuthorGCID, Title: "ATOM-2 round-trip",
	})

	// MCQ — full AUTHOR-SAFE shape with is_correct + explainer.
	mcqRow, _ := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: tsQuestionID, QuestionID: tsQuestionID, QuestionType: "mcq", Points: 5.0,
	})
	mcqSnap := `{"stem":"What gas do plants release as byproduct of photosynthesis?","options":[{"option_id":"opt-A","label":"Oxygen","is_correct":true,"explainer":"Correct — water splits to O2."},{"option_id":"opt-B","label":"CO2","is_correct":false,"explainer":"CO2 is consumed, not released."}],"scoring":{"mode":"single_correct"}}`
	mcqRow.PayloadSnapshot = mcqSnap

	// OE — full AUTHOR-SAFE shape with model_answer + rubric.
	oeQuestionID := "01985e7f-1234-7abc-8def-00000000aaaa"
	oeRow, _ := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: oeQuestionID, QuestionID: oeQuestionID, QuestionType: "oe", Points: 10.0,
	})
	oeSnap := `{"stem":"Why is photosynthesis important?","model_answer":"It produces oxygen + glucose; cornerstone of the food web.","rubric":[{"criterion_id":"c1","title":"Clarity","weight":0.4},{"criterion_id":"c2","title":"Accuracy","weight":0.6}],"max_words":150}`
	oeRow.PayloadSnapshot = oeSnap

	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, ts); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Walk every test_set_questions UPSERT and verify the bound payload
	// is byte-for-byte equal to the input — no projection at WRITE time.
	var mcqVerified, oeVerified bool
	for i, s := range q.sqls {
		if !strings.Contains(s, "INSERT INTO test_set_questions") {
			continue
		}
		args := q.args[i]
		if len(args) < 12 {
			t.Fatalf("test_set_questions UPSERT must bind ≥ 12 args; got %d", len(args))
		}
		// args[10] is the payload_snapshot bind (nullJSONString returns either
		// nil OR the original string verbatim — never a projection).
		bound, _ := args[10].(string)
		if bound == "" {
			continue
		}
		switch bound {
		case mcqSnap:
			mcqVerified = true
			// Sanity — author-only fields survive the write path.
			for _, mustContain := range []string{`"is_correct":true`, `"explainer"`, `"correct — water splits to O2."`} {
				if !strings.Contains(strings.ToLower(bound), strings.ToLower(mustContain)) {
					t.Errorf("ATOM-2: MCQ payload_snapshot bind is missing %q (write-time projection contaminated AUTHOR-SAFE shape); bound=%s", mustContain, bound)
				}
			}
		case oeSnap:
			oeVerified = true
			for _, mustContain := range []string{`"model_answer"`, `"rubric"`, `"criterion_id"`} {
				if !strings.Contains(bound, mustContain) {
					t.Errorf("ATOM-2: OE payload_snapshot bind is missing %q; bound=%s", mustContain, bound)
				}
			}
		}
	}
	if !mcqVerified {
		t.Errorf("ATOM-2: did not observe an MCQ payload_snapshot bind equal to the input shape (write-time projection?)")
	}
	if !oeVerified {
		t.Errorf("ATOM-2: did not observe an OE payload_snapshot bind equal to the input shape (write-time projection?)")
	}
}

func TestTestSetRepo_Get_HappyPath_LoadsRowAndQuestions(t *testing.T) {
	t.Parallel()
	calls := 0
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			calls++
			// The first Row call after SET LOCAL fetches test_sets header.
			return stubRow{scanFn: func(dest ...any) error {
				// 9 cols: test_set_id, tenant_id, author_gcid, title, description, state, created_at, updated_at, published_at
				if v, ok := dest[0].(*string); ok {
					*v = "ts-1"
				}
				if v, ok := dest[1].(*string); ok {
					*v = tenantID
				}
				if v, ok := dest[2].(*string); ok {
					*v = tsAuthorGCID
				}
				if v, ok := dest[3].(*string); ok {
					*v = "Phyllis Test"
				}
				return nil
			}}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			// Question listing returns one row.
			// LEG3-D R3 Option B — scan now reads 10 cols:
			//   test_set_question_id, test_set_id, question_atom_id, question_id,
			//   question_type, display_order, points, created_at,
			//   payload_snapshot, snapshot_at.
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					if v, ok := dest[0].(*string); ok {
						*v = "q-1"
					}
					if v, ok := dest[1].(*string); ok {
						*v = "ts-1"
					}
					if v, ok := dest[2].(*string); ok {
						*v = tsQuestionID
					}
					if v, ok := dest[3].(*string); ok {
						*v = tsQuestionID // question_id distinct in prod; stub reuses for simplicity
					}
					if v, ok := dest[4].(*string); ok {
						*v = "mcq"
					}
					if v, ok := dest[5].(*int); ok {
						*v = 1
					}
					if v, ok := dest[6].(*float64); ok {
						*v = 2.0
					}
					return nil
				},
			}}, nil
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	ts, ok, err := r.Get(ctx, tenantID, "ts-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || ts == nil {
		t.Fatalf("expected ok=true; got ok=%v ts=%v", ok, ts)
	}
	if ts.QuestionCount() != 1 {
		t.Fatalf("expected QuestionCount=1; got %d", ts.QuestionCount())
	}
	if ts.TotalPoints() != 2.0 {
		t.Fatalf("expected TotalPoints=2.0; got %v", ts.TotalPoints())
	}
}
