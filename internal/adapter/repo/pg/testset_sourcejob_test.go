// testset_sourcejob_test.go — Lane 1c W4 (CHO-1703 / ADR-180 D10) pg-adapter
// tests for test_sets.source_job_id (migration 0028).
//
// TDD RED phase: written FIRST. Reuses the stubQuerier/stubTxRunner fixtures
// from application_test.go (same pg_test package) so the SQL surface is
// exercised without a live DB; the SET LOCAL chora.tenant_id RLS contract is
// asserted via stub call ordering, mirroring testset_test.go.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const tsSourceJobID = "01985e7f-0000-7000-8000-00000000aaaa"

// -----------------------------------------------------------------------------
// Save — source_job_id rides the parent UPSERT
// -----------------------------------------------------------------------------

func TestTestSetRepo_Save_BindsSourceJobID(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:    tenantID,
		AuthorGCID:  tsAuthorGCID,
		Title:       "Batch-assembled",
		SourceJobID: tsSourceJobID,
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, ts); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The parent UPSERT must carry the source_job_id column AND preserve any
	// pre-existing non-null value (immutable provenance — COALESCE keeps the
	// stored value when set).
	found := false
	for i, s := range q.sqls {
		if strings.Contains(s, "INSERT INTO test_sets") {
			found = true
			if !strings.Contains(s, "source_job_id") {
				t.Fatalf("UPSERT must include source_job_id column; got %q", s)
			}
			if !strings.Contains(s, "COALESCE(test_sets.source_job_id, EXCLUDED.source_job_id)") {
				t.Fatalf("UPSERT must preserve existing source_job_id via COALESCE; got %q", s)
			}
			// The bind list must carry the job id value.
			foundArg := false
			for _, a := range q.args[i] {
				if v, ok := a.(string); ok && v == tsSourceJobID {
					foundArg = true
				}
			}
			if !foundArg {
				t.Fatalf("UPSERT args must bind %q; got %v", tsSourceJobID, q.args[i])
			}
		}
	}
	if !found {
		t.Fatalf("expected UPSERT INTO test_sets; got SQLs: %v", q.sqls)
	}
}

func TestTestSetRepo_Save_NilSourceJobID_BindsNULL(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	ts, _ := domain.NewTestSet(domain.NewTestSetInput{
		TenantID: tenantID, AuthorGCID: tsAuthorGCID, Title: "Hand-authored",
	})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, ts); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for i, s := range q.sqls {
		if strings.Contains(s, "INSERT INTO test_sets") {
			// No arg may equal a non-empty source job id; the column binds nil.
			for _, a := range q.args[i] {
				if v, ok := a.(string); ok && v == tsSourceJobID {
					t.Fatalf("nil SourceJobID must not bind a job id; args %v", q.args[i])
				}
			}
		}
	}
}

// -----------------------------------------------------------------------------
// GetBySourceJobID — RLS-scoped exact-match header lookup
// -----------------------------------------------------------------------------

func TestTestSetRepo_GetBySourceJobID_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewTestSetRepo(nil)
	_, _, err := r.GetBySourceJobID(context.Background(), tenantID, tsSourceJobID)
	if !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented; got %v", err)
	}
}

func TestTestSetRepo_GetBySourceJobID_AppliesRLSThenSelects(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows")
			}}
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	_, ok, err := r.GetBySourceJobID(context.Background(), tenantID, tsSourceJobID)
	if err != nil {
		t.Fatalf("GetBySourceJobID: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false on no-rows")
	}
	if len(q.sqls) < 2 {
		t.Fatalf("expected SET LOCAL + SELECT; got %d SQLs", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	sel := q.sqls[len(q.sqls)-1]
	if !strings.Contains(sel, "FROM test_sets") || !strings.Contains(sel, "source_job_id = $1") {
		t.Fatalf("SELECT must filter on source_job_id; got %q", sel)
	}
	if !strings.Contains(sel, "deleted_at IS NULL") {
		t.Fatalf("SELECT must be soft-delete-aware; got %q", sel)
	}
}

func TestTestSetRepo_GetBySourceJobID_ScansHeader(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				// Column order mirrors testSetCols + trailing source_job_id:
				// test_set_id, tenant_id, author_gcid, title, description,
				// state, created_at, updated_at, published_at, source_job_id.
				*(dest[0].(*string)) = "01985e7f-9999-7000-8000-000000000001"
				*(dest[1].(*string)) = tenantID
				*(dest[2].(*string)) = tsAuthorGCID
				*(dest[3].(*string)) = "Assembled"
				// dest[4] description *string stays nil
				*(dest[5].(*string)) = "DRAFT"
				*(dest[6].(*time.Time)) = now
				*(dest[7].(*time.Time)) = now
				// dest[8] published_at **time.Time stays nil
				sj := tsSourceJobID
				*(dest[9].(**string)) = &sj
				return nil
			}}
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	ts, ok, err := r.GetBySourceJobID(context.Background(), tenantID, tsSourceJobID)
	if err != nil {
		t.Fatalf("GetBySourceJobID: %v", err)
	}
	if !ok || ts == nil {
		t.Fatal("expected ok=true with aggregate")
	}
	if ts.SourceJobID == nil || *ts.SourceJobID != tsSourceJobID {
		t.Fatalf("SourceJobID round-trip: got %v", ts.SourceJobID)
	}
	if ts.Title != "Assembled" || ts.State != domain.TestSetStateDraft {
		t.Fatalf("header scan mismatch: %+v", ts)
	}
}

