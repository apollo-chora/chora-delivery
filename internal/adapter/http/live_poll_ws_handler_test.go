// live_poll_ws_handler_test.go — RED tests for the live-poll WebSocket
// fan-out handler.
//
// Test matrix per Wave-5 brief:
//
//   - GET /api/v1/live-polls/{pollId}/ws upgrades to WebSocket
//   - On connect, the client receives a `snapshot` envelope containing the
//     current poll state + per-option vote tallies
//   - Subsequent broker.Publish(pollID, ...) calls land on the client as
//     `event` envelopes (vote cast / poll opened / poll closed)
//   - Cross-poll isolation: a client on poll-A does not see poll-B events
//   - Missing pollId in the path returns 404 (the upgrade never starts)
//   - Repo miss returns 404
//   - Broker nil returns 503
//   - Repo nil returns 503
//   - Disconnect cleanup: broker.SubscriberCount drops back to 0
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	wsadapter "github.com/apollo-chora/chora-delivery/internal/adapter/ws"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// -----------------------------------------------------------------------------
// Fake repo
// -----------------------------------------------------------------------------

type fakeLivePollRepo struct {
	mu sync.RWMutex
	by map[string]*classroom.LivePoll
}

func newFakeLivePollRepo() *fakeLivePollRepo {
	return &fakeLivePollRepo{by: make(map[string]*classroom.LivePoll)}
}

func (r *fakeLivePollRepo) Save(p *classroom.LivePoll) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[p.ID] = p
}

func (r *fakeLivePollRepo) Get(_ context.Context, id string) (*classroom.LivePoll, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.by[id]
	return p, ok, nil
}

// -----------------------------------------------------------------------------
// Test helpers
// -----------------------------------------------------------------------------

func newLivePollWSServer(t *testing.T) (*httptest.Server, *fakeLivePollRepo, *wsadapter.Broker) {
	t.Helper()
	repo := newFakeLivePollRepo()
	broker := wsadapter.NewBroker(0)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/live-polls/", httpapi.LivePollWSHandler(repo, broker, nil))
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		broker.Close()
		srv.Close()
	})
	return srv, repo, broker
}

func dialLivePoll(t *testing.T, srvURL, pollID, tenantID string) *websocket.Conn {
	t.Helper()
	cfg, err := websocket.NewConfig(
		wsURL(srvURL, "/api/v1/live-polls/"+pollID+"/ws"),
		"http://localhost/",
	)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	// Gateway-supplied caller identity (CHO-1616) — see dialLiveQuiz.
	cfg.Header = http.Header{}
	cfg.Header.Set("X-Tenant-Id", tenantID)
	cfg.Header.Set("gcid", wsTestGCID)
	ws, err := cfg.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	return ws
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

func TestLivePollWSHandler_UpgradesAndSendsSnapshot(t *testing.T) {
	srv, repo, _ := newLivePollWSServer(t)

	poll, err := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID:       "tenant-1",
		InstructorGCID: "instr-1",
		Question:       "How was the lesson pacing?",
		Options:        []string{"Too slow", "Just right", "Too fast"},
	})
	if err != nil {
		t.Fatalf("NewLivePoll: %v", err)
	}
	if err := poll.Open(time.Now().UTC()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo.Save(poll)

	ws := dialLivePoll(t, srv.URL, poll.ID, poll.TenantID)
	defer ws.Close()

	snap := receiveEnvelope(t, ws, 2*time.Second)
	if snap.Type != "snapshot" {
		t.Fatalf("first envelope Type = %q, want snapshot", snap.Type)
	}
	if snap.Timestamp.IsZero() {
		t.Fatalf("snapshot Timestamp is zero")
	}

	// Verify the snapshot payload carries state + per-option tallies.
	raw, _ := json.Marshal(snap.Payload)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	if state, _ := p["state"].(string); state != string(classroom.LivePollStateOpen) {
		t.Fatalf("snapshot state = %q, want OPEN", state)
	}
	opts, _ := p["options"].([]any)
	if len(opts) != 3 {
		t.Fatalf("snapshot options len = %d, want 3", len(opts))
	}
}

