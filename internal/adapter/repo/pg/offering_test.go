// offering_test.go — unit tests for pg.OfferingRepo (R+ four-mode W1).
//
// Mirrors exam_test.go: stubs the Querier (shared stubQuerier / stubTxRunner /
// stubRow / stubRows + tenantID / courseID consts from application_test.go) so
// the SQL surface + RLS contract are exercised without a live DB. The Offering
// aggregate is persisted as a JSONB snapshot, so reads stub a single
// `data []byte` column carrying the marshalled aggregate.
//
// Guarantees:
//  1. rls.ApplySession runs BEFORE the data query (every method).
//  2. SQL matches shape (UPSERT ON CONFLICT (id), SELECT data ... deleted_at
//     IS NULL, ORDER BY id).
//  3. Nil-tx is fail-loud on writes (Save / ListByTenant → ErrNotImplemented)
//     and degrades on point reads (Get → ok=false).
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
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func newOffering(id string, state domain.OfferingState) *domain.Offering {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &domain.Offering{
		ID:           id,
		TenantID:     tenantID,
		CourseIDs:    []string{courseID},
		DeliveryType: domain.DeliveryTypeGraduate,
		Label:        "2026 Spring Cohort",
		Capacity:     30,
		State:        state,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func offeringJSON(t *testing.T, o *domain.Offering) []byte {
	t.Helper()
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("marshal offering: %v", err)
	}
	return b
}

func TestOfferingRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewOfferingRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newOffering("01970000-0000-7000-9999-f00000000001", domain.OfferingStateDraft)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenant(ctx, tenantID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "01970000-0000-7000-9999-f00000000001"); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

func TestOfferingRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newOffering("01970000-0000-7000-9999-f00000000002", domain.OfferingStateLaunched)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO offerings") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert into offerings; got %q", last)
	}
}

func TestOfferingRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	want := newOffering("01970000-0000-7000-9999-f00000000003", domain.OfferingStateRunning)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = offeringJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	o, ok, _ := r.Get(ctx, want.ID)
	if !ok || o == nil {
		t.Fatalf("Get: expected hit")
	}
	if o.ID != want.ID || o.State != domain.OfferingStateRunning || o.DeliveryType != domain.DeliveryTypeGraduate {
		t.Fatalf("Get: rehydration mismatch; got %+v", o)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM offerings") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM offerings; got %q", last)
	}
}

func TestOfferingRepo_Get_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if o, ok, _ := r.Get(ctx, "nope"); ok || o != nil {
		t.Fatalf("Get: expected (nil, false); got (%+v, %v)", o, ok)
	}
}

func TestOfferingRepo_ListByTenant_TwoRows(t *testing.T) {
	t.Parallel()
	a := newOffering("01970000-0000-7000-9999-f000000000aa", domain.OfferingStateDraft)
	b := newOffering("01970000-0000-7000-9999-f000000000bb", domain.OfferingStateConcluded)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = offeringJSON(t, a); return nil },
				func(dest ...any) error { *(dest[0].(*[]byte)) = offeringJSON(t, b); return nil },
			}}, nil
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
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