// -----------------------------------------------------------------------------
// Get — header scan now includes source_job_id
// -----------------------------------------------------------------------------

func TestTestSetRepo_Get_ScansSourceJobID(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*string)) = "01985e7f-9999-7000-8000-000000000002"
				*(dest[1].(*string)) = tenantID
				*(dest[2].(*string)) = tsAuthorGCID
				*(dest[3].(*string)) = "With provenance"
				*(dest[5].(*string)) = "DRAFT"
				*(dest[6].(*time.Time)) = now
				*(dest[7].(*time.Time)) = now
				sj := tsSourceJobID
				*(dest[9].(**string)) = &sj
				return nil
			}}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil // no children
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	ts, ok, err := r.Get(context.Background(), tenantID, "01985e7f-9999-7000-8000-000000000002")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || ts == nil {
		t.Fatal("expected ok=true")
	}
	if ts.SourceJobID == nil || *ts.SourceJobID != tsSourceJobID {
		t.Fatalf("Get must scan source_job_id; got %v", ts.SourceJobID)
	}
}

// -----------------------------------------------------------------------------
// List — exact-match filter
// -----------------------------------------------------------------------------

func TestTestSetRepo_List_SourceJobIDFilter_InSQLAndBinds(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	_, _, err := r.List(context.Background(), tenantID, domain.TestSetListFilter{
		SourceJobID: tsSourceJobID,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var listSQL string
	var listArgs []any
	for i, s := range q.sqls {
		if strings.Contains(s, "FROM test_sets ts") {
			listSQL = s
			listArgs = q.args[i]
		}
	}
	if listSQL == "" {
		t.Fatalf("expected list SELECT; got %v", q.sqls)
	}
	if !strings.Contains(listSQL, "source_job_id") {
		t.Fatalf("list SQL must filter + project source_job_id; got %q", listSQL)
	}
	found := false
	for _, a := range listArgs {
		if v, ok := a.(string); ok && v == tsSourceJobID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list args must bind the source_job_id needle; got %v", listArgs)
	}
}

func TestTestSetRepo_List_NoSourceJobIDFilter_BindsNULLNeedle(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	if _, _, err := r.List(context.Background(), tenantID, domain.TestSetListFilter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	for i, s := range q.sqls {
		if strings.Contains(s, "FROM test_sets ts") {
			// The empty filter must NOT bind ''::uuid (22P02 risk) — the
			// source_job_id needle ($6, the 6th bind) carries SQL NULL (nil)
			// so `$6::uuid IS NULL` disables the term. (The title needle $4
			// legitimately binds "" — its guard is `$4::text = ''`.)
			if len(q.args[i]) < 6 {
				t.Fatalf("list SELECT must bind 6 args; got %d (%v)", len(q.args[i]), q.args[i])
			}
			if q.args[i][5] != nil {
				t.Fatalf("empty source_job_id must bind nil at $6; got %v", q.args[i][5])
			}
		}
	}
}

func TestTestSetRepo_List_ScansSourceJobIDColumn(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					// list row: 9 header cols + source_job_id + 2 counters.
					*(dest[0].(*string)) = "01985e7f-9999-7000-8000-000000000003"
					*(dest[1].(*string)) = tenantID
					*(dest[2].(*string)) = tsAuthorGCID
					*(dest[3].(*string)) = "Listed"
					*(dest[5].(*string)) = "DRAFT"
					*(dest[6].(*time.Time)) = now
					*(dest[7].(*time.Time)) = now
					sj := tsSourceJobID
					*(dest[9].(**string)) = &sj
					*(dest[10].(*int)) = 3
					*(dest[11].(*float64)) = 30
					return nil
				},
			}}, nil
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewTestSetRepo(tx)

	items, _, err := r.List(context.Background(), tenantID, domain.TestSetListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 row; got %d", len(items))
	}
	if items[0].SourceJobID == nil || *items[0].SourceJobID != tsSourceJobID {
		t.Fatalf("list scan must hydrate SourceJobID; got %v", items[0].SourceJobID)
	}
	if items[0].QuestionCount() != 3 {
		t.Fatalf("summary counters must still hydrate after the column add; got %d", items[0].QuestionCount())
	}
}
