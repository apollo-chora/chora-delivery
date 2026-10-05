//go:build integration

// offering_search_integration_test.go — live Cloud SQL integration test for the
// R+ universal-finder offering search (W2.A, CHO-1850). Gated behind the
// `integration` build tag; run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration -run TestIntegration_OfferingSearch ./services/chora-delivery/internal/adapter/repo/pg/...
//
// Requires migration 0032_offerings_search applied (label column + indexes).
// Proves the real SQL: keyset pagination, query-minus-self facets, and — the
// load-bearing assertion — RLS tenant isolation (tenant B never sees A's rows).
package pg_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func TestIntegration_OfferingSearch_RLS_Keyset_Facets(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewOfferingRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	course, _ := uuid.NewV7()
	ctxA := tracing.WithTenantID(context.Background(), tenantA.String())
	ctxB := tracing.WithTenantID(context.Background(), tenantB.String())

	var ids []string
	mk := func(ctx context.Context, tenant uuid.UUID, dt domain.DeliveryType, label string) *domain.Offering {
		o, err := domain.NewOffering(domain.NewOfferingInput{
			TenantID: tenant.String(), CourseIDs: []string{course.String()}, DeliveryType: dt, Label: label, Capacity: 0,
		})
		if err != nil {
			t.Fatalf("NewOffering: %v", err)
		}
		if err := repo.Save(ctx, o); err != nil {
			t.Fatalf("Save: %v", err)
		}
		ids = append(ids, o.ID)
		time.Sleep(2 * time.Millisecond) // distinct created_at for a deterministic keyset
		return o
	}

	t.Cleanup(func() {
		for _, tenant := range []uuid.UUID{tenantA, tenantB} {
			tx, err := pool.Begin(context.Background())
			if err != nil {
				return
			}
			_, _ = tx.Exec(context.Background(), fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant.String()))
			for _, id := range ids {
				_, _ = tx.Exec(context.Background(), `DELETE FROM offerings WHERE id = $1`, id)
			}
			_ = tx.Commit(context.Background())
		}
	})

	// Tenant A: 3 offerings (2 graduate, 1 short). Tenant B: 1 (async).
	mk(ctxA, tenantA, domain.DeliveryTypeGraduate, "Alpha Cohort")
	mk(ctxA, tenantA, domain.DeliveryTypeShort, "Bravo Bootcamp")
	mk(ctxA, tenantA, domain.DeliveryTypeGraduate, "Charlie Cohort")
	mk(ctxB, tenantB, domain.DeliveryTypeAsync, "Bravo Async")

	// ---- RLS isolation: tenant B sees only its own row ----
	pageB, err := repo.Search(ctxB, domain.OfferingQuery{TenantID: tenantB.String()})
	if err != nil {
		t.Fatalf("Search B: %v", err)
	}
	if pageB.TotalEstimate != 1 || len(pageB.Items) != 1 {
		t.Fatalf("RLS LEAK: tenant B should see exactly 1 offering; got total=%d items=%d", pageB.TotalEstimate, len(pageB.Items))
	}

	// ---- tenant A: total + facets ----
	pageA, err := repo.Search(ctxA, domain.OfferingQuery{TenantID: tenantA.String()})
	if err != nil {
		t.Fatalf("Search A: %v", err)
	}
	if pageA.TotalEstimate != 3 {
		t.Fatalf("tenant A total want 3; got %d", pageA.TotalEstimate)
	}
	var graduateCount int
	for _, f := range pageA.Facets {
		if f.Field == domain.OfferingFacetDeliveryType {
			for _, v := range f.Values {
				if v.Value == "graduate" {
					graduateCount = v.Count
				}
			}
		}
	}
	if graduateCount != 2 {
		t.Fatalf("delivery_type facet graduate count want 2; got %d", graduateCount)
	}

	// ---- keyset: limit=2 walk covers all 3 with no overlap ----
	seen := map[string]bool{}
	var cursor *domain.OfferingCursor
	for i := 0; i < 5; i++ {
		res, err := repo.Search(ctxA, domain.OfferingQuery{TenantID: tenantA.String(), Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("keyset page %d: %v", i, err)
		}
		for _, o := range res.Items {
			if seen[o.ID] {
				t.Fatalf("keyset overlap on %s", o.ID)
			}
			seen[o.ID] = true
		}
		if res.NextCursor == nil {
			break
		}
		cursor = res.NextCursor
	}
	if len(seen) != 3 {
		t.Fatalf("keyset walk should cover all 3 tenant-A offerings; got %d", len(seen))
	}

	// ---- free-text label (ILIKE) is tenant-scoped: "bravo" matches A's only ----
	pageQ, err := repo.Search(ctxA, domain.OfferingQuery{TenantID: tenantA.String(), Q: "bravo"})
	if err != nil {
		t.Fatalf("Search q: %v", err)
	}
	if len(pageQ.Items) != 1 || pageQ.Items[0].Label != "Bravo Bootcamp" {
		t.Fatalf("q=bravo under tenant A should match only Bravo Bootcamp; got %d items", len(pageQ.Items))
	}
}
