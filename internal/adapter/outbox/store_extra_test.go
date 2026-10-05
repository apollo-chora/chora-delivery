// store_extra_test.go — additional Store coverage: the error branches the
// baseline fixtures miss — InMemoryStore missing-row errors, PostgresStore
// Exec/Query/scan/iteration error paths, the duplicate-key-classification
// message (without SQLSTATE 23505), and the long-message truncate branch —
// all on the same stubDB / fakeRows shapes the existing store tests use.
package outbox_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/outbox"
)

func TestInMemoryStore_MarkPublished_MissingRow(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	err := store.MarkPublished(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error for missing row")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v; want not-found error", err)
	}
}

func TestInMemoryStore_MarkFailed_MissingRow(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	err := store.MarkFailed(context.Background(), "no-such-id", "boom")
	if err == nil {
		t.Fatal("expected error for missing row")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v; want not-found error", err)
	}
}

func TestInMemoryStore_Deadletter_MissingRow(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	err := store.Deadletter(context.Background(), "no-such-id", "fatal", 1)
	if err == nil {
		t.Fatal("expected error for missing row")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v; want not-found error", err)
	}
}

func TestPostgresStore_Insert_ExecError_NotUnique(t *testing.T) {
	t.Parallel()
	db := &stubDB{execErr: errors.New("db down")}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	err := store.Insert(context.Background(), newRow("rE", "t", time.Now().UTC()))
	if err == nil {
		t.Fatal("expected exec error")
	}
	if errors.Is(err, outbox.ErrDuplicateIdempotencyKey) {
		t.Fatalf("err = %v; must NOT classify a plain error as duplicate", err)
	}
	if !strings.Contains(err.Error(), "outbox: Insert") {
		t.Errorf("err = %v; want outbox: Insert wrapper", err)
	}
}

// TestPostgresStore_Insert_DuplicateKeyMessage_NoSQLSTATE — the duplicate-key
// classification also accepts the "duplicate key value violates unique
// constraint" message WITHOUT the SQLSTATE 23505 prefix (exercises the second
// branch of isUniqueViolation that the 23505-based fixture skips).
func TestPostgresStore_Insert_DuplicateKeyMessage_NoSQLSTATE(t *testing.T) {
	t.Parallel()
	db := &stubDB{execErr: errors.New("ERROR: duplicate key value violates unique constraint \"outbox_events_idempotency_idx\"")}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	err := store.Insert(context.Background(), newRow("rD", "t", time.Now().UTC()))
	if err == nil {
		t.Fatal("expected unique-violation error")
	}
	if !errors.Is(err, outbox.ErrDuplicateIdempotencyKey) {
		t.Errorf("err = %v; want ErrDuplicateIdempotencyKey", err)
	}
}

func TestPostgresStore_Insert_Succeeds(t *testing.T) {
	t.Parallel()
	db := &stubDB{}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	row := newRow("rOk", "t", time.Now().UTC())
	if err := store.Insert(context.Background(), row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if len(db.execStatements) != 1 {
		t.Fatalf("Exec calls = %d; want 1", len(db.execStatements))
	}
}

func TestPostgresStore_MarkPublished_ExecError(t *testing.T) {
	t.Parallel()
	db := &stubDB{execErr: errors.New("db down")}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	err := store.MarkPublished(context.Background(), "row-X")
	if err == nil || !strings.Contains(err.Error(), "MarkPublished") {
		t.Errorf("err = %v; want MarkPublished wrapper", err)
	}
}

func TestPostgresStore_MarkFailed_ExecError(t *testing.T) {
	t.Parallel()
	db := &stubDB{execErr: errors.New("db down")}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	err := store.MarkFailed(context.Background(), "row-Y", "boom")
	if err == nil || !strings.Contains(err.Error(), "MarkFailed") {
		t.Errorf("err = %v; want MarkFailed wrapper", err)
	}
}

// stepStubDB is stubDB with per-call exec errors — lets a multi-statement
// method (Deadletter's INSERT + UPDATE) fail on a chosen statement.
type stepStubDB struct {
	*stubDB
	errs []error
	n    int
}

func (s *stepStubDB) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if s.n < len(s.errs) {
		e := s.errs[s.n]
		s.n++
		if e != nil {
			return nil, e
		}
	}
	return s.stubDB.ExecContext(ctx, q, args...)
}

func TestPostgresStore_Deadletter_InsertError(t *testing.T) {
	t.Parallel()
	db := &stepStubDB{stubDB: &stubDB{}, errs: []error{errors.New("insert dead letter boom")}}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	err := store.Deadletter(context.Background(), "row-Z", "fatal", 3)
	if err == nil || !strings.Contains(err.Error(), "Deadletter insert") {
		t.Errorf("err = %v; want Deadletter insert wrapper", err)
	}
	if len(db.execStatements) != 0 {
		t.Errorf("Exec calls = %d; want 0 (failed on first statement)", len(db.execStatements))
	}
}

