// closure_repo_topup_test.go — idempotent-replay branch of the in-memory
// closure repo (a second Pseudonymise for the same (tenant, gcid) pair must
// short-circuit to 0 rows — the inbox dedupes the subscriber-level replay, so
// only a direct repo call reaches this path) + the row-count accumulation.
package events_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/config"
)

func twoTableSpec() []config.TableSpec {
	return []config.TableSpec{
		{Table: "t1", Columns: []config.ColumnSpec{
			{Column: "c1", Strategy: "drop"},
			{Column: "c2", Strategy: "tombstone_string", Value: "Former member"},
		}},
		{Table: "t2", Columns: []config.ColumnSpec{
			{Column: "c3", Strategy: "tombstone_string", Value: "Former member"},
		}},
	}
}

func TestInMemoryClosureRepo_Pseudonymise_ReplayShortCircuits(t *testing.T) {
	repo := events.NewInMemoryClosureRepo()
	ctx := context.Background()

	if rows, err := repo.Pseudonymise(ctx, "tenant-1", "gcid-1", twoTableSpec()); err != nil || rows != 3 {
		t.Fatalf("first call = (%d, %v), want (3, nil)", rows, err)
	}
	// Second call for the same pair — already pseudonymised -> 0 rows.
	if rows, err := repo.Pseudonymise(ctx, "tenant-1", "gcid-1", twoTableSpec()); err != nil || rows != 0 {
		t.Fatalf("replay call = (%d, %v), want short-circuit (0, nil)", rows, err)
	}

	// A DIFFERENT tenant with the same gcid is NOT short-circuited.
	if rows, err := repo.Pseudonymise(ctx, "tenant-2", "gcid-1", twoTableSpec()); err != nil || rows != 3 {
		t.Fatalf("other-tenant call = (%d, %v), want fresh (3, nil)", rows, err)
	}
	got, err := repo.IsPseudonymised(ctx, "tenant-2", "gcid-1")
	if err != nil || !got {
		t.Fatalf("tenant-2 must be pseudonymised: (got=%v, err=%v)", got, err)
	}
}
