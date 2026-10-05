// module_progress_resolver_unit_test.go — unit tests for pg.ProgressResolver.
//
// Exercises the enrolment-gated completion-target resolution (WS-A W7) against
// a stub Querier, no live DB. Guarantees:
//  1. rls.ApplySession runs BEFORE the resolution query (the tenant GUC is a
//     hard requirement on the FORCE-RLS tables);
//  2. SQL shape: DISTINCT (content_item_id, module_id, course_id) over
//     course_content_items ⋈ course_module_items ⋈ course_modules ⋈
//     course_enrollments — the enrolment join is the pollution guard;
//  3. bind-arg order matches $1 tenant, $2 gcid, $3 kind, $4 ref;
//  4. nil-tx returns ErrNotImplemented (fail-loud);
//  5. empty RLS scope returns ErrProgressMissingScope;
//  6. empty results are an empty slice, not an error.
//
// Live RLS isolation is covered by the integration-tagged tests.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
)

// resolvFillTargetRow writes ResolveEnrolledTargets' 3 destinations
// (content_item_id, module_id, course_id).
func resolvFillTargetRow(dest []any, contentItemID, moduleID, courseID string) error {
	if len(dest) != 3 {
		return errors.New("ResolveEnrolledTargets scan: expected 3 destinations")
	}
	*(dest[0].(*string)) = contentItemID
	*(dest[1].(*string)) = moduleID
	*(dest[2].(*string)) = courseID
	return nil
}

// -----------------------------------------------------------------------------
// Nil-TxRunner — fail-loud
// -----------------------------------------------------------------------------

func TestProgressResolver_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewProgressResolver(nil)
	_, err := r.ResolveEnrolledTargets(context.Background(), tenantID, gcid, "atom", "ref-1")
	if !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Input validation — RLS scope sentinel
// -----------------------------------------------------------------------------

func TestProgressResolver_RejectsMissingScope(t *testing.T) {
	t.Parallel()
	r := pg.NewProgressResolver(&stubTxRunner{q: &stubQuerier{}})
	ctx := context.Background()

	if _, err := r.ResolveEnrolledTargets(ctx, "", gcid, "atom", "ref-1"); !errors.Is(err, pg.ErrProgressMissingScope) {
		t.Fatalf("empty tenant: expected ErrProgressMissingScope; got %v", err)
	}
	if _, err := r.ResolveEnrolledTargets(ctx, tenantID, " ", "atom", "ref-1"); !errors.Is(err, pg.ErrProgressMissingScope) {
		t.Fatalf("empty gcid: expected ErrProgressMissingScope; got %v", err)
	}
	if _, err := r.ResolveEnrolledTargets(ctx, tenantID, gcid, "", "ref-1"); !errors.Is(err, pg.ErrProgressMissingScope) {
		t.Fatalf("empty kind: expected ErrProgressMissingScope; got %v", err)
	}
	if _, err := r.ResolveEnrolledTargets(ctx, tenantID, gcid, "atom", "  "); !errors.Is(err, pg.ErrProgressMissingScope) {
		t.Fatalf("empty ref: expected ErrProgressMissingScope; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ResolveEnrolledTargets — the four-way intra-chora_delivery join
// -----------------------------------------------------------------------------

func TestProgressResolver_ResolveEnrolledTargets_ReturnsTargets(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return resolvFillTargetRow(dest, "item-1", "mod-1", courseID) },
				func(dest ...any) error { return resolvFillTargetRow(dest, "item-2", "mod-2", courseID) },
			}}, nil
		},
	}
	r := pg.NewProgressResolver(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ResolveEnrolledTargets(ctx, tenantID, gcid, "atom", "ref-1")
	if err != nil {
		t.Fatalf("ResolveEnrolledTargets: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 targets; got %d", len(out))
	}
	if out[0].ContentItemID != "item-1" || out[0].ModuleID != "mod-1" || out[0].CourseID != courseID {
		t.Fatalf("target 0 wrong: %+v", out[0])
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	sel := q.sqls[1]
	for _, frag := range []string{
		"SELECT DISTINCT cmi.content_item_id, cmi.module_id, cm.course_id",
		"FROM course_content_items cci",
		"JOIN course_module_items",
		"JOIN course_modules",
		"JOIN course_enrollments",
		"WHERE cci.tenant_id = $1",
	} {
		if !strings.Contains(sel, frag) {
			t.Fatalf("resolution SELECT must contain %q; got %q", frag, sel)
		}
	}
	if len(q.args) < 2 || len(q.args[1]) != 4 ||
		q.args[1][0] != tenantID || q.args[1][1] != gcid || q.args[1][2] != "atom" || q.args[1][3] != "ref-1" {
		t.Fatalf("resolution args wrong: %v", q.args[1])
	}
}

func TestProgressResolver_ResolveEnrolledTargets_Empty_ReturnsEmptySlice(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{} // no rowsFn → empty stubRows (zero rows)
	r := pg.NewProgressResolver(&stubTxRunner{q: q})
	ctx := context.Background()

	out, err := r.ResolveEnrolledTargets(ctx, tenantID, gcid, "atom", "ref-1")
	if err != nil {
		t.Fatalf("empty resolution must not error; got %v", err)
	}
	if out == nil || len(out) != 0 {
		t.Fatalf("expected a non-nil empty slice; got %#v", out)
	}
}

func TestProgressResolver_ResolveEnrolledTargets_QueryError_Returned(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("conn closed")
		},
	}
	r := pg.NewProgressResolver(&stubTxRunner{q: q})
	ctx := context.Background()

	_, err := r.ResolveEnrolledTargets(ctx, tenantID, gcid, "atom", "ref-1")
	if err == nil || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("Query error must propagate; got %v", err)
	}
}

func TestProgressResolver_ResolveEnrolledTargets_RowScanError_Returned(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewProgressResolver(&stubTxRunner{q: q})
	ctx := context.Background()

	_, err := r.ResolveEnrolledTargets(ctx, tenantID, gcid, "atom", "ref-1")
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("row-scan error must propagate; got %v", err)
	}
}
