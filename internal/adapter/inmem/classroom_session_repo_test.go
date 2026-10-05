// classroom_session_repo_test.go — direct unit tests for the in-memory
// ClassroomSessionRepo (R+ Stage C-lite). Pins the ARMED|LIVE join-code
// lookup (newest-ID wins on collision), Mutate's write-lock semantics +
// ErrSessionNotFound, and the tenant-scoped sorted listing.
package inmem_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

const (
	sessQuizID       = "019e2f93-d586-71b5-8c3d-e2b0d0d5ee01"
	sessInstructorID = "019e2f93-d586-71b5-8c3d-e2b0d0d5ee02"
)

func mustSession(t *testing.T, tenantID string) *classroom.LiveQuizSession {
	t.Helper()
	s, err := classroom.NewLiveQuizSession(sessQuizID, tenantID, sessInstructorID)
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	return s
}

func TestClassroomSessionRepo_SaveGet(t *testing.T) {
	t.Parallel()
	r := inmem.NewClassroomSessionRepo()
	s := mustSession(t, tenantA)
	if err := r.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.Get(s.ID)
	if err != nil || !ok {
		t.Fatalf("Get: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != s.ID {
		t.Fatalf("round-trip mismatch")
	}
	if _, ok, _ := r.Get("nope"); ok {
		t.Fatal("Get: expected miss for unknown id")
	}
}

func TestClassroomSessionRepo_SaveNilIsNoop(t *testing.T) {
	t.Parallel()
	r := inmem.NewClassroomSessionRepo()
	if err := r.Save(nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	if len(r.ListByTenant(tenantA)) != 0 {
		t.Fatal("store must stay empty after Save(nil)")
	}
}

func TestClassroomSessionRepo_GetByJoinCode(t *testing.T) {
	t.Parallel()
	r := inmem.NewClassroomSessionRepo()

	// Empty join code is a short-circuit miss.
	if _, ok, _ := r.GetByJoinCode(tenantA, ""); ok {
		t.Fatal("empty join code must be a miss")
	}

	armed := mustSession(t, tenantA)
	armed.JoinCode = "ABC123"
	if err := r.Save(armed); err != nil {
		t.Fatalf("Save armed: %v", err)
	}
	now := time.Now().UTC()
	live := mustSession(t, tenantA)
	live.JoinCode = "XYZ789"
	if err := live.Start(now); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Save(live); err != nil {
		t.Fatalf("Save live: %v", err)
	}

	got, ok, err := r.GetByJoinCode(tenantA, "ABC123")
	if err != nil || !ok {
		t.Fatalf("GetByJoinCode ARMED: ok=%v err=%v", ok, err)
	}
	if got.ID != armed.ID {
		t.Fatalf("expected armed session, got %s", got.ID)
	}
	got, ok, _ = r.GetByJoinCode(tenantA, "XYZ789")
	if !ok || got.ID != live.ID {
		t.Fatalf("expected live session, got %s ok=%v", got.ID, ok)
	}

	// Wrong tenant / unknown code are misses.
	if _, ok, _ := r.GetByJoinCode(tenantB, "ABC123"); ok {
		t.Fatal("cross-tenant join code must be a miss")
	}
	if _, ok, _ := r.GetByJoinCode(tenantA, "NOPE99"); ok {
		t.Fatal("unknown join code must be a miss")
	}
}

// A CLOSED session with a join code must NOT resolve — only ARMED|LIVE are
// reachable via the lobby code.
func TestClassroomSessionRepo_GetByJoinCode_ClosedIsMiss(t *testing.T) {
	t.Parallel()
	r := inmem.NewClassroomSessionRepo()
	s := mustSession(t, tenantA)
	s.JoinCode = "CLSD00"
	s.State = classroom.LiveQuizSessionStateClosed
	if err := r.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, ok, _ := r.GetByJoinCode(tenantA, "CLSD00"); ok {
		t.Fatal("CLOSED session must not resolve via join code")
	}
}

// Two active sessions sharing a join code (an improbable collision) resolve
// the NEWEST id (UUIDv7 ⇒ creation order).
func TestClassroomSessionRepo_GetByJoinCode_NewestIDWins(t *testing.T) {
	t.Parallel()
	r := inmem.NewClassroomSessionRepo()
	older := mustSession(t, tenantA)
	older.JoinCode = "TIE001"
	time.Sleep(2 * time.Millisecond) // monotonic UUIDv7 spread — newer strictly after older
	newer := mustSession(t, tenantA)
	newer.JoinCode = "TIE001"
	// Save out of order — resolution must still pick the newest by ID.
	if err := r.Save(newer); err != nil {
		t.Fatalf("Save newer: %v", err)
	}
	if err := r.Save(older); err != nil {
		t.Fatalf("Save older: %v", err)
	}
	got, ok, err := r.GetByJoinCode(tenantA, "TIE001")
	if err != nil || !ok {
		t.Fatalf("GetByJoinCode: ok=%v err=%v", ok, err)
	}
	if got.ID != newer.ID {
		t.Fatalf("expected newest session %s, got %s", newer.ID, got.ID)
	}
}

func TestClassroomSessionRepo_Mutate(t *testing.T) {
	t.Parallel()
	r := inmem.NewClassroomSessionRepo()
	s := mustSession(t, tenantA)
	if err := r.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Success path: mutate under the write lock and persist the change.
	now := time.Now().UTC()
	err := r.Mutate(s.ID, func(cur *classroom.LiveQuizSession) error {
		return cur.Start(now)
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	got, ok, _ := r.Get(s.ID)
	if !ok || got.State != classroom.LiveQuizSessionStateLive {
		t.Fatalf("Mutate did not persist the state change; state=%s ok=%v", got.State, ok)
	}

	// Unknown id → ErrSessionNotFound.
	if err := r.Mutate("missing", func(*classroom.LiveQuizSession) error { return nil }); !errors.Is(err, classroom.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}

	// A failing fn propagates (the inmem analogue of a rolled-back txn).
	want := errors.New("boom")
	if err := r.Mutate(s.ID, func(*classroom.LiveQuizSession) error { return want }); !errors.Is(err, want) {
		t.Fatalf("expected fn error to propagate, got %v", err)
	}
}

func TestClassroomSessionRepo_ListByTenant(t *testing.T) {
	t.Parallel()
	r := inmem.NewClassroomSessionRepo()
	if err := r.Save(mustSession(t, tenantA)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := r.Save(mustSession(t, tenantA)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := r.Save(mustSession(t, tenantB)); err != nil {
		t.Fatalf("Save other tenant: %v", err)
	}

	out := r.ListByTenant(tenantA)
	if len(out) != 2 {
		t.Fatalf("expected 2 sessions for tenantA; got %d", len(out))
	}
	for _, s := range out {
		if s.TenantID != tenantA {
			t.Errorf("cross-tenant leak: tenant=%q", s.TenantID)
		}
	}
	if strings.Compare(out[0].ID, out[1].ID) >= 0 {
		t.Fatalf("ListByTenant must be sorted ascending by ID")
	}
}
