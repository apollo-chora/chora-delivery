//go:build integration

// catalogue_integration_test.go — exercises the CatalogueRepo round-trip
// against live Cloud SQL via the local Cloud SQL Auth Proxy (port 5432).
//
// This is the durability + cross-tenant-isolation proof for the public
// course catalogue: the Phyllis demo's catalogue is DB-backed (no in-memory,
// no inline seed), so an anonymous learner sees the seeded public courses
// AND a pod restart leaves them intact.
//
// Run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration -run TestIntegration_CatalogueRepo ./services/chora-delivery/internal/adapter/repo/pg/...
//
// Pre-req: chora-infra/scripts/seed-phyllis-demo.sh has been run so the
// Phyllis cast courses (incl. Mr. Chen's CSPO course, public=true) exist in
// chora_delivery.
//
// Suite:
//
//  1. SearchCursor(VisibilityFilterPublic) returns public courses CROSS-
//     TENANT through the public_courses_catalog view — Mr. Chen's CSPO
//     course (333…) is visible with NO tenant context set.
//  2. The same query NEVER surfaces a non-public course (MTM Math, 444…,
//     public=false) — the view's WHERE clause is the gate.
//  3. Get(cspoCourseID) resolves Mr. Chen's CSPO course by id, cross-tenant.
//  4. Save → SearchCursor round-trip: a freshly UPSERTed public course
//     appears in the public catalogue; the same Save replayed is idempotent
//     (no duplicate row).
//  5. Tenant-scoped SearchCursor (TenantID set) stays RLS-isolated — a
//     course inserted under tenant A is NOT visible to tenant B's
//     tenant-scoped browse.
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// catalogueLiveTxRunner reuses the liveTxRunner from course_integration_test.go
// (same package). Defined there as `liveTxRunner`; we just construct it here.

// TestIntegration_CatalogueRepo_PublicDiscovery_CrossTenant verifies the
// public-discovery read path: VisibilityFilterPublic with NO tenant context
// returns Mr. Chen's CSPO course (public=true) cross-tenant via the view.
func TestIntegration_CatalogueRepo_PublicDiscovery_CrossTenant(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewCatalogueRepo(&liveTxRunner{pool: pool})

	page, err := repo.SearchCursor(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
		First:      100,
	})
	if err != nil {
		t.Fatalf("SearchCursor public: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatalf("expected >=1 public course (run chora-infra/scripts/seed-phyllis-demo.sh)")
	}

	// The seeded CSPO course (Mr. Chen, public=true) MUST be present.
	var sawCSPO bool
	for _, pc := range page.Items {
		if pc.ID == cspoCourseID {
			sawCSPO = true
			if pc.Visibility != domain.VisibilityPublic || !pc.Public {
				t.Errorf("CSPO course must scan as VisibilityPublic; got %q", pc.Visibility)
			}
		}
		// The public view must NEVER surface a non-public row.
		if !pc.Public || pc.Visibility != domain.VisibilityPublic {
			t.Errorf("public catalogue leaked a non-public row: %s (%q)", pc.ID, pc.Visibility)
		}
	}
	if !sawCSPO {
		t.Errorf("expected Mr. Chen's CSPO course %s in the public catalogue", cspoCourseID)
	}
	t.Logf("public catalogue cross-tenant: %d course(s), CSPO present=%v", len(page.Items), sawCSPO)
}

// TestIntegration_CatalogueRepo_PublicDiscovery_NonPublicNeverLeaks verifies
// the negative: the MTM Math course (444…, public=false) is unreachable
// through the public catalogue regardless of caller context.
func TestIntegration_CatalogueRepo_PublicDiscovery_NonPublicNeverLeaks(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewCatalogueRepo(&liveTxRunner{pool: pool})

	const mtmCourseID = "44444444-4444-7444-8444-444444444444"

	page, err := repo.SearchCursor(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
		First:      200,
	})
	if err != nil {
		t.Fatalf("SearchCursor: %v", err)
	}
	for _, pc := range page.Items {
		if pc.ID == mtmCourseID {
			t.Fatalf("RLS/view LEAK: non-public MTM Math course surfaced in the public catalogue")
		}
	}

	// Get on the non-public course id must also return ok=false through the
	// public view.
	_, ok, err := repo.Get(context.Background(), mtmCourseID)
	if err != nil {
		t.Fatalf("Get mtm: %v", err)
	}
	if ok {
		t.Fatalf("Get LEAK: non-public MTM Math course resolved through the public view")
	}
}

// TestIntegration_CatalogueRepo_Get_CSPOCourse verifies Get resolves Mr.
// Chen's CSPO course by id, cross-tenant, through the view.
func TestIntegration_CatalogueRepo_Get_CSPOCourse(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewCatalogueRepo(&liveTxRunner{pool: pool})

	pc, ok, err := repo.Get(context.Background(), cspoCourseID)
	if err != nil {
		t.Fatalf("Get CSPO: %v", err)
	}
	if !ok || pc == nil {
		t.Fatalf("expected CSPO course %s reachable via Get (run seed-phyllis-demo.sh)", cspoCourseID)
	}
	if pc.TenantID != chenTenantID {
		t.Errorf("CSPO course tenant mismatch: got %s want %s", pc.TenantID, chenTenantID)
	}
	if !pc.Public {
		t.Errorf("CSPO course must be public")
	}
	t.Logf("Get CSPO: title=%q tenant=%s public=%v", pc.Title, pc.TenantID, pc.Public)
}

