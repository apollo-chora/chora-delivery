// course_content_test.go — unit tests for the pg CourseContentRepo (CHO-1794).
//
// Stubs the Querier (shared with application_test.go) so the SQL surface +
// arg bindings + rls.ApplySession ordering are exercised without a live DB.
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
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
)

// sqlErrQuerier delegates to stubQuerier but fails Exec when the SQL contains
// failSubstr — lets a test target a specific statement's error branch (e.g. the
// soft-delete or upsert) while letting the rls SET LOCAL succeed.
type sqlErrQuerier struct {
	*stubQuerier
	failSubstr string
}

func (q *sqlErrQuerier) Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error) {
	if q.failSubstr != "" && strings.Contains(sql, q.failSubstr) {
		q.sqls = append(q.sqls, sql)
		q.args = append(q.args, args)
		return rls.CommandTag{}, errors.New("boom")
	}
	return q.stubQuerier.Exec(ctx, sql, args...)
}

// qTxRunner runs fn against an arbitrary Querier (for the selective-error cases).
type qTxRunner struct{ q pg.Querier }

func (r qTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q pg.Querier) error) error {
	return fn(ctx, r.q)
}

func mustCC(t *testing.T, kinds, refs, titles []string) *cc.CourseContent {
	t.Helper()
	c, err := cc.New(cc.NewParams{CourseID: courseID, TenantID: tenantID})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := range kinds {
		if _, err := c.AddItem(cc.AddItemParams{Kind: cc.Kind(kinds[i]), Ref: refs[i], Title: titles[i]}); err != nil {
			t.Fatalf("AddItem(%s): %v", kinds[i], err)
		}
	}
	return c
}

func TestCourseContentRepo_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewCourseContentRepo(nil)
	if _, err := r.Get(context.Background(), tenantID, courseID); err != pg.ErrNotImplemented {
		t.Fatalf("Get nil tx: want ErrNotImplemented, got %v", err)
	}
	c := mustCC(t, []string{"atom"}, []string{"01970000-0000-7000-8000-0000000000a1"}, []string{"x"})
	if err := r.Save(context.Background(), c); err != pg.ErrNotImplemented {
		t.Fatalf("Save nil tx: want ErrNotImplemented, got %v", err)
	}
}

func TestCourseContentRepo_Get_NotFoundOnZeroRows(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(string, ...any) (pg.Rows, error) { return &stubRows{}, nil }}
	r := pg.NewCourseContentRepo(&stubTxRunner{q: q})
	_, err := r.Get(context.Background(), tenantID, courseID)
	if err != cc.ErrNotFound {
		t.Fatalf("Get zero rows: want ErrNotFound, got %v", err)
	}
	// RLS applied first.
	if len(q.sqls) == 0 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("expected SET LOCAL first; sqls=%v", q.sqls)
	}
}

