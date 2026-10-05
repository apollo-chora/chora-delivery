// live_poll_repo_test.go — adapter coverage for the in-memory LivePoll repo
// (R+ Wave-6 M11). TDD backfill per feedback_strict_tdd (60% adapter gate).
package inmem

import (
	"strings"
	"sync"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

func mustNewPoll(t *testing.T, tenant, question string) *classroom.LivePoll {
	t.Helper()
	p, err := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID:       tenant,
		InstructorGCID: "11111111-1111-7111-8111-111111111111",
		Question:       question,
		Options:        []string{"A", "B", "C"},
	})
	if err != nil {
		t.Fatalf("NewLivePoll err: %v", err)
	}
	return p
}

func TestLivePollRepo_SaveGet(t *testing.T) {
	r := NewLivePollRepo()
	p := mustNewPoll(t, "tenant-1", "Favourite ceremony?")
	r.Save(p)
	got, ok, _ := r.Get(p.ID)
	if !ok || got.ID != p.ID {
		t.Fatalf("want saved poll back; ok=%v", ok)
	}
}

func TestLivePollRepo_GetMissReturnsFalse(t *testing.T) {
	r := NewLivePollRepo()
	if _, ok, _ := r.Get("nope"); ok {
		t.Fatalf("want ok=false for missing id")
	}
}

func TestLivePollRepo_SaveNilNoop(t *testing.T) {
	r := NewLivePollRepo()
	r.Save(nil) // must not panic
	if got := r.ListByTenant("tenant-1"); len(got) != 0 {
		t.Fatalf("want empty after nil save; got %d", len(got))
	}
}

func TestLivePollRepo_ListByTenantSortedAndScoped(t *testing.T) {
	r := NewLivePollRepo()
	p1 := mustNewPoll(t, "tenant-1", "q1")
	p2 := mustNewPoll(t, "tenant-1", "q2")
	other := mustNewPoll(t, "tenant-2", "q3")
	r.Save(p2)
	r.Save(p1)
	r.Save(other)

	got := r.ListByTenant("tenant-1")
	if len(got) != 2 {
		t.Fatalf("want 2 polls for tenant-1; got %d", len(got))
	}
	// sorted ascending by ID (UUIDv7 ⇒ creation order)
	if strings.Compare(got[0].ID, got[1].ID) >= 0 {
		t.Fatalf("ListByTenant must be sorted ascending by ID")
	}
	for _, p := range got {
		if p.TenantID != "tenant-1" {
			t.Fatalf("tenant scoping leaked: %s", p.TenantID)
		}
	}
}

func TestLivePollRepo_ConcurrentSaveGet(t *testing.T) {
	r := NewLivePollRepo()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := mustNewPoll(t, "tenant-1", "q")
			r.Save(p)
			_, _, _ = r.Get(p.ID)
			_ = r.ListByTenant("tenant-1")
		}()
	}
	wg.Wait()
	if got := r.ListByTenant("tenant-1"); len(got) != 50 {
		t.Fatalf("want 50 polls after concurrent saves; got %d", len(got))
	}
}
