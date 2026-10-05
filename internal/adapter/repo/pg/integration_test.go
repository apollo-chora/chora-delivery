//go:build integration

// integration_test.go — live Cloud SQL integration tests gated behind
// the `integration` build tag. Run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration ./services/chora-delivery/internal/adapter/repo/pg/...
//
// Closes the deferred Wave-A RLS check for chora_delivery.
//
// Test suite:
//
//  1. RLS isolation on courses — tenant A insert NOT visible to tenant B
//  2. Phyllis seed roundtrip — verifies CSPO course (Mr. Chen) is reachable
//     under his tenant context (00000000-...22222222 tenant)
package pg_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	cgcdb "github.com/apollo-chora/chora-common/db"
	cgcsecrets "github.com/apollo-chora/chora-common/secrets"
)

const (
	chenTenantID = "22222222-2222-7222-8222-222222222222"
	cspoCourseID = "33333333-3333-7333-8333-333333333333"
	mtmTenantID  = "11111111-1111-7111-8111-111111111111"
	phyllisGCID  = "00000000-0000-7000-8000-000000001999"
)

func liveDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("CHORA_TEST_DSN")
	secretID := os.Getenv("CHORA_TEST_DSN_SECRET_ID")
	if dsn == "" && secretID == "" {
		t.Skip("set CHORA_TEST_DSN or CHORA_TEST_DSN_SECRET_ID to run integration tests")
	}

	project := os.Getenv("CHORA_TEST_DB_PROJECT")
	if project == "" {
		project = "chora-489812"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var fetcher cgcdb.SecretFetcher
	var sclient *cgcsecrets.Client
	if secretID != "" && dsn == "" {
		c, err := cgcsecrets.NewClient(ctx, project)
		if err != nil {
			t.Fatalf("secret manager: %v", err)
		}
		sclient = c
		fetcher = c
	}

	pool, err := cgcdb.Bootstrap(ctx, cgcdb.BootstrapOptions{
		DSN:             dsn,
		SecretID:        secretID,
		SecretFetcher:   fetcher,
		RewriteFromPort: 6432,
		RewriteToPort:   5432,
		AppName:         "chora-delivery-pg-integration-test",
	})
	if err != nil {
		if sclient != nil {
			_ = sclient.Close()
		}
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if sclient != nil {
			_ = sclient.Close()
		}
	})
	return pool
}

// TestIntegration_PhyllisSeed_CSPOCourse verifies the Phyllis seed
// (CSPO course owned by Mr. Chen) is reachable under his tenant context.
// Cross-tenant visibility check: Phyllis's MTM tenant should NOT see
// the internal MTM Math course from Chen's tenant context.
func TestIntegration_PhyllisSeed_CSPOCourse(t *testing.T) {
	pool := liveDB(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", chenTenantID)); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	var (
		title          string
		instructorGCID string
		isPublic       bool
	)
	if err := tx.QueryRow(ctx, `
        SELECT title, instructor_gcid, public FROM courses
        WHERE course_id = $1::uuid AND deleted_at IS NULL`,
		cspoCourseID).Scan(&title, &instructorGCID, &isPublic); err != nil {
		t.Fatalf("query CSPO course: %v (run chora-infra/scripts/seed-phyllis-demo.sh)", err)
	}
	if !isPublic {
		t.Errorf("CSPO course should be public; got public=%v", isPublic)
	}
	t.Logf("CSPO seed verified: title=%q instructor=%s public=%v", title, instructorGCID, isPublic)

	// Cross-tenant visibility: from Chen's tenant, MTM Math (Phyllis's
	// internal course in tenant 11111111) should NOT be visible.
	var countMTM int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM courses WHERE tenant_id = $1::uuid AND deleted_at IS NULL`,
		mtmTenantID).Scan(&countMTM); err != nil {
		t.Fatalf("cross-tenant count: %v", err)
	}
	if countMTM != 0 {
		t.Errorf("RLS LEAK: Chen's tenant context saw %d MTM courses (expected 0)", countMTM)
	}
}

// TestIntegration_RLS_TenantIsolation_Courses verifies that a course
// inserted under tenant A is NOT visible to tenant B.
func TestIntegration_RLS_TenantIsolation_Courses(t *testing.T) {
	pool := liveDB(t)
	ctx := context.Background()

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	courseID, _ := uuid.NewV7()
	instructorGCID, _ := uuid.NewV7()

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM courses WHERE course_id = $1`, courseID.String())
	})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("set local A: %v", err)
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO courses (course_id, tenant_id, instructor_gcid, title, description, public)
        VALUES ($1, $2, $3, 'rls-test', 'integration test', false)`,
		courseID.String(), tenantA.String(), instructorGCID.String()); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert under A: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	// Tenant B → 0 rows
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin B: %v", err)
	}
	defer tx2.Rollback(ctx)
	if _, err := tx2.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantB.String())); err != nil {
		t.Fatalf("set local B: %v", err)
	}
	var countB int
	if err := tx2.QueryRow(ctx,
		`SELECT count(*) FROM courses WHERE course_id = $1`, courseID.String()).Scan(&countB); err != nil {
		t.Fatalf("count under B: %v", err)
	}
	if countB != 0 {
		t.Errorf("RLS LEAK: tenant B saw %d rows for tenant A's course", countB)
	}

	// Tenant A → 1 row
	tx3, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A2: %v", err)
	}
	defer tx3.Rollback(ctx)
	if _, err := tx3.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
		t.Fatalf("set local A2: %v", err)
	}
	var countA int
	if err := tx3.QueryRow(ctx,
		`SELECT count(*) FROM courses WHERE course_id = $1`, courseID.String()).Scan(&countA); err != nil {
		t.Fatalf("count under A: %v", err)
	}
	if countA != 1 {
		t.Errorf("expected 1 row under tenant A, got %d", countA)
	}
	_ = phyllisGCID
	t.Logf("RLS isolation OK: tenant A=%d row(s), tenant B=%d row(s)", countA, countB)
}
