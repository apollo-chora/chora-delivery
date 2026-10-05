// user_directory_test.go — unit tests for pg.UserDirectoryRepo (Q3 name
// projection, delivery side). Mirrors enrollment_test.go: a stub Querier
// exercises the SQL surface + arg binding without a live DB. Reuses the
// stubQuerier / stubRow / stubRows / stubTxRunner defined in application_test.go
// (same pg_test package).
//
// What these guarantee:
//  1. Nil-tx returns ErrNotImplemented (fail-loud, matches sibling repos).
//  2. Upsert emits SQLUpsertUserDirectory with (gcid, display_name, email,
//     updated_at) in order — and NO rls.ApplySession runs first (the directory
//     is a tenant-agnostic, RLS-free global table).
//  3. Empty gcid is rejected at the boundary (loud sentinel).
//  4. LookupNames short-circuits on empty input (no query) and maps rows on a hit.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
)

func TestUserDirectoryRepo_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewUserDirectoryRepo(nil)
	if err := r.Upsert(context.Background(), directory.UserDirectoryEntry{GCID: gcid, DisplayName: "x", UpdatedAt: time.Now()}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Upsert: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.LookupNames(context.Background(), []string{gcid}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("LookupNames: expected ErrNotImplemented; got %v", err)
	}
}

func TestUserDirectoryRepo_Upsert_EmitsUpsertSQLWithArgs(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewUserDirectoryRepo(tx)

	ts := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	if err := r.Upsert(context.Background(), directory.UserDirectoryEntry{
		GCID: gcid, DisplayName: "Alice", Email: "a@x.com", UpdatedAt: ts,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if len(q.sqls) != 1 {
		t.Fatalf("expected exactly 1 SQL statement (no rls.ApplySession — global table); got %d: %v", len(q.sqls), q.sqls)
	}
	if !strings.Contains(q.sqls[0], "INSERT INTO user_directory") || !strings.Contains(q.sqls[0], "ON CONFLICT (gcid)") {
		t.Fatalf("unexpected upsert SQL: %s", q.sqls[0])
	}
	if !strings.Contains(q.sqls[0], "user_directory.updated_at < EXCLUDED.updated_at") {
		t.Fatalf("upsert SQL missing last-writer-wins guard: %s", q.sqls[0])
	}
	args := q.args[0]
	if len(args) != 4 {
		t.Fatalf("expected 4 bind args; got %d: %v", len(args), args)
	}
	if args[0] != gcid {
		t.Errorf("arg[0] gcid = %v, want %s", args[0], gcid)
	}
	if args[1] != "Alice" {
		t.Errorf("arg[1] display_name = %v, want Alice", args[1])
	}
	if args[2] != "a@x.com" {
		t.Errorf("arg[2] email = %v, want a@x.com", args[2])
	}
	if args[3] != ts {
		t.Errorf("arg[3] updated_at = %v, want %v", args[3], ts)
	}
}

func TestUserDirectoryRepo_Upsert_EmptyEmailBindsNil(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewUserDirectoryRepo(&stubTxRunner{q: q})
	if err := r.Upsert(context.Background(), directory.UserDirectoryEntry{GCID: gcid, DisplayName: "Alice", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got := q.args[0][2]; got != nil {
		t.Errorf("empty email should bind SQL NULL (nil arg); got %v", got)
	}
}

func TestUserDirectoryRepo_Upsert_RejectsEmptyGCID(t *testing.T) {
	t.Parallel()
	r := pg.NewUserDirectoryRepo(&stubTxRunner{q: &stubQuerier{}})
	if err := r.Upsert(context.Background(), directory.UserDirectoryEntry{GCID: "", DisplayName: "x", UpdatedAt: time.Now()}); !errors.Is(err, pg.ErrUserDirectoryMissingGCID) {
		t.Fatalf("expected ErrUserDirectoryMissingGCID; got %v", err)
	}
}

func TestUserDirectoryRepo_LookupNames_EmptyInput_NoQuery(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewUserDirectoryRepo(&stubTxRunner{q: q})
	names, err := r.LookupNames(context.Background(), nil)
	if err != nil {
		t.Fatalf("LookupNames nil: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected empty map; got %v", names)
	}
	if len(q.sqls) != 0 {
		t.Errorf("empty input should short-circuit before any query; ran %v", q.sqls)
	}
}

func TestUserDirectoryRepo_LookupNames_MapsRows(t *testing.T) {
	t.Parallel()
	rows := []func(dest ...any) error{
		func(dest ...any) error { *(dest[0].(*string)) = "g1"; *(dest[1].(*string)) = "Alice"; return nil },
		func(dest ...any) error { *(dest[0].(*string)) = "g2"; *(dest[1].(*string)) = "Bob"; return nil },
	}
	q := &stubQuerier{
		rowsFn: func(_ string, _ ...any) (pg.Rows, error) { return &stubRows{rows: rows}, nil },
	}
	r := pg.NewUserDirectoryRepo(&stubTxRunner{q: q})

	names, err := r.LookupNames(context.Background(), []string{"g1", "g2"})
	if err != nil {
		t.Fatalf("LookupNames: %v", err)
	}
	if names["g1"] != "Alice" || names["g2"] != "Bob" {
		t.Errorf("mapped names = %v, want {g1:Alice, g2:Bob}", names)
	}
	if len(q.sqls) != 1 || !strings.Contains(q.sqls[0], "FROM user_directory") || !strings.Contains(q.sqls[0], "gcid = ANY(") {
		t.Fatalf("unexpected lookup SQL: %v", q.sqls)
	}
}