func TestLivePollWSHandler_FansOutPublishedEvents(t *testing.T) {
	srv, repo, broker := newLivePollWSServer(t)

	poll, _ := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID:       "tenant-1",
		InstructorGCID: "instr-1",
		Question:       "Pacing?",
		Options:        []string{"slow", "fast"},
	})
	_ = poll.Open(time.Now().UTC())
	repo.Save(poll)

	ws := dialLivePoll(t, srv.URL, poll.ID, poll.TenantID)
	defer ws.Close()
	_ = receiveEnvelope(t, ws, 2*time.Second) // drain snapshot

	broker.Publish(poll.ID, wsadapter.Message{
		Type:    "event",
		Payload: map[string]any{"kind": "vote_cast", "option": "slow", "total": 1},
	})

	got := receiveEnvelope(t, ws, 2*time.Second)
	if got.Type != "event" {
		t.Fatalf("got.Type = %q, want event", got.Type)
	}
	rawPayload, _ := json.Marshal(got.Payload)
	if !strings.Contains(string(rawPayload), `"kind":"vote_cast"`) {
		t.Fatalf("payload missing vote_cast kind: %s", rawPayload)
	}
}

func TestLivePollWSHandler_CrossPollIsolation(t *testing.T) {
	srv, repo, broker := newLivePollWSServer(t)

	pollA, _ := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID:       "tenant-1",
		InstructorGCID: "instr-A",
		Question:       "qA?",
		Options:        []string{"y", "n"},
	})
	_ = pollA.Open(time.Now().UTC())
	repo.Save(pollA)

	pollB, _ := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID:       "tenant-1",
		InstructorGCID: "instr-B",
		Question:       "qB?",
		Options:        []string{"y", "n"},
	})
	_ = pollB.Open(time.Now().UTC())
	repo.Save(pollB)

	wsA := dialLivePoll(t, srv.URL, pollA.ID, pollA.TenantID)
	defer wsA.Close()
	wsB := dialLivePoll(t, srv.URL, pollB.ID, pollB.TenantID)
	defer wsB.Close()
	_ = receiveEnvelope(t, wsA, 2*time.Second)
	_ = receiveEnvelope(t, wsB, 2*time.Second)

	broker.Publish(pollA.ID, wsadapter.Message{Type: "event", Payload: "A-only"})

	gotA := receiveEnvelope(t, wsA, 2*time.Second)
	if gotA.Payload != "A-only" {
		t.Fatalf("wsA payload = %v, want A-only", gotA.Payload)
	}

	_ = wsB.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var leaked wsadapter.Message
	if err := websocket.JSON.Receive(wsB, &leaked); err == nil {
		t.Fatalf("wsB received unrelated poll-A event: %+v", leaked)
	}
}

func TestLivePollWSHandler_RepoMissReturns404(t *testing.T) {
	srv, _, _ := newLivePollWSServer(t)

	// Identity headers supplied (CHO-1616) so the request clears the auth gate
	// and reaches the repo lookup — authenticated caller, unknown id → 404.
	got := getWSStatus(t, srv.URL+"/api/v1/live-polls/nonexistent-id/ws", "any-tenant", wsTestGCID)
	if got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

// TestLivePollWSHandler_MissingTenantReturns401 — WS upgrade to an existing
// poll without X-Tenant-Id is 401 before the lookup (no existence oracle).
func TestLivePollWSHandler_MissingTenantReturns401(t *testing.T) {
	srv, repo, _ := newLivePollWSServer(t)
	poll, _ := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID: "tenant-real", InstructorGCID: "instr-1",
		Question: "q?", Options: []string{"y", "n"},
	})
	_ = poll.Open(time.Now().UTC())
	repo.Save(poll)

	got := getWSStatus(t, srv.URL+"/api/v1/live-polls/"+poll.ID+"/ws", "", wsTestGCID)
	if got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (missing X-Tenant-Id)", got)
	}
}

