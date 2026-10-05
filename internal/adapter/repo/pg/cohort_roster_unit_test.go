// cohort_roster_unit_test.go — unit tests for pg.CohortRosterRepo.
//
// Exercises the cohort authz roster reads (CHO-2153 / ADR-234) against a stub
// Querier, no live DB. Guarantees:
//  1. rls.ApplySession runs BEFORE the EXISTS probe (the GUC is applied on
//     every path — on FORCE-RLS tables a missing GUC silently matches zero rows);
//  2. SQL shape: EXISTS over offerings ⋈ course_enrollments (offering) and
//     EXISTS over bookings (class);
//  3. bind-arg order matches $1 tenant, $2 offering/class, $3 learner;
//  4. nil-tx returns ErrNotImplemented (fail-loud);
//  5. a scan error is RETURNED — never folded into (false, nil): a denial and
//     a database outage are different things.
//
// Live RLS isolation is covered by cohort_authz_integration_test.go (build tag
// `integration`).
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
)

// rosterExistsRow stubs the single-boolean EXISTS scan used by both methods.
func rosterExistsRow(dest []any, value bool) error {
	if len(dest) != 1 {
		return errors.New("EXISTS scan: expected 1 destination")
	}
	*(dest[0].(*bool)) = value
	return nil
}

// -----------------------------------------------------------------------------
// Nil-TxRunner — fail-loud
// -----------------------------------------------------------------------------

func TestCohortRosterRepo_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewCohortRosterRepo(nil)
	ctx := context.Background()

	if _, err := r.EnrolledInOffering(ctx, tenantID, "offering-1", gcid); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("EnrolledInOffering: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.BookedOnClass(ctx, tenantID, "class-1", gcid); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("BookedOnClass: expected ErrNotImplemented; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Input validation
// -----------------------------------------------------------------------------

func TestCohortRosterRepo_RejectEmptyTenant(t *testing.T) {
	t.Parallel()
	r := pg.NewCohortRosterRepo(&stubTxRunner{q: &stubQuerier{}})
	ctx := context.Background()

	if _, err := r.EnrolledInOffering(ctx, "", "offering-1", gcid); err == nil || !strings.Contains(err.Error(), "tenantID required") {
		t.Fatalf("EnrolledInOffering: expected tenantID required; got %v", err)
	}
	if _, err := r.BookedOnClass(ctx, "  ", "class-1", gcid); err == nil || !strings.Contains(err.Error(), "tenantID required") {
		t.Fatalf("BookedOnClass: expected tenantID required; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// EnrolledInOffering — EXISTS over offerings ⋈ course_enrollments
// -----------------------------------------------------------------------------

func TestCohortRosterRepo_EnrolledInOffering_ReturnsTrue(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return rosterExistsRow(dest, true) }}
		},
	}
	r := pg.NewCohortRosterRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	enrolled, err := r.EnrolledInOffering(ctx, tenantID, "offering-1", gcid)
	if err != nil {
		t.Fatalf("EnrolledInOffering: %v", err)
	}
	if !enrolled {
		t.Fatalf("expected enrolled=true from the stub")
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	sel := q.sqls[1]
	for _, frag := range []string{"SELECT EXISTS", "FROM offerings", "JOIN course_enrollments", "WHERE o.id = $2"} {
		if !strings.Contains(sel, frag) {
			t.Fatalf("offering EXISTS must contain %q; got %q", frag, sel)
		}
	}
	if len(q.args) < 2 || len(q.args[1]) != 3 ||
		q.args[1][0] != tenantID || q.args[1][1] != "offering-1" || q.args[1][2] != gcid {
		t.Fatalf("offering EXISTS args wrong: %v", q.args[1])
	}
}

func TestCohortRosterRepo_EnrolledInOffering_ReturnsFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return rosterExistsRow(dest, false) }}
		},
	}
	r := pg.NewCohortRosterRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	enrolled, err := r.EnrolledInOffering(ctx, tenantID, "offering-1", gcid)
	if err != nil {
		t.Fatalf("EnrolledInOffering: %v", err)
	}
	if enrolled {
		t.Fatalf("expected enrolled=false (no match)")
	}
}

func TestCohortRosterRepo_EnrolledInOffering_ScanError_Returned(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows in result set")
			}}
		},
	}
	r := pg.NewCohortRosterRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	_, err := r.EnrolledInOffering(ctx, tenantID, "offering-1", gcid)
	if err == nil {
		t.Fatalf("a scan error must be RETURNED, never folded into false")
	}
	if !strings.Contains(err.Error(), "no rows in result set") {
		t.Fatalf("expected the underlying scan error; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// BookedOnClass — EXISTS over bookings
// -----------------------------------------------------------------------------

func TestCohortRosterRepo_BookedOnClass_ReturnsTrue(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return rosterExistsRow(dest, true) }}
		},
	}
	r := pg.NewCohortRosterRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	booked, err := r.BookedOnClass(ctx, tenantID, "class-1", gcid)
	if err != nil {
		t.Fatalf("BookedOnClass: %v", err)
	}
	if !booked {
		t.Fatalf("expected booked=true from the stub")
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	sel := q.sqls[1]
	for _, frag := range []string{"SELECT EXISTS", "FROM bookings", "class_id     = $2", "learner_gcid = $3"} {
		if !strings.Contains(sel, frag) {
			t.Fatalf("class EXISTS must contain %q; got %q", frag, sel)
		}
	}
	if len(q.args) < 2 || len(q.args[1]) != 3 ||
		q.args[1][0] != tenantID || q.args[1][1] != "class-1" || q.args[1][2] != gcid {
		t.Fatalf("class EXISTS args wrong: %v", q.args[1])
	}
}

func TestCohortRosterRepo_BookedOnClass_ScanError_Returned(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("bad row bytes")
			}}
		},
	}
	r := pg.NewCohortRosterRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	_, err := r.BookedOnClass(ctx, tenantID, "class-1", gcid)
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("scan error must propagate; got %v", err)
	}
}
