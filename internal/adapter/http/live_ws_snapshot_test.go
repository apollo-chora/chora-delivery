// live_ws_snapshot_test.go — ADR-168 §2: WS snapshot-on-connect must read the
// authoritative per-vote counts (and, for quizzes, the cumulative leaderboard)
// from the cross-pod realtime stores, NOT the pod-local aggregate.
//
// Scenario: a vote/response is recorded on pod A → it lands in the Redis
// TallyStore + LeaderboardStore (cross-pod) but is NOT written back into the
// session/poll aggregate per-vote. A client then connects to pod B, whose
// in-mem aggregate has zero responses. The snapshot it receives MUST still
// reflect the live counts + board (sourced from the realtime stores), else a
// late joiner on a non-mutating pod renders a blank tally until the next event.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/realtime"
	wsadapter "github.com/apollo-chora/chora-delivery/internal/adapter/ws"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

func TestLiveQuizWSSnapshot_FromTallyAndLeaderboard(t *testing.T) {
	repo := newFakeLiveQuizSessionRepo()
	broker := wsadapter.NewBroker(0)
	tally, lb := realtime.NewMemTally(), realtime.NewMemLeaderboard()
	mux := http.NewServeMux()
	mux.Handle("/api/v1/live-quizzes/", httpapi.LiveQuizWSHandler(repo, nil, broker, tally, lb))
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { broker.Close(); srv.Close() })

	sess, err := classroom.NewLiveQuizSession(
		"019e3000-0000-7000-8000-000000000901",
		"019e3000-0000-7000-8000-000000000902",
		"019e3000-0000-7000-9000-000000000903",
	)
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	_ = sess.Start(time.Now().UTC())
	// The current question is live, but THIS pod recorded no responses
	// (they happened on another pod → only Redis has them).
	if err := sess.AdvanceTo("q1", time.Now().UTC()); err != nil {
		t.Fatalf("AdvanceTo: %v", err)
	}
	repo.Save(sess)

	ctx := context.Background()
	_, _ = tally.Incr(ctx, sess.ID, "q1", "Sprint Planning")
	_, _ = tally.Incr(ctx, sess.ID, "q1", "Sprint Planning")
	_, _ = tally.Incr(ctx, sess.ID, "q1", "Daily Scrum")
	_, _ = lb.Credit(ctx, sess.ID, "019e3000-0000-7000-9000-00000000aaaa", 950)

	ws := dialLiveQuiz(t, srv.URL, sess.ID, sess.TenantID)
	defer ws.Close()
	snap := receiveEnvelope(t, ws, 2*time.Second)

	payloadJSON, _ := json.Marshal(snap.Payload)
	var p struct {
		ResponseCounts map[string]map[string]int   `json:"response_counts"`
		Leaderboard    []realtime.LeaderboardEntry `json:"leaderboard"`
	}
	_ = json.Unmarshal(payloadJSON, &p)
	if got := p.ResponseCounts["q1"]["Sprint Planning"]; got != 2 {
		t.Fatalf("snapshot counts from tally: q1/Sprint Planning = %d, want 2 (payload=%s)", got, payloadJSON)
	}
	if got := p.ResponseCounts["q1"]["Daily Scrum"]; got != 1 {
		t.Fatalf("snapshot counts from tally: q1/Daily Scrum = %d, want 1", got)
	}
	if len(p.Leaderboard) != 1 || p.Leaderboard[0].Score != 950 {
		t.Fatalf("snapshot leaderboard from store = %#v, want one entry score 950", p.Leaderboard)
	}
}

func TestLivePollWSSnapshot_FromTally(t *testing.T) {
	repo := newFakeLivePollRepo()
	broker := wsadapter.NewBroker(0)
	tally := realtime.NewMemTally()
	mux := http.NewServeMux()
	mux.Handle("/api/v1/live-polls/", httpapi.LivePollWSHandler(repo, broker, tally))
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { broker.Close(); srv.Close() })

	poll, err := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID:       "019e3000-0000-7000-8000-000000000902",
		InstructorGCID: "019e3000-0000-7000-9000-000000000903",
		Question:       "How is the pace?",
		Options:        []string{"Too slow", "Just right", "Too fast"},
	})
	if err != nil {
		t.Fatalf("NewLivePoll: %v", err)
	}
	_ = poll.Open(time.Now().UTC())
	repo.Save(poll) // aggregate has zero votes on this pod

	ctx := context.Background()
	_, _ = tally.Incr(ctx, poll.ID, "_poll", "Just right")
	_, _ = tally.Incr(ctx, poll.ID, "_poll", "Just right")
	_, _ = tally.Incr(ctx, poll.ID, "_poll", "Too fast")

	ws := dialLivePoll(t, srv.URL, poll.ID, poll.TenantID)
	defer ws.Close()
	snap := receiveEnvelope(t, ws, 2*time.Second)

	payloadJSON, _ := json.Marshal(snap.Payload)
	var p struct {
		Options []struct {
			Label     string `json:"label"`
			VoteCount int    `json:"vote_count"`
		} `json:"options"`
		TotalVotes int `json:"total_votes"`
	}
	_ = json.Unmarshal(payloadJSON, &p)
	counts := map[string]int{}
	for _, o := range p.Options {
		counts[o.Label] = o.VoteCount
	}
	if counts["Just right"] != 2 || counts["Too fast"] != 1 {
		t.Fatalf("poll snapshot counts from tally = %#v, want Just right:2 Too fast:1", counts)
	}
	if p.TotalVotes != 3 {
		t.Fatalf("poll snapshot total_votes = %d, want 3", p.TotalVotes)
	}
}
