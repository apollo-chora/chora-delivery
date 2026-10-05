// testset_extra_test.go — extra pg.TestSetRepo unit tests: finish partial
// statement coverage of testset.go.
//
// Extends testset_test.go + testset_sourcejob_test.go (same pg_test package)
// — reuses the shared stubQuerier / stubTxRunner / stubRow / stubRows fixtures
// + the tsAuthorGCID / tsQuestionID / tsSourceJobID consts.
//
// New helpers/fillers/consts in this file are prefixed `tsx` to keep the
// package-level pg_test namespace unique across parallel agents.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// tsxTestSet builds a fresh TestSet with the author/title used across tests.
func tsxTestSet(t *testing.T) *domain.TestSet {
	t.Helper()
	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:   tenantID,
		AuthorGCID: tsAuthorGCID,
		Title:      "Phyllis Math Test Set 1",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	return ts
}

// tsxHeaderRow fills a Scan dest shaped like testSetCols (10 cols) — the
// description + published_at + source_job_id pointers are caller-chosen.
func tsxHeaderRow(id string, description *string, publishedAt *time.Time, sourceJobID *string) func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) != 10 {
			return errors.New("scanTestSetHeader: expected 10 destinations")
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		*(dest[0].(*string)) = id
		*(dest[1].(*string)) = tenantID
		*(dest[2].(*string)) = tsAuthorGCID
		*(dest[3].(*string)) = "Phyllis Test"
		*(dest[4].(**string)) = description
		*(dest[5].(*string)) = "DRAFT"
		*(dest[6].(*time.Time)) = now
		*(dest[7].(*time.Time)) = now
		*(dest[8].(**time.Time)) = publishedAt
		*(dest[9].(**string)) = sourceJobID
		return nil
	}
}

// tsxQuestionRow fills a Scan dest shaped like testSetQuestionCols (10 cols)
// with caller-chosen snapshot pointers.
func tsxQuestionRow(id string, payloadSnapshot *string, snapshotAt *time.Time) func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) != 10 {
			return errors.New("scanTestSetQuestionRow: expected 10 destinations")
		}
		*(dest[0].(*string)) = id
		*(dest[1].(*string)) = "ts-1"
		*(dest[2].(*string)) = tsQuestionID
		*(dest[3].(*string)) = tsQuestionID
		*(dest[4].(*string)) = "mcq"
		*(dest[5].(*int)) = 1
		*(dest[6].(*float64)) = 2.0
		*(dest[7].(*time.Time)) = time.Now().UTC().Truncate(time.Microsecond)
		*(dest[8].(**string)) = payloadSnapshot
		*(dest[9].(**time.Time)) = snapshotAt
		return nil
	}
}

// -----------------------------------------------------------------------------
// Save — parent/child exec-error wrap branches
// -----------------------------------------------------------------------------

func TestTestSetRepo_Save_ParentUpsertError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO test_sets") {
			return errors.New("parent write failed")
		}
		return nil
	}}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err := r.Save(ctx, tsxTestSet(t))
	if err == nil {
		t.Fatalf("expected the parent UPSERT error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: upsert test_set") {
		t.Fatalf("parent error must be wrapped with context; got %v", err)
	}
}

func TestTestSetRepo_Save_ChildUpsertError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO test_set_questions") {
			return errors.New("child write failed")
		}
		return nil
	}}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	ts := tsxTestSet(t)
	if _, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: tsQuestionID, QuestionID: tsQuestionID, QuestionType: "mcq", Points: 2.0,
	}); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err := r.Save(ctx, ts)
	if err == nil {
		t.Fatalf("expected the child UPSERT error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: upsert test_set_question") {
		t.Fatalf("child error must be wrapped with context; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// SaveQuestionRemoval — both Exec error wraps
// -----------------------------------------------------------------------------

func TestTestSetRepo_SaveQuestionRemoval_SoftDeleteExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "UPDATE test_set_questions") {
			return errors.New("soft-delete failed")
		}
		return nil
	}}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err := r.SaveQuestionRemoval(ctx, tsxTestSet(t), "tsq-1")
	if err == nil {
		t.Fatalf("expected the soft-delete error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: soft-delete test_set_question") {
		t.Fatalf("soft-delete error must be wrapped; got %v", err)
	}
}