func TestPostgresStore_Deadletter_UpdateError(t *testing.T) {
	t.Parallel()
	db := &stepStubDB{stubDB: &stubDB{}, errs: []error{nil, errors.New("update dead letter boom")}}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	err := store.Deadletter(context.Background(), "row-Z", "fatal", 3)
	if err == nil || !strings.Contains(err.Error(), "Deadletter update") {
		t.Errorf("err = %v; want Deadletter update wrapper", err)
	}
	if len(db.execStatements) != 1 {
		t.Errorf("Exec calls = %d; want 1 (insert succeeded before update failed)", len(db.execStatements))
	}
}

func TestPostgresStore_MarkFailed_TruncatesLongError(t *testing.T) {
	t.Parallel()
	db := &stubDB{}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	longMsg := strings.Repeat("x", 1500)
	if err := store.MarkFailed(context.Background(), "row-Y", longMsg); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if len(db.execArgs[0]) != 2 {
		t.Fatalf("args = %d; want 2", len(db.execArgs[0]))
	}
	arg, ok := db.execArgs[0][1].(string)
	if !ok {
		t.Fatalf("errMsg arg type = %T; want string", db.execArgs[0][1])
	}
	if len(arg) != 1000 {
		t.Errorf("truncated errMsg len = %d; want 1000", len(arg))
	}
}

func TestPostgresStore_Deadletter_TruncatesLongFailureReason(t *testing.T) {
	t.Parallel()
	db := &stubDB{}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	longReason := strings.Repeat("y", 1200)
	if err := store.Deadletter(context.Background(), "row-Z", longReason, 4); err != nil {
		t.Fatalf("Deadletter: %v", err)
	}
	arg, ok := db.execArgs[0][1].(string)
	if !ok {
		t.Fatalf("failure reason arg type = %T; want string", db.execArgs[0][1])
	}
	if len(arg) != 1000 {
		t.Errorf("truncated failure reason len = %d; want 1000", len(arg))
	}
}

func TestPostgresStore_FetchPending_QueryError(t *testing.T) {
	t.Parallel()
	db := &stubDB{queryErr: errors.New("query boom")}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	_, err := store.FetchPending(context.Background(), 10)
	if err == nil || !strings.Contains(err.Error(), "FetchPending") {
		t.Errorf("err = %v; want FetchPending wrapper", err)
	}
}

func TestPostgresStore_FetchPending_EnvelopeCorrupt(t *testing.T) {
	t.Parallel()
	db := &stubDB{queryRows: []fakePGRow{
		{
			id: "row-1", tenant: "t", gcid: "g", aggregateType: "booking",
			aggregateID: "b-1", eventType: "delivery.booking.confirmed",
			topic: "chora.delivery.booking.confirmed.v1", payload: []byte(`{}`),
			envelope:   "not-json{{",
			occurredAt: time.Now().UTC(),
		},
	}}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	_, err := store.FetchPending(context.Background(), 10)
	if err == nil || !strings.Contains(err.Error(), "envelope") {
		t.Errorf("err = %v; want FetchPending envelope wrapper", err)
	}
}

// scanErrRows + scanErrDB let FetchPending's rows.Scan failure branch run.
type scanErrRows struct{}

func (r *scanErrRows) Next() bool             { return true }
func (r *scanErrRows) Scan(dest ...any) error { return errors.New("scan boom") }
func (r *scanErrRows) Close() error           { return nil }
func (r *scanErrRows) Err() error             { return nil }

type scanErrDB struct{ *stubDB }

func (s *scanErrDB) QueryContext(ctx context.Context, q string, args ...any) (outbox.SQLRows, error) {
	return &scanErrRows{}, nil
}

func TestPostgresStore_FetchPending_ScanError(t *testing.T) {
	t.Parallel()
	db := &scanErrDB{stubDB: &stubDB{}}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	_, err := store.FetchPending(context.Background(), 10)
	if err == nil || !strings.Contains(err.Error(), "scan") {
		t.Errorf("err = %v; want FetchPending scan wrapper", err)
	}
}

// rowsErrStubDB returns fakeRows with a canned Err() — exercises the
// FetchPending iteration-error branch.
type rowsErrStubDB struct {
	*stubDB
	rowsErr error
}

func (s *rowsErrStubDB) QueryContext(ctx context.Context, q string, args ...any) (outbox.SQLRows, error) {
	if s.rowsErr != nil {
		return &fakeRows{rows: s.queryRows, err: s.rowsErr}, nil
	}
	return s.stubDB.QueryContext(ctx, q, args...)
}

func TestPostgresStore_FetchPending_IterationError(t *testing.T) {
	t.Parallel()
	db := &rowsErrStubDB{stubDB: &stubDB{queryRows: []fakePGRow{
		{
			id: "row-1", tenant: "t", gcid: "g", aggregateType: "booking",
			aggregateID: "b-1", eventType: "delivery.booking.confirmed",
			topic: "chora.delivery.booking.confirmed.v1", payload: []byte(`{}`),
			envelope:   `{"event_id":"row-1"}`,
			occurredAt: time.Now().UTC(),
		},
	}}, rowsErr: errors.New("iter boom")}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	_, err := store.FetchPending(context.Background(), 10)
	if err == nil || !strings.Contains(err.Error(), "iter") {
		t.Errorf("err = %v; want FetchPending iter wrapper", err)
	}
}
