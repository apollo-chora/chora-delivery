// live_poll_json_test.go — domain-level tests for the LivePoll lossless JSON
// serialization (ADR-168 gap-1b). The pg JSONB snapshot persistence relies on
// the unexported `voters` set surviving a Marshal→Unmarshal round-trip; since
// encoding/json cannot see unexported fields, LivePoll owns explicit
// MarshalJSON/UnmarshalJSON that emits `voters` as a JSON array of learner_gcids
// and rehydrates it back into the set. These tests pin that contract from
// WITHIN the domain package (where the unexported field IS observable).
package classroom

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestLivePollJSON_VotersSurviveRoundTrip(t *testing.T) {
	p, err := NewLivePoll(NewLivePollInput{
		TenantID:       "t-1",
		InstructorGCID: "i-1",
		Question:       "Pace?",
		Options:        []string{"slow", "ok", "fast"},
	})
	if err != nil {
		t.Fatalf("NewLivePoll: %v", err)
	}
	now := time.Now().UTC()
	_ = p.Open(now)
	_ = p.CastVote("learner-A", "ok", now)
	_ = p.CastVote("learner-B", "fast", now)

	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The wire form MUST carry a voters array (otherwise it is silently lossy).
	if !strings.Contains(string(raw), `"voters"`) {
		t.Fatalf("marshalled LivePoll omits voters array: %s", raw)
	}

	var got LivePoll
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.voters) != 2 {
		t.Fatalf("voter set count lossy: want 2 got %d (%v)", len(got.voters), got.voters)
	}
	if _, ok := got.voters["learner-A"]; !ok {
		t.Error("voter learner-A missing post-round-trip")
	}
	if _, ok := got.voters["learner-B"]; !ok {
		t.Error("voter learner-B missing post-round-trip")
	}
}

func TestLivePollJSON_VotersArrayContents(t *testing.T) {
	p, _ := NewLivePoll(NewLivePollInput{
		TenantID: "t-1", InstructorGCID: "i-1", Question: "Q", Options: []string{"a", "b"},
	})
	now := time.Now().UTC()
	_ = p.Open(now)
	_ = p.CastVote("g1", "a", now)
	_ = p.CastVote("g2", "b", now)

	raw, _ := json.Marshal(p)
	var wire struct {
		Voters []string `json:"voters"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	sort.Strings(wire.Voters)
	if len(wire.Voters) != 2 || wire.Voters[0] != "g1" || wire.Voters[1] != "g2" {
		t.Fatalf("voters array = %v, want [g1 g2]", wire.Voters)
	}
}

func TestLivePollJSON_EmptyVotersRoundTrip(t *testing.T) {
	p, _ := NewLivePoll(NewLivePollInput{
		TenantID: "t-1", InstructorGCID: "i-1", Question: "Q", Options: []string{"a", "b"},
	})
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got LivePoll
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.voters == nil {
		t.Fatal("voters set is nil post-unmarshal; CastVote would still self-heal but the snapshot should rehydrate a non-nil set")
	}
	if len(got.voters) != 0 {
		t.Fatalf("empty-poll voters should be empty, got %v", got.voters)
	}
	// All other fields intact.
	if got.ID != p.ID || got.State != p.State || len(got.Options) != 2 {
		t.Fatalf("empty-poll round-trip lossy: %#v", got)
	}
}

// TestLivePollJSON_LegacyDataNoVotersField guards forward-compat: a snapshot
// written before MarshalJSON existed (no `voters` key) must unmarshal cleanly
// with an empty-but-non-nil voter set (CastVote then self-heals as before).
func TestLivePollJSON_LegacyDataNoVotersField(t *testing.T) {
	legacy := `{"id":"x","tenant_id":"t","instructor_gcid":"i","question":"Q",
		"options":[{"label":"a","vote_count":0},{"label":"b","vote_count":0}],
		"state":"OPEN","created_at":"2026-05-29T08:00:00Z","updated_at":"2026-05-29T08:00:00Z"}`
	var got LivePoll
	if err := json.Unmarshal([]byte(legacy), &got); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if got.voters == nil {
		t.Fatal("legacy snapshot should rehydrate a non-nil empty voter set")
	}
	if err := got.CastVote("g1", "a", time.Now().UTC()); err != nil {
		t.Fatalf("CastVote on legacy-rehydrated poll: %v", err)
	}
}
