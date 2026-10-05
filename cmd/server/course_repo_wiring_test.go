// course_repo_wiring_test.go — ADR-236 D1: proves the pool-gated
// courseRepo wiring in main() actually PICKS the durable adapter when a
// pool is available, and the in-memory fallback when it is not.
//
// main() itself has no return value and is not unit-tested anywhere in this
// package (log.Fatalf-driven bootstrap) — every one of the ~15 other
// `if txr := newPgxTxRunner(pool); txr != nil { ... } else { ... }` gates in
// main.go share this same untested shape. This test exercises the actual
// decision mechanism (newPgxTxRunner's nil-vs-non-nil contract) the new
// courseRepo gate depends on, plus proves a non-nil TxRunner constructs a
// working pg.CourseRepo — i.e. "pool present ⇒ pg is wired, not inmem".
//
// The non-nil *pgxpool.Pool is built via pgxpool.ParseConfig + NewWithConfig
// against a port nothing listens on — pgx v5 pools are lazy (New succeeds;
// the dial happens on first Acquire), so construction never touches the
// network. Mirrors chora-notifications/internal/adapter/pg/runtime_test.go's
// newDeadPool (same DSN + timeout-tuning idiom, reused here for the same
// "prove the wiring without a live DB" reason).
package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	deliverypg "github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// deadPool builds a *pgxpool.Pool wired to an unreachable port. Connect is
// lazy: NewWithConfig succeeds; Exec/BeginTx fails on first attempt with a
// connect error. MinConns=0 + a short ConnectTimeout keep the test fast.
func deadPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://stub:stub@127.0.0.1:1/stub?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.MinConns = 0
	cfg.MaxConns = 1
	cfg.ConnConfig.ConnectTimeout = 200 * time.Millisecond

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestCourseRepoWiring_NilPool_PicksInMemFallback mirrors the courseRepo
// wiring's else-branch condition: CHORA_DB_DSN unset (pool nil) ⇒
// newPgxTxRunner(nil) is nil, so main.go's gate falls back to
// inmem.NewCourseRepo() — never durable.
func TestCourseRepoWiring_NilPool_PicksInMemFallback(t *testing.T) {
	t.Parallel()
	if txr := newPgxTxRunner(nil); txr != nil {
		t.Fatalf("newPgxTxRunner(nil) = %v, want nil (drives the courseRepo gate's in-memory fallback)", txr)
	}
}

// TestCourseRepoWiring_LivePool_PicksDurablePgCourseRepo mirrors the
// courseRepo wiring's if-branch condition: a live pool ⇒ newPgxTxRunner
// returns a non-nil TxRunner, and pg.NewCourseRepo(txr) constructs a
// CourseRepo that is wired for durable writes (NOT the ErrNotImplemented
// nil-TxRunner shape a nil pool would produce). This is the exact
// conditional cmd/server/main.go's courseRepo gate runs.
func TestCourseRepoWiring_LivePool_PicksDurablePgCourseRepo(t *testing.T) {
	t.Parallel()
	pool := deadPool(t)

	txr := newPgxTxRunner(pool)
	if txr == nil {
		t.Fatalf("newPgxTxRunner(non-nil pool) = nil, want non-nil (drives the courseRepo gate's pg branch)")
	}

	// Mirrors main.go's gate exactly: `courseRepo = pg.NewCourseRepo(txr)`.
	var courseRepo domain.CourseRepo = deliverypg.NewCourseRepo(txr)

	// A nil-TxRunner pg.CourseRepo (the CHORA_DB_DSN-unset shape) returns
	// ErrNotImplemented synchronously, with no I/O attempt. Save on THIS
	// repo must NOT take that path — proving txr (and therefore the pool)
	// is actually threaded through, i.e. the durable branch was picked, not
	// the never-wired one. It still errors (nothing listens on
	// 127.0.0.1:1), but the error must come from an attempted
	// connection/transaction (BeginTx), not the nil-TxRunner guard clause.
	const wiringProbeTenantID = "01970000-0000-7000-8000-000000000abc"
	c, err := domain.NewCourse(wiringProbeTenantID, "wiring probe", nil, 1)
	if err != nil {
		t.Fatalf("NewCourse: %v", err)
	}
	c.InstructorGCID = wiringProbeTenantID

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	saveErr := courseRepo.Save(ctx, c)
	if saveErr == nil {
		t.Fatalf("Save against a dead pool unexpectedly succeeded")
	}
	if saveErr == deliverypg.ErrNotImplemented {
		t.Fatalf("Save returned ErrNotImplemented — the pg branch was NOT wired with a real TxRunner (regressed to the nil-pool shape)")
	}
}