// TestLivePollWSHandler_WrongTenantReturns404 — cross-tenant caller gets 404
// (indistinguishable from not-found; no existence leak) (CHO-1616).
func TestLivePollWSHandler_WrongTenantReturns404(t *testing.T) {
	srv, repo, _ := newLivePollWSServer(t)
	poll, _ := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID: "tenant-real", InstructorGCID: "instr-1",
		Question: "q?", Options: []string{"y", "n"},
	})
	_ = poll.Open(time.Now().UTC())
	repo.Save(poll)

	got := getWSStatus(t, srv.URL+"/api/v1/live-polls/"+poll.ID+"/ws", "tenant-other", wsTestGCID)
	if got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (cross-tenant — no existence leak)", got)
	}
}

func TestLivePollWSHandler_MissingPollIDReturns404(t *testing.T) {
	srv, _, _ := newLivePollWSServer(t)

	resp, err := http.Get(srv.URL + "/api/v1/live-polls//ws")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestLivePollWSHandler_NilBrokerReturns503(t *testing.T) {
	repo := newFakeLivePollRepo()
	mux := http.NewServeMux()
	mux.Handle("/api/v1/live-polls/", httpapi.LivePollWSHandler(repo, nil, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/live-polls/some-id/ws")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestLivePollWSHandler_NilRepoReturns503(t *testing.T) {
	broker := wsadapter.NewBroker(0)
	defer broker.Close()
	mux := http.NewServeMux()
	mux.Handle("/api/v1/live-polls/", httpapi.LivePollWSHandler(nil, broker, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/live-polls/some-id/ws")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestLivePollWSHandler_DisconnectCleansUpSubscriber(t *testing.T) {
	srv, repo, broker := newLivePollWSServer(t)

	poll, _ := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID:       "tenant-1",
		InstructorGCID: "instr-1",
		Question:       "q?",
		Options:        []string{"y", "n"},
	})
	_ = poll.Open(time.Now().UTC())
	repo.Save(poll)

	ws := dialLivePoll(t, srv.URL, poll.ID, poll.TenantID)
	_ = receiveEnvelope(t, ws, 2*time.Second)

	if got := broker.SubscriberCountForSession(poll.ID); got != 1 {
		t.Fatalf("SubscriberCountForSession = %d, want 1", got)
	}

	_ = ws.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		broker.Publish(poll.ID, wsadapter.Message{Type: "event", Payload: "noop"})
		if broker.SubscriberCountForSession(poll.ID) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("subscriber not cleaned up within 2s; SubscriberCountForSession = %d", broker.SubscriberCountForSession(poll.ID))
}

func TestLivePollWSHandler_MultipleClientsAllReceive(t *testing.T) {
	srv, repo, broker := newLivePollWSServer(t)

	poll, _ := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID:       "tenant-1",
		InstructorGCID: "instr-1",
		Question:       "q?",
		Options:        []string{"y", "n"},
	})
	_ = poll.Open(time.Now().UTC())
	repo.Save(poll)

	const clients = 5
	conns := make([]*websocket.Conn, clients)
	for i := 0; i < clients; i++ {
		conns[i] = dialLivePoll(t, srv.URL, poll.ID, poll.TenantID)
		defer conns[i].Close()
		_ = receiveEnvelope(t, conns[i], 2*time.Second)
	}

	broker.Publish(poll.ID, wsadapter.Message{Type: "event", Payload: "broadcast"})

	for i, ws := range conns {
		got := receiveEnvelope(t, ws, 2*time.Second)
		if got.Type != "event" || got.Payload != "broadcast" {
			t.Fatalf("client[%d] got Type=%q Payload=%v, want event/broadcast", i, got.Type, got.Payload)
		}
	}
}