// TestIntegration_CatalogueRepo_Save_RoundTrip_AndIdempotent verifies a
// freshly UPSERTed public course appears in the public catalogue, and the
// same Save replayed is idempotent (no duplicate row).
func TestIntegration_CatalogueRepo_Save_RoundTrip_AndIdempotent(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewCatalogueRepo(&liveTxRunner{pool: pool})

	tenant, _ := uuid.NewV7()
	courseID, _ := uuid.NewV7()
	instructor, _ := uuid.NewV7()

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM courses WHERE course_id = $1`, courseID.String())
	})

	pc, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenant.String(),
		Title:          "integration CatalogueRepo public course",
		InstructorGCID: instructor.String(),
		InstructorName: "Integration Tester",
		Visibility:     domain.VisibilityPublic,
		PriceSGDCents:  0,
	})
	if err != nil {
		t.Fatalf("NewPublicCourse: %v", err)
	}
	pc.ID = courseID.String()

	if err := repo.Save(context.Background(), pc); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// It must now resolve through the public view (cross-tenant).
	got, ok, err := repo.Get(context.Background(), courseID.String())
	if err != nil || !ok {
		t.Fatalf("Get after Save: ok=%v err=%v", ok, err)
	}
	if got.Title != pc.Title {
		t.Fatalf("round-trip title mismatch: got %q want %q", got.Title, pc.Title)
	}

	// Replay Save with a mutated title — UPSERT mutates, never duplicates.
	pc.Title = "integration CatalogueRepo public course (v2)"
	pc.UpdatedAt = time.Now().UTC()
	if err := repo.Save(context.Background(), pc); err != nil {
		t.Fatalf("Save replay: %v", err)
	}
	got2, ok, err := repo.Get(context.Background(), courseID.String())
	if err != nil || !ok {
		t.Fatalf("Get after replay: ok=%v err=%v", ok, err)
	}
	if got2.Title != "integration CatalogueRepo public course (v2)" {
		t.Fatalf("idempotent UPSERT did not mutate; got %q", got2.Title)
	}

	// Exactly ONE row for that course_id.
	verifyTx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin verify: %v", err)
	}
	defer verifyTx.Rollback(context.Background())
	if _, err := verifyTx.Exec(context.Background(),
		"SET LOCAL chora.tenant_id = '"+tenant.String()+"'"); err != nil {
		t.Fatalf("set local: %v", err)
	}
	var count int
	if err := verifyTx.QueryRow(context.Background(),
		`SELECT count(*) FROM courses WHERE course_id = $1`, courseID.String()).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row after UPSERT replay; got %d", count)
	}
}

// TestIntegration_CatalogueRepo_TenantScoped_RLSIsolation verifies a
// tenant-scoped SearchCursor stays RLS-isolated: a course inserted under
// tenant A is NOT visible to tenant B's tenant-scoped browse.
func TestIntegration_CatalogueRepo_TenantScoped_RLSIsolation(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewCatalogueRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	courseID, _ := uuid.NewV7()
	instructor, _ := uuid.NewV7()

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM courses WHERE course_id = $1`, courseID.String())
	})

	pc, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA.String(),
		Title:          "tenant-A-only catalogue course",
		InstructorGCID: instructor.String(),
		Visibility:     domain.VisibilityTenantOnly,
	})
	if err != nil {
		t.Fatalf("NewPublicCourse: %v", err)
	}
	pc.ID = courseID.String()
	if err := repo.Save(context.Background(), pc); err != nil {
		t.Fatalf("Save under A: %v", err)
	}

	// Tenant B's tenant-scoped browse must NOT see tenant A's tenant_only
	// course.
	pageB, err := repo.SearchCursor(context.Background(), domain.CatalogueQuery{
		TenantID:   tenantB.String(),
		Visibility: domain.VisibilityFilterTenantOrPublic,
		First:      200,
	})
	if err != nil {
		t.Fatalf("SearchCursor under B: %v", err)
	}
	for _, c := range pageB.Items {
		if c.ID == courseID.String() {
			t.Fatalf("RLS LEAK: tenant B's browse saw tenant A's tenant_only course")
		}
	}

	// Tenant A's tenant-scoped browse MUST see it.
	pageA, err := repo.SearchCursor(context.Background(), domain.CatalogueQuery{
		TenantID:   tenantA.String(),
		Visibility: domain.VisibilityFilterTenantOrPublic,
		First:      200,
	})
	if err != nil {
		t.Fatalf("SearchCursor under A: %v", err)
	}
	var sawA bool
	for _, c := range pageA.Items {
		if c.ID == courseID.String() {
			sawA = true
		}
	}
	if !sawA {
		t.Fatalf("expected tenant A's browse to see its own tenant_only course")
	}
	t.Logf("tenant-scoped RLS isolation OK: A sees own course, B does not")
}
