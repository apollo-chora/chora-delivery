//go:build integration

// offering_bare_context_integration_test.go — CHO-2184 regression guard.
//
// THE DEFECT: OfferingRepo.Get returned (T, bool) and swallowed its error, so
// an RLS failure — a subscriber/cron handing the repo a BARE context.Background()
// with no tenant on it — was INDISTINGUISHABLE from a legitimate "row not found".
// rls.ApplySession produces a real, loud ErrNoTenantContext before any SQL runs;
// Get threw it away and reported a clean miss. The auto-cert completion engine
// logged "offering not found", looked perfectly healthy, and silently withheld
// every certificate it tried to issue.
//
// The control below is what makes this undeniable: the SAME row is readable
// under a tenant-bearing context. So (nil,false) from the bare-ctx read is not
// an absent row — it is an infrastructure failure wearing a miss's clothes.
//
// ⚠ This test is deliberately driven with a BARE context.Background(). An
// integration test that BUILDS its own ctx supplies what production does not,
// and passes over a repo that cannot make a single live call.
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration -run TestIntegration_OfferingRepo_BareContext ./services/chora-delivery/internal/adapter/repo/pg/...
package pg_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func TestIntegration_OfferingRepo_BareContext_IsLoudNotAMiss(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewOfferingRepo(&liveTxRunner{pool: pool})

	tenant, _ := uuid.NewV7()
	course, _ := uuid.NewV7()
	tenantCtx := tracing.WithTenantID(context.Background(), tenant.String())

	o, err := domain.NewOffering(domain.NewOfferingInput{
		TenantID:     tenant.String(),
		CourseIDs:    []string{course.String()},
		DeliveryType: domain.DeliveryTypeGraduate,
		Label:        "CHO-2184 bare-context guard",
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	if err := repo.Save(tenantCtx, o); err != nil {
		t.Fatalf("Save: %v", err)
	}
	t.Cleanup(func() {
		tx, err := pool.Begin(context.Background())
		if err != nil {
			return
		}
		_, _ = tx.Exec(context.Background(), fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant.String()))
		_, _ = tx.Exec(context.Background(), `DELETE FROM offerings WHERE id = $1`, o.ID)
		_ = tx.Commit(context.Background())
	})

	// ---- CONTROL: the row genuinely EXISTS and is readable with a tenant. ----
	got, ok, err := repo.Get(tenantCtx, o.ID)
	if err != nil {
		t.Fatalf("control Get(tenantCtx): unexpected error: %v", err)
	}
	if !ok || got == nil {
		t.Fatalf("control Get(tenantCtx): row must be found — seed did not land")
	}

	// ---- THE ASSERTION: a BARE ctx is an INFRA FAILURE, and must be LOUD. ----
	// This is the exact shape a Pub/Sub subscriber / cron hands the repo.
	got, ok, err = repo.Get(context.Background(), o.ID)
	if err == nil {
		t.Fatalf("SWALLOW: Get(context.Background()) returned no error for a row that EXISTS "+
			"(ok=%v) — an RLS failure is being reported as a clean miss. This is the defect "+
			"that silently withheld every auto-issued certificate.", ok)
	}
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("Get(bare ctx) must surface rls.ErrNoTenantContext, got: %v", err)
	}
	if ok || got != nil {
		t.Fatalf("Get(bare ctx) must not report a found row alongside an error (ok=%v, got=%v)", ok, got)
	}

	// ---- The other half of the contract: a GENUINE miss stays a quiet miss. ----
	// If "no row" started returning an error, callers would treat every absent
	// offering as an outage. not-found and blew-up must remain distinguishable.
	absent, _ := uuid.NewV7()
	got, ok, err = repo.Get(tenantCtx, absent.String())
	if err != nil {
		t.Fatalf("genuine miss must NOT be an error, got: %v", err)
	}
	if ok || got != nil {
		t.Fatalf("genuine miss must report ok=false, nil (ok=%v, got=%v)", ok, got)
	}
}