func TestTestSetRepo_SaveQuestionRemoval_ParentBumpExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "UPDATE test_sets SET updated_at") {
			return errors.New("bump failed")
		}
		return nil
	}}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err := r.SaveQuestionRemoval(ctx, tsxTestSet(t), "tsq-1")
	if err == nil {
		t.Fatalf("expected the parent-bump error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: bump test_set updated_at") {
		t.Fatalf("parent-bump error must be wrapped; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Get — happy path with description/published_at + snapshot-bearing children,
// plus the child query/scan error wraps
// -----------------------------------------------------------------------------

func TestTestSetRepo_Get_HappyPath_WithSnapshots(t *testing.T) {
	t.Parallel()
	desc := "A test set"
	published := time.Now().UTC().Add(-time.Hour)
	sj := tsSourceJobID
	snap := `{"stem":"What gas?"}`
	snapAt := time.Now().UTC().Add(-time.Minute)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: tsxHeaderRow("ts-1", &desc, &published, &sj)}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				tsxQuestionRow("q-1", &snap, &snapAt),
			}}, nil
		},
	}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	ts, ok, err := r.Get(ctx, tenantID, "ts-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if ts.Description != desc {
		t.Fatalf("description must hydrate; got %q", ts.Description)
	}
	if ts.PublishedAt == nil || !ts.PublishedAt.Equal(published.UTC()) {
		t.Fatalf("published_at must hydrate; got %v", ts.PublishedAt)
	}
	if ts.SourceJobID == nil || *ts.SourceJobID != sj {
		t.Fatalf("source_job_id must hydrate; got %v", ts.SourceJobID)
	}
	if ts.QuestionCount() != 1 {
		t.Fatalf("expected 1 question; got %d", ts.QuestionCount())
	}
	qrow := ts.Questions()[0]
	if qrow.PayloadSnapshot != snap {
		t.Fatalf("payload_snapshot must hydrate; got %q", qrow.PayloadSnapshot)
	}
	if qrow.SnapshotAt == nil || !qrow.SnapshotAt.Equal(snapAt.UTC()) {
		t.Fatalf("snapshot_at must hydrate; got %v", qrow.SnapshotAt)
	}
}

func TestTestSetRepo_Get_ChildQueryError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: tsxHeaderRow("ts-1", nil, nil, nil)}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("child list failed")
		},
	}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, _, err := r.Get(ctx, tenantID, "ts-1")
	if err == nil {
		t.Fatalf("expected the child query error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: list test_set_questions") {
		t.Fatalf("child query error must be wrapped; got %v", err)
	}
}