func TestCourseContentRepo_Get_BuildsOrderedAggregate(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	rowData := []struct {
		itemID, kind, ref, title string
		pos                      int
	}{
		{"01970000-0000-7000-8000-0000000000a1", "atom", "01970000-0000-7000-8000-0000000000b1", "Intro", 0},
		{"01970000-0000-7000-8000-0000000000a2", "video", "gs://chora-delivery-course-media-dev/tenants/t/courses/c/v.mp4", "Lecture", 1},
	}
	var fns []func(dest ...any) error
	for _, rd := range rowData {
		rd := rd
		fns = append(fns, func(dest ...any) error {
			*(dest[0].(*string)) = rd.itemID
			*(dest[1].(*string)) = courseID
			*(dest[2].(*string)) = tenantID
			*(dest[3].(*string)) = rd.kind
			*(dest[4].(*string)) = rd.ref
			*(dest[5].(*string)) = rd.title
			*(dest[6].(*int)) = rd.pos
			*(dest[7].(*time.Time)) = now
			*(dest[8].(*time.Time)) = now
			return nil
		})
	}
	q := &stubQuerier{rowsFn: func(string, ...any) (pg.Rows, error) { return &stubRows{rows: fns}, nil }}
	r := pg.NewCourseContentRepo(&stubTxRunner{q: q})

	got, err := r.Get(context.Background(), tenantID, courseID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CourseID != courseID || got.TenantID != tenantID || len(got.Items) != 2 {
		t.Fatalf("aggregate wrong: %+v", got)
	}
	if got.Items[0].Kind != cc.KindAtom || got.Items[1].Kind != cc.KindVideo {
		t.Fatalf("items not in order: %+v", got.Items)
	}
	if got.Items[1].Ref != rowData[1].ref {
		t.Fatalf("gs:// ref not preserved on load: %q", got.Items[1].Ref)
	}
}

func TestCourseContentRepo_Save_AppliesRLSThenReconciles(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseContentRepo(&stubTxRunner{q: q})

	c := mustCC(t,
		[]string{"atom", "video"},
		[]string{"01970000-0000-7000-8000-0000000000b1", "https://cdn/x.mp4"},
		[]string{"Intro", "Lecture"},
	)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Expect: SET LOCAL (rls) → soft-delete → 2 upserts = 4 statements.
	if len(q.sqls) != 4 {
		t.Fatalf("expected 4 statements (rls + soft-delete + 2 upserts); got %d: %v", len(q.sqls), q.sqls)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("RLS must be applied first; got %q", q.sqls[0])
	}
	if !strings.Contains(q.sqls[1], "SET deleted_at") || !strings.Contains(q.sqls[1], "NOT (item_id = ANY") {
		t.Fatalf("expected soft-delete reconcile second; got %q", q.sqls[1])
	}
	// soft-delete arg[3] is the []string of surviving item ids (both items).
	ids, ok := q.args[1][3].([]string)
	if !ok || len(ids) != 2 {
		t.Fatalf("soft-delete ids arg: want 2-element []string, got %T %v", q.args[1][3], q.args[1][3])
	}
	for _, idx := range []int{2, 3} {
		if !strings.Contains(q.sqls[idx], "INSERT INTO course_content_items") {
			t.Fatalf("statement %d should be an upsert; got %q", idx, q.sqls[idx])
		}
	}
	// First upsert binds kind=atom at arg[3].
	if kind, _ := q.args[2][3].(string); kind != "atom" {
		t.Fatalf("first upsert kind: want atom, got %v", q.args[2][3])
	}
}

func TestCourseContentRepo_Save_NilAggregate(t *testing.T) {
	t.Parallel()
	r := pg.NewCourseContentRepo(&stubTxRunner{q: &stubQuerier{}})
	if err := r.Save(context.Background(), nil); err == nil {
		t.Fatal("Save(nil): want error")
	}
}

func TestCourseContentRepo_Save_SoftDeleteError(t *testing.T) {
	t.Parallel()
	q := &sqlErrQuerier{stubQuerier: &stubQuerier{}, failSubstr: "SET deleted_at"}
	r := pg.NewCourseContentRepo(qTxRunner{q: q})
	c := mustCC(t, []string{"atom"}, []string{"01970000-0000-7000-8000-0000000000b1"}, []string{"x"})
	if err := r.Save(tracing.WithTenantID(context.Background(), tenantID), c); err == nil {
		t.Fatal("Save with soft-delete error: want error")
	}
}

func TestCourseContentRepo_Save_UpsertError(t *testing.T) {
	t.Parallel()
	q := &sqlErrQuerier{stubQuerier: &stubQuerier{}, failSubstr: "INSERT INTO course_content_items"}
	r := pg.NewCourseContentRepo(qTxRunner{q: q})
	c := mustCC(t, []string{"atom"}, []string{"01970000-0000-7000-8000-0000000000b1"}, []string{"x"})
	if err := r.Save(tracing.WithTenantID(context.Background(), tenantID), c); err == nil {
		t.Fatal("Save with upsert error: want error")
	}
}

func TestCourseContentRepo_Save_ZeroTimestampsDefaulted(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseContentRepo(&stubTxRunner{q: q})
	// Construct an item with zero CreatedAt/UpdatedAt to exercise the IsZero
	// defaulting branches in Save.
	c := &cc.CourseContent{
		CourseID: courseID, TenantID: tenantID,
		Items: []*cc.ContentItem{{
			ItemID: "01970000-0000-7000-8000-0000000000c1", CourseID: courseID,
			Kind: cc.KindAtom, Ref: "01970000-0000-7000-8000-0000000000b1", Title: "x", Position: 0,
		}},
	}
	if err := r.Save(tracing.WithTenantID(context.Background(), tenantID), c); err != nil {
		t.Fatalf("Save zero-ts: %v", err)
	}
	// Upsert's created_at (arg index 7) + updated_at (8) must be non-zero.
	upsert := q.args[len(q.args)-1]
	if ts, ok := upsert[7].(time.Time); !ok || ts.IsZero() {
		t.Fatalf("created_at should be defaulted non-zero, got %v", upsert[7])
	}
}

func TestCourseContentRepo_Get_ScanError(t *testing.T) {
	t.Parallel()
	fns := []func(dest ...any) error{func(dest ...any) error { return errors.New("scan boom") }}
	q := &stubQuerier{rowsFn: func(string, ...any) (pg.Rows, error) { return &stubRows{rows: fns}, nil }}
	r := pg.NewCourseContentRepo(&stubTxRunner{q: q})
	if _, err := r.Get(context.Background(), tenantID, courseID); err == nil {
		t.Fatal("Get with scan error: want error")
	}
}

func TestCourseContentRepo_Save_EmptyItemsSoftDeletesAll(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseContentRepo(&stubTxRunner{q: q})
	// An aggregate with all items removed → Save soft-deletes everything, no upserts.
	c, _ := cc.New(cc.NewParams{CourseID: courseID, TenantID: tenantID})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save empty: %v", err)
	}
	if len(q.sqls) != 2 { // rls + soft-delete only
		t.Fatalf("expected 2 statements (rls + soft-delete); got %d: %v", len(q.sqls), q.sqls)
	}
	ids, ok := q.args[1][3].([]string)
	if !ok || len(ids) != 0 {
		t.Fatalf("empty Save ids arg: want empty []string, got %T %v", q.args[1][3], q.args[1][3])
	}
}
