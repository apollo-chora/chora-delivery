// franchise_satellite_repo_test.go: contract tests for the in-memory
// franchise.Store (CHO-2230). The inmem adapter must mirror the pg adapter's
// semantics exactly (live-duplicate refusal, revoked rows read as misses,
// owner-scoped listing) so dev behaviour never diverges from prod.
package inmem_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/franchise"
)

const (
	fsOwner     = "019e2f93-d586-71b5-8c3d-e2b0d0d50001"
	fsSatellite = "019e2f93-d586-71b5-8c3d-e2b0d0d50002"
	fsOther     = "019e2f93-d586-71b5-8c3d-e2b0d0d50003"
	fsGCID      = "019e2f93-d586-71b5-8c3d-e2b0d0d5ad01"
)

func mustMapping(t *testing.T, owner, satellite string) *franchise.FranchiseSatellite {
	t.Helper()
	m, err := franchise.New(owner, satellite, fsGCID)
	if err != nil {
		t.Fatalf("franchise.New: %v", err)
	}
	return m
}

func TestFranchiseSatelliteRepo_SaveGetList(t *testing.T) {
	t.Parallel()
	r := inmem.NewFranchiseSatelliteRepo()
	ctx := context.Background()
	m := mustMapping(t, fsOwner, fsSatellite)
	if err := r.Save(ctx, m); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok, err := r.Get(ctx, m.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.SatelliteTenantID != fsSatellite {
		t.Errorf("Get: %+v", got)
	}

	// Live duplicate pair refused (mirrors the pg partial unique index).
	dup := mustMapping(t, fsOwner, fsSatellite)
	if err := r.Save(ctx, dup); !errors.Is(err, franchise.ErrDuplicateMapping) {
		t.Fatalf("Save dup: want ErrDuplicateMapping, got %v", err)
	}

	// Listing is owner-scoped.
	other := mustMapping(t, fsOther, fsSatellite)
	if err := r.Save(ctx, other); err != nil {
		t.Fatalf("Save other: %v", err)
	}
	items, err := r.ListByOwner(ctx, fsOwner)
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(items) != 1 || items[0].ID != m.ID {
		t.Fatalf("ListByOwner: %+v", items)
	}
}

func TestFranchiseSatelliteRepo_RevokeSemantics(t *testing.T) {
	t.Parallel()
	r := inmem.NewFranchiseSatelliteRepo()
	ctx := context.Background()
	m := mustMapping(t, fsOwner, fsSatellite)
	if err := r.Save(ctx, m); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ok, err := r.Revoke(ctx, m.ID, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("Revoke: ok=%v err=%v", ok, err)
	}

	// A revoked row reads as a genuine miss everywhere.
	if _, ok, _ := r.Get(ctx, m.ID); ok {
		t.Fatal("Get after revoke: must be a miss")
	}
	items, _ := r.ListByOwner(ctx, fsOwner)
	if len(items) != 0 {
		t.Fatalf("ListByOwner after revoke: %+v", items)
	}
	if ok, _ := r.Revoke(ctx, m.ID, time.Now().UTC()); ok {
		t.Fatal("second Revoke: must be a miss")
	}

	// Revoke then re-grant works (uniqueness is over LIVE rows only).
	again := mustMapping(t, fsOwner, fsSatellite)
	if err := r.Save(ctx, again); err != nil {
		t.Fatalf("Save after revoke: %v", err)
	}
}

func TestFranchiseSatelliteRepo_UnknownRevoke_IsMiss(t *testing.T) {
	t.Parallel()
	r := inmem.NewFranchiseSatelliteRepo()
	if ok, err := r.Revoke(context.Background(), "absent", time.Now()); ok || err != nil {
		t.Fatalf("Revoke absent: ok=%v err=%v", ok, err)
	}
}
