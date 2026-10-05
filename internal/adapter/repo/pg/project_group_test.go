// project_group_test.go — unit tests for pg.ProjectGroupRepo (R+ durability
// sweep).
//
// Mirrors exam_test.go: stubs the Querier (shared stubQuerier / stubTxRunner /
// stubRow / stubRows + tenantID / courseID consts from application_test.go) so
// the SQL surface + RLS contract are exercised without a live DB. The
// ProjectGroup aggregate is persisted as a JSONB snapshot (exam.go pattern),
// so reads stub a single `data []byte` column carrying the marshalled
// aggregate.
//
// Guarantees:
//  1. rls.ApplySession runs BEFORE the data query (every method).
//  2. SQL matches shape (UPSERT ON CONFLICT (id), SELECT data ... deleted_at
//     IS NULL, ORDER BY id; ListByCourse adds course_id = $2).
//  3. Nil-tx is fail-loud on writes (Save / ListByTenant / ListByCourse →
//     ErrNotImplemented) and degrades on point reads (Get → ok=false).
//  4. JSONB round-trip rehydrates the aggregate.
package pg_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	pgdomain "github.com/apollo-chora/chora-delivery/internal/domain/project_group"
)

func newProjectGroup(id string, state pgdomain.ProjectState) *pgdomain.ProjectGroup {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &pgdomain.ProjectGroup{
		ID:        id,
		TenantID:  tenantID,
		CourseID:  courseID,
		Name:      "Group " + id,
		Members:   []pgdomain.Member{{GCID: gcid, Role: pgdomain.RoleLeader}},
		State:     state,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func projectGroupJSON(t *testing.T, g *pgdomain.ProjectGroup) []byte {
	t.Helper()
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal project_group: %v", err)
	}
	return b
}

func TestProjectGroupRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewProjectGroupRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newProjectGroup("01970000-0000-7000-9999-f00000000001", pgdomain.StateForming)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenant(ctx, tenantID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByCourse(ctx, tenantID, courseID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByCourse: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "01970000-0000-7000-9999-f00000000001"); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

func TestProjectGroupRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewProjectGroupRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newProjectGroup("01970000-0000-7000-9999-f00000000002", pgdomain.StateActive)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO project_groups") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert into project_groups; got %q", last)
	}
}

func TestProjectGroupRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	want := newProjectGroup("01970000-0000-7000-9999-f00000000003", pgdomain.StateSubmitted)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = projectGroupJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewProjectGroupRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	g, ok, _ := r.Get(ctx, want.ID)
	if !ok || g == nil {
		t.Fatalf("Get: expected hit")
	}
	if g.ID != want.ID || g.State != pgdomain.StateSubmitted || g.CourseID != courseID {
		t.Fatalf("Get: rehydration mismatch; got %+v", g)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM project_groups") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM project_groups; got %q", last)
	}
}

func TestProjectGroupRepo_Get_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewProjectGroupRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if g, ok, _ := r.Get(ctx, "nope"); ok || g != nil {
		t.Fatalf("Get: expected (nil, false); got (%+v, %v)", g, ok)
	}
}

func TestProjectGroupRepo_ListByTenant_TwoRows(t *testing.T) {
	t.Parallel()
	a := newProjectGroup("01970000-0000-7000-9999-f00000000aa", pgdomain.StateForming)
	b := newProjectGroup("01970000-0000-7000-9999-f00000000bb", pgdomain.StateGraded)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = projectGroupJSON(t, a); return nil },
				func(dest ...any) error { *(dest[0].(*[]byte)) = projectGroupJSON(t, b); return nil },
			}}, nil
		},
	}
	r := pg.NewProjectGroupRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 rows; got %d", len(out))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "deleted_at IS NULL") || !strings.Contains(last, "ORDER BY id") {
		t.Fatalf("expected tenant-scoped, soft-delete-filtered, ordered list; got %q", last)
	}
}

func TestProjectGroupRepo_ListByCourse_FiltersOnCourse(t *testing.T) {
	t.Parallel()
	a := newProjectGroup("01970000-0000-7000-9999-f00000000cc", pgdomain.StateActive)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = projectGroupJSON(t, a); return nil },
			}}, nil
		},
	}
	r := pg.NewProjectGroupRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByCourse(ctx, tenantID, courseID)
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(out) != 1 || out[0].CourseID != courseID {
		t.Fatalf("expected 1 course-scoped row; got %+v", out)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	// The bound args must carry tenant_id ($1) then course_id ($2).
	lastArgs := q.args[len(q.args)-1]
	if len(lastArgs) != 2 || lastArgs[0] != tenantID || lastArgs[1] != courseID {
		t.Fatalf("expected ($1=tenant, $2=course) bind args; got %#v", lastArgs)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "course_id = $2") || !strings.Contains(last, "deleted_at IS NULL") || !strings.Contains(last, "ORDER BY id") {
		t.Fatalf("expected tenant+course-scoped, soft-delete-filtered, ordered list; got %q", last)
	}
}
