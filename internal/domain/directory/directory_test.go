// directory_test.go — unit tests for the InMemUserDirectory projection.
//
// Covers: Upsert-then-LookupNames round-trip, last-writer-wins on UpdatedAt
// (older + equal-timestamp writes ignored, strictly-newer applied), empty-GCID
// rejection (fail loud), and the empty-input LookupNames contract.
package directory_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
)

func TestInMemUserDirectory_UpsertThenLookup(t *testing.T) {
	t.Parallel()
	d := directory.NewInMemUserDirectory()
	ctx := context.Background()
	now := time.Now().UTC()

	if err := d.Upsert(ctx, directory.UserDirectoryEntry{
		GCID: "g1", DisplayName: "Alice", Email: "a@x.com", UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	names, err := d.LookupNames(ctx, []string{"g1", "g2"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if names["g1"] != "Alice" {
		t.Errorf("g1 display_name = %q, want Alice", names["g1"])
	}
	if _, ok := names["g2"]; ok {
		t.Errorf("g2 has no entry — should be absent from the map")
	}
}

func TestInMemUserDirectory_LastWriterWins(t *testing.T) {
	t.Parallel()
	d := directory.NewInMemUserDirectory()
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)

	// Newer write first.
	if err := d.Upsert(ctx, directory.UserDirectoryEntry{GCID: "g1", DisplayName: "New", UpdatedAt: t1}); err != nil {
		t.Fatalf("upsert new: %v", err)
	}
	// Older write — must be IGNORED (a stale/out-of-order redelivery).
	if err := d.Upsert(ctx, directory.UserDirectoryEntry{GCID: "g1", DisplayName: "Old", UpdatedAt: t0}); err != nil {
		t.Fatalf("upsert old: %v", err)
	}
	if got := mustName(t, d, "g1"); got != "New" {
		t.Errorf("older write clobbered newer; g1 = %q, want New", got)
	}

	// Equal-timestamp replay — no-op (idempotent, matches SQL `<` strict guard).
	if err := d.Upsert(ctx, directory.UserDirectoryEntry{GCID: "g1", DisplayName: "Replay", UpdatedAt: t1}); err != nil {
		t.Fatalf("upsert replay: %v", err)
	}
	if got := mustName(t, d, "g1"); got != "New" {
		t.Errorf("equal-timestamp replay overwrote; g1 = %q, want New", got)
	}

	// Strictly newer — applied.
	if err := d.Upsert(ctx, directory.UserDirectoryEntry{GCID: "g1", DisplayName: "Newest", UpdatedAt: t1.Add(time.Hour)}); err != nil {
		t.Fatalf("upsert newest: %v", err)
	}
	if got := mustName(t, d, "g1"); got != "Newest" {
		t.Errorf("newer write not applied; g1 = %q, want Newest", got)
	}
}

func TestInMemUserDirectory_Upsert_RejectsEmptyGCID(t *testing.T) {
	t.Parallel()
	d := directory.NewInMemUserDirectory()
	if err := d.Upsert(context.Background(), directory.UserDirectoryEntry{
		GCID: "", DisplayName: "x", UpdatedAt: time.Now(),
	}); err == nil {
		t.Fatal("expected error for empty gcid, got nil")
	}
}

func TestInMemUserDirectory_LookupNames_EmptyInput(t *testing.T) {
	t.Parallel()
	d := directory.NewInMemUserDirectory()
	names, err := d.LookupNames(context.Background(), nil)
	if err != nil {
		t.Fatalf("lookup nil: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected empty map for nil input, got %d entries", len(names))
	}
}

func mustName(t *testing.T, d *directory.InMemUserDirectory, gcid string) string {
	t.Helper()
	names, err := d.LookupNames(context.Background(), []string{gcid})
	if err != nil {
		t.Fatalf("lookup %s: %v", gcid, err)
	}
	return names[gcid]
}