func TestTestSetRepo_Get_ChildScanError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: tsxHeaderRow("ts-1", nil, nil, nil)}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, _, err := r.Get(ctx, tenantID, "ts-1")
	if err == nil {
		t.Fatalf("expected the child scan error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: scan test_set_question") {
		t.Fatalf("child scan error must be wrapped; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// GetBySourceJobID — RLS-session failure with an empty tenant argument
// -----------------------------------------------------------------------------

func TestTestSetRepo_GetBySourceJobID_EmptyTenant_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	// tracing.WithTenantID(ctx, "") yields an empty tenant → ApplySession must
	// refuse (an unscoped read would leak across tenants).
	if _, _, err := r.GetBySourceJobID(context.Background(), "", tsSourceJobID); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// List — states-family / caps / error wraps / counter row with desc+publish
// -----------------------------------------------------------------------------

func TestTestSetRepo_List_StatesFilterAndCapsAndBinds(t *testing.T) {
	t.Parallel()
	var listSQL string
	var listArgs []any
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		if strings.Contains(sql, "FROM test_sets ts") {
			listSQL = sql
			listArgs = args
		}
		return &stubRows{}, nil
	}}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	// 3 states > the floor of 2 → exercises max(a>b); PageSize 250 → capped at
	// 100; a non-nil author list + title needle are bound verbatim.
	_, _, err := r.List(context.Background(), tenantID, domain.TestSetListFilter{
		States: []domain.TestSetState{
			domain.TestSetStateDraft,
			domain.TestSetStatePublished,
			domain.TestSetStateArchived,
		},
		AuthorGCIDs: []string{tsAuthorGCID},
		TitleQuery:  "math",
		PageSize:    250,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if listSQL == "" {
		t.Fatalf("expected a list SELECT; got %v", q.sqls)
	}
	if len(listArgs) != 6 {
		t.Fatalf("list SELECT must bind 6 args; got %d (%v)", len(listArgs), listArgs)
	}
	// $2 states (3), $3 authors (1), $4 title needle, $5 page_size capped 100.
	if states, ok := listArgs[1].([]string); !ok || len(states) != 3 || states[2] != string(domain.TestSetStateArchived) {
		t.Fatalf("states bind wrong: %v", listArgs[1])
	}
	if authors, ok := listArgs[2].([]string); !ok || len(authors) != 1 || authors[0] != tsAuthorGCID {
		t.Fatalf("authors bind wrong: %v", listArgs[2])
	}
	if needle, _ := listArgs[3].(string); needle != "math" {
		t.Fatalf("title needle bind wrong: %v", listArgs[3])
	}
	if n, _ := listArgs[4].(int); n != 100 {
		t.Fatalf("PageSize 250 must clamp to 100; got %v", listArgs[4])
	}
}

func TestTestSetRepo_List_DefaultStatesAndAuthors(t *testing.T) {
	t.Parallel()
	var listArgs []any
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		if strings.Contains(sql, "FROM test_sets ts") {
			listArgs = args
		}
		return &stubRows{}, nil
	}}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	if _, _, err := r.List(context.Background(), tenantID, domain.TestSetListFilter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	// Empty States → contract default {DRAFT, PUBLISHED}; nil authors → [].
	if states, ok := listArgs[1].([]string); !ok || len(states) != 2 ||
		states[0] != string(domain.TestSetStateDraft) ||
		states[1] != string(domain.TestSetStatePublished) {
		t.Fatalf("default state-set bind wrong: %v", listArgs[1])
	}
	if authors, ok := listArgs[2].([]string); !ok || authors == nil || len(authors) != 0 {
		t.Fatalf("nil authors must bind an empty slice; got %v", listArgs[2])
	}
}

func TestTestSetRepo_List_QueryError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, errors.New("list query failed")
	}}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	_, _, err := r.List(context.Background(), tenantID, domain.TestSetListFilter{})
	if err == nil {
		t.Fatalf("expected the query error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: select test_sets list") {
		t.Fatalf("query error must be wrapped; got %v", err)
	}
}

func TestTestSetRepo_List_ScanError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	_, _, err := r.List(context.Background(), tenantID, domain.TestSetListFilter{})
	if err == nil {
		t.Fatalf("expected the row-scan error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: scan test_set row") {
		t.Fatalf("scan error must be wrapped; got %v", err)
	}
}

