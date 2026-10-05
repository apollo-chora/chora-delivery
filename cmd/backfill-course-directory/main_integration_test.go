//go:build integration

// main_integration_test.go — PG prepare-smoke for the CHO-2247 backfill sweep.
//
// WHY THIS EXISTS
// ---------------
// The sweep shipped with `COALESCE(author_gcid, ”)` where courses.author_gcid
// is `uuid`. Postgres coalesced a uuid with an empty STRING, tried ”::uuid, and
// killed the query at parse time with 22P02 on EVERY tenant, unconditionally.
// The unit suite was green throughout, because a stub/mock Querier never
// type-checks SQL — it just records the string. The tool could not read a single
// row and nothing said so until a human ran it.
//
// A prepare-smoke is the cheapest thing that could have caught it: PREPARE asks
// Postgres to PARSE + TYPE-CHECK the statement server-side WITHOUT executing it.
// No rows read, no rows written, no side effects — and a type error surfaces
// exactly as it would in production. Per the repo's standing rule: "PG
// prepare-smoke over exec stubs".
//
// Run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN="$(gcloud secrets versions access latest \
//	  --secret=chora-dev-cloudsql-chora_delivery-app_ro-dsn | sed 's#:5432#:15432#')"
//	go test -tags integration ./services/chora-delivery/cmd/backfill-course-directory/
//
// (app_ro is sufficient and correct — this test only ever READS.)
package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// liveDelivery opens chora_delivery. Skips only when no DSN is configured —
// but says so LOUDLY, because a silently-skipped guard is precisely the failure
// mode this file exists to prevent.
func liveDelivery(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("CHORA_TEST_DSN"))
	if dsn == "" {
		t.Skip("CHORA_TEST_DSN unset — SKIPPING the prepare-smoke. This guard is the ONLY thing that type-checks the sweep's SQL; a stub Querier cannot. Run it before trusting the tool.")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open chora_delivery: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping chora_delivery (is the proxy port-forward up?): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestIntegration_PrepareSmoke_SelectPublishedCourses type-checks the sweep's
// SELECT against live Postgres. PREPARE parses + analyses without executing, so
// this asserts the SQL is well-typed against the REAL schema — the exact check
// that was missing when COALESCE(author_gcid,”) shipped.
func TestIntegration_PrepareSmoke_SelectPublishedCourses(t *testing.T) {
	db := liveDelivery(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stmt, err := db.PrepareContext(ctx, sqlSelectPublishedCourses)
	if err != nil {
		t.Fatalf("PREPARE sqlSelectPublishedCourses failed — the sweep's SQL is not well-typed "+
			"against the live schema and the tool cannot read a single row:\n%v", err)
	}
	_ = stmt.Close()
}

// TestIntegration_PrepareSmoke_NegativeControl proves the smoke is NOT vacuous:
// the ORIGINAL, shipped-broken SQL (COALESCE over a bare uuid) MUST fail to
// prepare. If this passes, the guard cannot see the bug it was built for and
// every green above is meaningless.
func TestIntegration_PrepareSmoke_NegativeControl(t *testing.T) {
	db := liveDelivery(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const shippedBrokenSQL = `
SELECT course_id, tenant_id, title, COALESCE(author_gcid,''), state,
       COALESCE(published_at, updated_at, created_at)
  FROM courses
 WHERE deleted_at IS NULL AND state = 'PUBLISHED'
 ORDER BY created_at`

	stmt, err := db.PrepareContext(ctx, shippedBrokenSQL)
	if err == nil {
		_ = stmt.Close()
		t.Fatal("NEGATIVE CONTROL FAILED: the known-broken SQL prepared cleanly. " +
			"This smoke cannot detect a uuid/text type error, so its PASS proves nothing — fix the guard.")
	}
	if !strings.Contains(err.Error(), "22P02") && !strings.Contains(strings.ToLower(err.Error()), "uuid") {
		t.Fatalf("negative control failed for an UNEXPECTED reason (%v) — a name-diff is blind to a NEW failure reason; re-ground before trusting it", err)
	}
	t.Logf("negative control OK: the shipped SQL is rejected by Postgres as expected (%v)", err)
}

// TestIntegration_LoadCourses_ScansRealRows drives loadCourses end to end
// against live Postgres. PREPARE type-checks the statement; only a real Query +
// Scan proves the uuid columns actually land in Go strings — the SECOND arm of
// the same bug class, one line below the first.
//
// ⚠ Asserts a NON-ZERO read. A 0 here is not "clean" — courses is FORCE-RLS with
// no BYPASSRLS role available, so an unset/wrong chora.tenant_id yields a silent
// empty set that looks exactly like success.
func TestIntegration_LoadCourses_ScansRealRows(t *testing.T) {
	db := liveDelivery(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const chensCoaching = "22222222-2222-7222-8222-222222222222"
	rows, err := loadCourses(ctx, db, []string{chensCoaching})
	if err != nil {
		t.Fatalf("loadCourses against live chora_delivery: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("loadCourses read 0 rows for a tenant known to hold PUBLISHED courses — " +
			"an RLS false negative or a broken GUC, NOT an empty platform")
	}
	for _, c := range rows {
		if c.CourseID == "" || c.TenantID == "" {
			t.Fatalf("uuid column scanned into an EMPTY string (%+v) — the ::text casts are not doing their job", c)
		}
		if c.State != "PUBLISHED" {
			t.Errorf("state = %q; want PUBLISHED only (the sweep must never emit a DRAFT title)", c.State)
		}
		if c.OccurredAt.IsZero() {
			t.Errorf("occurred_at is zero for %s — LWW would treat it as the oldest writer and the upsert would no-op", c.CourseID)
		}
	}
	t.Logf("loadCourses read %d PUBLISHED course(s) for Chen's Coaching", len(rows))
}
