// live_poll_roundtrip_test.go — local (no-DB) verification that the JSONB
// snapshot persistence is LOSSLESS for the LivePoll aggregate (ADR-168 gap-1b).
//
// Unlike LiveQuiz/LiveQuizSession (all fields exported → trivially lossless),
// LivePoll carries an UNEXPORTED `voters map[string]struct{}` set that drives
// first-vote-wins idempotency. A naive json.Marshal would DROP it (the field is
// unexported → invisible to encoding/json), so a poll rehydrated on a non-owning
// pod would let an already-voted learner vote AGAIN — silent correctness loss.
// That is exactly why LivePoll was DEFERRED from gap-1.
//
// This asserts the LivePoll.MarshalJSON / UnmarshalJSON path (which the pg
// `data` JSONB snapshot rides on, identically to live_classroom.go) round-trips
// `voters` losslessly: count + membership survive, AND the rehydrated set still
// rejects a duplicate vote and accepts a fresh one. The SQL execution itself is
// covered by the DSN-gated integration_test.go in CI.
package pg

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// buildVotedPoll constructs an OPEN LivePoll with 2 distinct voters recorded.
func buildVotedPoll(t *testing.T) *classroom.LivePoll {
	t.Helper()
	p, err := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID:       "019e2f93-d586-71b5-8c3d-e2b0d0d50300",
		CourseID:       "019e2f93-d586-71b5-8c3d-e2b0d0d50400",
		InstructorGCID: "019e2f93-d586-71b5-8c3d-e2b0d0d50310",
		Question:       "How was the pace?",
		Options:        []string{"Too slow", "Just right", "Too fast"},
	})
	if err != nil {
		t.Fatalf("NewLivePoll: %v", err)
	}
	now := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)
	if err := p.Open(now); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := p.CastVote("019e2f93-d586-71b5-8c3d-e2b0d0d50320", "Just right", now.Add(time.Second)); err != nil {
		t.Fatalf("CastVote learner-1: %v", err)
	}
	if err := p.CastVote("019e2f93-d586-71b5-8c3d-e2b0d0d50321", "Too fast", now.Add(2*time.Second)); err != nil {
		t.Fatalf("CastVote learner-2: %v", err)
	}
	return p
}

func TestJSONBRoundTrip_LivePoll_VotersLossless(t *testing.T) {
	p := buildVotedPoll(t)

	// Round-trip through the SAME json path the pg `data` column uses.
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got classroom.LivePoll
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Exported scalar fields survive.
	if got.ID != p.ID || got.TenantID != p.TenantID || got.CourseID != p.CourseID ||
		got.InstructorGCID != p.InstructorGCID || got.Question != p.Question || got.State != p.State {
		t.Fatalf("scalar fields lossy:\n want %#v\n  got %#v", *p, got)
	}
	// Options + tallies survive.
	if len(got.Options) != len(p.Options) {
		t.Fatalf("options length lossy: want %d got %d", len(p.Options), len(got.Options))
	}
	for i := range p.Options {
		if got.Options[i] != p.Options[i] {
			t.Fatalf("option[%d] lossy: want %#v got %#v", i, p.Options[i], got.Options[i])
		}
	}
	if got.TotalVotes() != 2 {
		t.Fatalf("total votes lossy: want 2 got %d", got.TotalVotes())
	}

	// Timestamps survive.
	if (got.OpenedAt == nil) != (p.OpenedAt == nil) ||
		(got.OpenedAt != nil && !got.OpenedAt.Equal(*p.OpenedAt)) {
		t.Fatalf("opened_at lossy: want %v got %v", p.OpenedAt, got.OpenedAt)
	}
	if !got.CreatedAt.Equal(p.CreatedAt) || !got.UpdatedAt.Equal(p.UpdatedAt) {
		t.Fatalf("created/updated lossy")
	}

	// THE crux: the unexported voter set survived. A behavioural assertion is
	// the only way to observe it from another package — a previously-voted
	// learner must STILL be rejected, and the option tally must NOT double-count.
	dupErr := got.CastVote("019e2f93-d586-71b5-8c3d-e2b0d0d50320", "Too slow", time.Now().UTC())
	if dupErr != classroom.ErrLivePollDuplicateVote {
		t.Fatalf("voter set NOT rehydrated — duplicate vote allowed: err=%v", dupErr)
	}
	if got.TotalVotes() != 2 {
		t.Fatalf("duplicate vote was counted post-rehydrate: total=%d", got.TotalVotes())
	}

	// A genuinely new learner can still vote (the set isn't frozen-closed).
	if err := got.CastVote("019e2f93-d586-71b5-8c3d-e2b0d0d50322", "Too slow", time.Now().UTC()); err != nil {
		t.Fatalf("fresh learner vote rejected post-rehydrate: %v", err)
	}
	if got.TotalVotes() != 3 {
		t.Fatalf("fresh vote not counted post-rehydrate: total=%d", got.TotalVotes())
	}
}

// nilTxRunner fails LOUD for the LivePoll pg repo: Save AND Get both return
// ErrNotImplemented (mirrors TestNilTxRunner_FailSafe). An unwired adapter must
// not masquerade as an empty database (CHO-2184).
func TestNilTxRunner_FailSafe_LivePoll(t *testing.T) {
	lp := NewLivePollRepo(nil)
	if err := lp.Save(&classroom.LivePoll{ID: "x"}); err != ErrNotImplemented {
		t.Errorf("nil-tx LivePoll.Save = %v, want ErrNotImplemented", err)
	}
	if _, ok, err := lp.Get("x"); ok || err != ErrNotImplemented {
		t.Errorf("nil-tx LivePoll.Get = (ok=%v, err=%v), want (false, ErrNotImplemented)", ok, err)
	}
	if got := lp.ListByTenant("t"); got != nil {
		t.Errorf("nil-tx LivePoll.ListByTenant = %v, want nil", got)
	}
}