func TestTestSetRepo_List_RowWithDescriptionAndPublishHydratesCounts(t *testing.T) {
	t.Parallel()
	desc := "Listed with description"
	published := time.Now().UTC().Add(-2 * time.Hour)
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error {
				if len(dest) != 12 {
					return errors.New("list row: expected 12 destinations")
				}
				now := time.Now().UTC().Truncate(time.Microsecond)
				*(dest[0].(*string)) = "01985e7f-9999-7000-8000-00000000abcd"
				*(dest[1].(*string)) = tenantID
				*(dest[2].(*string)) = tsAuthorGCID
				*(dest[3].(*string)) = "Phyllis Test"
				*(dest[4].(**string)) = &desc
				*(dest[5].(*string)) = "DRAFT"
				*(dest[6].(*time.Time)) = now
				*(dest[7].(*time.Time)) = now
				*(dest[8].(**time.Time)) = &published
				*(dest[9].(**string)) = nil
				*(dest[10].(*int)) = 4
				*(dest[11].(*float64)) = 17.5
				return nil
			},
		}}, nil
	}}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	items, token, err := r.List(context.Background(), tenantID, domain.TestSetListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item; got %d", len(items))
	}
	if items[0].Description != desc || items[0].PublishedAt == nil {
		t.Fatalf("header optionals must hydrate; got %+v", items[0])
	}
	if items[0].QuestionCount() != 4 || items[0].TotalPoints() != 17.5 {
		t.Fatalf("summary counters must hydrate; got %d / %v", items[0].QuestionCount(), items[0].TotalPoints())
	}
	if token != "" {
		t.Fatalf("v1 page token must be empty; got %q", token)
	}
}

// -----------------------------------------------------------------------------
// RLS-session failures + rows.Err() iteration guards
// -----------------------------------------------------------------------------

func TestTestSetRepo_Get_EmptyTenant_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	// WithTenantID(ctx, "") yields an empty tenant → ApplySession refuses.
	if _, _, err := r.Get(context.Background(), "", "ts-1"); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestTestSetRepo_Get_ChildIterError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: tsxHeaderRow("ts-1", nil, nil, nil)}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &tsxErrRows{stubRows: &stubRows{rows: []func(dest ...any) error{
				tsxQuestionRow("q-1", nil, nil),
			}}}, nil
		},
	}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, _, err := r.Get(ctx, tenantID, "ts-1")
	if err == nil {
		t.Fatalf("expected the rows.Err() failure to propagate")
	}
	if !strings.Contains(err.Error(), "pg: iter test_set_questions") {
		t.Fatalf("iter error must be wrapped; got %v", err)
	}
}

func TestTestSetRepo_List_ApplySessionExecFails(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "SET LOCAL chora.tenant_id") {
			return errors.New("rlx blocked")
		}
		return nil
	}}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	_, _, err := r.List(context.Background(), tenantID, domain.TestSetListFilter{})
	if err == nil {
		t.Fatalf("expected the SET LOCAL failure to propagate")
	}
	if !strings.Contains(err.Error(), "rlx blocked") {
		t.Fatalf("expected the underlying SET LOCAL error; got %v", err)
	}
}

func TestTestSetRepo_List_RowsIteratorError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &tsxErrRows{stubRows: &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error {
				if len(dest) != 12 {
					return errors.New("list row: expected 12 destinations")
				}
				now := time.Now().UTC().Truncate(time.Microsecond)
				*(dest[0].(*string)) = "01985e7f-9999-7000-8000-00000000a0a1"
				*(dest[1].(*string)) = tenantID
				*(dest[2].(*string)) = tsAuthorGCID
				*(dest[3].(*string)) = "Phyllis Test"
				*(dest[5].(*string)) = "DRAFT"
				*(dest[6].(*time.Time)) = now
				*(dest[7].(*time.Time)) = now
				*(dest[10].(*int)) = 1
				*(dest[11].(*float64)) = 2
				return nil
			},
		}}}, nil
	}}
	r := pg.NewTestSetRepo(&stubTxRunner{q: q})
	_, _, err := r.List(context.Background(), tenantID, domain.TestSetListFilter{})
	if err == nil {
		t.Fatalf("expected the rows.Err() failure to propagate")
	}
	if !strings.Contains(err.Error(), "pg: iter test_sets list") {
		t.Fatalf("iter error must be wrapped; got %v", err)
	}
}

// tsxErrRows is a stubRows whose Err() reports an iteration failure —
// exercises the `rows.Err()` guard bodies without a live DB.
type tsxErrRows struct {
	*stubRows
}

func (r *tsxErrRows) Err() error { return errors.New("iteration failed") }
