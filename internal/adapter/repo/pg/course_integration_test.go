//go:build integration

// course_integration_test.go — exercises the CourseRepo round-trip against
// live Cloud SQL via the local Cloud SQL Auth Proxy (port 5432).
//
// Run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration -run TestIntegration_CourseRepo ./services/chora-delivery/internal/adapter/repo/pg/...
//
// Suite:
//
//  1. Save → Get round-trip under tenant A; reads succeed.
//  2. Save under tenant A then read under tenant B → ok=false (RLS isolation
//     end-to-end through the repo layer, not just raw SQL).
//  3. UPSERT idempotency: Save twice with same course_id; second call
//     mutates fields without inserting a new row.
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// liveTxRunner adapts a pgx pool to pg.TxRunner for integration tests.
type liveTxRunner struct {
	pool *pgxpool.Pool
}

// pgxPoolQuerier wraps a pgx.Tx so it satisfies pg.Querier (the local
// Querier shape used by chora-delivery's pg package).
type pgxPoolQuerier struct {
	tx pgx.Tx
}

func (q *pgxPoolQuerier) Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error) {
	tag, err := q.tx.Exec(ctx, sql, args...)
	if err != nil {
		return rls.CommandTag{}, err
	}
	return rls.CommandTag{RowsAffected: tag.RowsAffected()}, nil
}

func (q *pgxPoolQuerier) QueryRow(ctx context.Context, sql string, args ...any) pg.Row {
	return &pgxPoolRow{r: q.tx.QueryRow(ctx, sql, args...)}
}

func (q *pgxPoolQuerier) Query(ctx context.Context, sql string, args ...any) (pg.Rows, error) {
	rs, err := q.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &pgxPoolRows{r: rs}, nil
}

type pgxPoolRow struct{ r pgx.Row }

func (r *pgxPoolRow) Scan(d ...any) error { return r.r.Scan(d...) }

type pgxPoolRows struct{ r pgx.Rows }

func (r *pgxPoolRows) Next() bool          { return r.r.Next() }
func (r *pgxPoolRows) Scan(d ...any) error { return r.r.Scan(d...) }
func (r *pgxPoolRows) Close() error        { r.r.Close(); return nil }
func (r *pgxPoolRows) Err() error          { return r.r.Err() }

// RunInTx opens a pgx tx + invokes fn with a Querier wrapping the tx.
func (l *liveTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q pg.Querier) error) (err error) {
	tx, err := l.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
			return
		}
		err = tx.Commit(ctx)
	}()
	q := &pgxPoolQuerier{tx: tx}
	return fn(ctx, q)
}

// TestIntegration_CourseRepo_SaveGet_Roundtrip verifies Save + Get under
// the same tenant context, RLS-scoped through the repo layer.
func TestIntegration_CourseRepo_SaveGet_Roundtrip(t *testing.T) {
	pool := liveDB(t)
	tx := &liveTxRunner{pool: pool}
	r := pg.NewCourseRepo(tx)

	tenantA, _ := uuid.NewV7()
	courseID, _ := uuid.NewV7()
	instructorGCID, _ := uuid.NewV7()

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM courses WHERE course_id = $1`, courseID.String())
	})

	c := &domain.Course{
		ID:             courseID.String(),
		TenantID:       tenantA.String(),
		InstructorGCID: instructorGCID.String(),
		Title:          "integration-test course",
		AtomIDs:        []string{},
		MaxCapacity:    25,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}

	ctx := tracing.WithTenantID(context.Background(), tenantA.String())
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok, err := r.Get(ctx, tenantA.String(), courseID.String())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || got == nil {
		t.Fatalf("expected ok=true on round-trip; got nil")
	}
	if got.ID != c.ID || got.TenantID != c.TenantID || got.Title != c.Title || got.MaxCapacity != c.MaxCapacity {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, c)
	}
}

// TestIntegration_CourseRepo_RLS_TenantIsolation verifies that a Course
// inserted under tenant A through the CourseRepo is NOT readable through
// the CourseRepo under tenant B's context — i.e., the rls.ApplySession
// inside the repo + the Postgres RLS policy compose to leak-safe.
func TestIntegration_CourseRepo_RLS_TenantIsolation(t *testing.T) {
	pool := liveDB(t)
	tx := &liveTxRunner{pool: pool}
	r := pg.NewCourseRepo(tx)

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	courseID, _ := uuid.NewV7()
	instructorGCID, _ := uuid.NewV7()

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM courses WHERE course_id = $1`, courseID.String())
	})

	c := &domain.Course{
		ID:             courseID.String(),
		TenantID:       tenantA.String(),
		InstructorGCID: instructorGCID.String(),
		Title:          "rls course",
		AtomIDs:        []string{},
		MaxCapacity:    10,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}

	ctxA := tracing.WithTenantID(context.Background(), tenantA.String())
	if err := r.Save(ctxA, c); err != nil {
		t.Fatalf("Save under A: %v", err)
	}

	// Reading under tenant B → must not surface the row.
	ctxB := tracing.WithTenantID(context.Background(), tenantB.String())
	got, ok, err := r.Get(ctxB, tenantB.String(), courseID.String())
	if err != nil {
		t.Fatalf("Get under B: %v", err)
	}
	if ok || got != nil {
		t.Fatalf("RLS LEAK: tenant B saw tenant A's course")
	}

	// Reading under tenant A → must surface the row.
	gotA, okA, err := r.Get(ctxA, tenantA.String(), courseID.String())
	if err != nil {
		t.Fatalf("Get under A: %v", err)
	}
	if !okA || gotA == nil {
		t.Fatalf("expected tenant A to see its own course")
	}
	t.Logf("RLS isolation OK at the repo layer: A=ok B=hidden")
}

// TestIntegration_CourseRepo_Save_IsIdempotent verifies UPSERT semantics:
// the same Save call replayed mutates fields, NEVER inserts a new row.
func TestIntegration_CourseRepo_Save_IsIdempotent(t *testing.T) {
	pool := liveDB(t)
	tx := &liveTxRunner{pool: pool}
	r := pg.NewCourseRepo(tx)

	tenantA, _ := uuid.NewV7()
	courseID, _ := uuid.NewV7()
	instructorGCID, _ := uuid.NewV7()

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM courses WHERE course_id = $1`, courseID.String())
	})

	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	c := &domain.Course{
		ID:             courseID.String(),
		TenantID:       tenantA.String(),
		InstructorGCID: instructorGCID.String(),
		Title:          "v1",
		AtomIDs:        []string{},
		MaxCapacity:    5,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	c.Title = "v2"
	c.MaxCapacity = 50
	c.UpdatedAt = time.Now().UTC()
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save v2 (replay): %v", err)
	}

	got, ok, err := r.Get(ctx, tenantA.String(), courseID.String())
	if err != nil || !ok {
		t.Fatalf("Get after replay: ok=%v err=%v", ok, err)
	}
	if got.Title != "v2" || got.MaxCapacity != 50 {
		t.Fatalf("idempotent UPSERT did not mutate; got %+v", got)
	}

	// Verify there is exactly ONE row for that course_id. Use a tx with
	// SET LOCAL chora.tenant_id so the RLS policy admits the row.
	verifyTx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin verify: %v", err)
	}
	defer verifyTx.Rollback(context.Background())
	if _, err := verifyTx.Exec(context.Background(),
		"SET LOCAL chora.tenant_id = '"+tenantA.String()+"'"); err != nil {
		t.Fatalf("set local for verify: %v", err)
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
