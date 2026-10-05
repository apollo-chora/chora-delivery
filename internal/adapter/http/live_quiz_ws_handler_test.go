// live_quiz_ws_handler_test.go — RED tests for the live-quiz WebSocket
// fan-out handler.
//
// Test matrix per Wave-5 brief:
//
//   - GET /api/v1/live-quizzes/{sessionId}/ws upgrades to WebSocket
//   - On connect, the client receives a `snapshot` envelope containing the
//     current session state (state, response counts by question)
//   - Subsequent broker.Publish(sessionID, ...) calls land on the client
//     as `event` envelopes (instructor advance / learner submit fan-out)
//   - Cross-session isolation: a client on session-A does not see
//     session-B events
//   - Missing sessionId in the path returns 404 (the upgrade never starts)
//   - Repo miss (unknown session) returns 404
//   - Broker nil returns 503
//   - Repo nil returns 503
//   - Disconnect cleanup: broker.SubscriberCount drops back to 0
//
// Mocks: the live-quiz WS handler depends on a small port (LiveQuizSessionRepo)
// + the ws.Broker. The repo is fake'd in-memory; the broker is the real one
// (it's pure in-process state, so per `feedback_no_stubs_real_wiring` we use
// the real component, no stub).
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

// fakeLiveQuizSessionRepo is an in-process repo for tests.
type fakeLiveQuizSessionRepo struct {
	mu sync.RWMutex
	by map[string]*classroom.LiveQuizSession
}

func newFakeLiveQuizSessionRepo() *fakeLiveQuizSessionRepo {
	return &fakeLiveQuizSessionRepo{by: make(map[string]*classroom.LiveQuizSession)}
}

func (r *fakeLiveQuizSessionRepo) Save(s *classroom.LiveQuizSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[s.ID] = s
}

func (r *fakeLiveQuizSessionRepo) Get(_ context.Context, id string) (*classroom.LiveQuizSession, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.by[id]
	return s, ok, nil
}

// -----------------------------------------------------------------------------
// Test helpers — build a test server with just the WS route mounted.
// -----------------------------------------------------------------------------

// newLiveQuizWSServer mounts /api/v1/live-quizzes/{sessionId}/ws against fresh
// fakes + broker. Returns the running httptest server, the repo + broker
// for in-test mutation.
func newLiveQuizWSServer(t *testing.T) (*httptest.Server, *fakeLiveQuizSessionRepo, *wsadapter.Broker) {
	t.Helper()
	repo := newFakeLiveQuizSessionRepo()
	broker := wsadapter.NewBroker(0)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/live-quizzes/", httpapi.LiveQuizWSHandler(repo, nil, broker, nil, nil))
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		broker.Close()
		srv.Close()
	})
	return srv, repo, broker
}

// wsURL converts an http:// httptest URL + path into a ws:// URL.
func wsURL(srvURL, path string) string {
	return "ws" + strings.TrimPrefix(srvURL, "http") + path
}

// wsTestGCID is the caller identity stamped on test WS handshakes — the
// chora-gateway JWT gate stamps the validated gcid onto this header in prod
// (ADR-168 CHO-1616). The WS handler only checks presence (any authenticated
// caller in the tenant may subscribe), so a fixed value suffices.
const wsTestGCID = "01970000-0000-7000-8000-00000000ca11"

// dialLiveQuiz opens a WS client against the given sessionID, stamping the
// gateway-supplied caller identity (X-Tenant-Id + gcid) onto the handshake —
// browsers cannot set Authorization on a native WS upgrade, so the gateway
// validates the JWT and forwards these mesh-trust headers (CHO-1616). tenantID
// MUST match the session's tenant or the handler 404s.
func dialLiveQuiz(t *testing.T, srvURL, sessionID, tenantID string) *websocket.Conn {
	t.Helper()
	cfg, err := websocket.NewConfig(
		wsURL(srvURL, "/api/v1/live-quizzes/"+sessionID+"/ws"),
		"http://localhost/",
	)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	cfg.Header = http.Header{}
	cfg.Header.Set("X-Tenant-Id", tenantID)
	cfg.Header.Set("gcid", wsTestGCID)
	ws, err := cfg.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	return ws
}

// getWSStatus issues a plain HTTP GET to a WS path with the given caller
// identity headers and returns the status code. Used to assert pre-upgrade
// rejections (401/404) where the WS dial helper would hide the status behind
// ErrBadStatus. Empty tenant/gcid omit the corresponding header.
func getWSStatus(t *testing.T, url, tenant, gcid string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if tenant != "" {
		req.Header.Set("X-Tenant-Id", tenant)
	}
	if gcid != "" {
		req.Header.Set("gcid", gcid)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// receiveEnvelope reads a single Message from ws with a deadline.
func receiveEnvelope(t *testing.T, ws *websocket.Conn, timeout time.Duration) wsadapter.Message {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(timeout))
	defer ws.SetReadDeadline(time.Time{})
	var msg wsadapter.Message
	if err := websocket.JSON.Receive(ws, &msg); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	return msg
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

func TestLiveQuizWSHandler_UpgradesAndSendsSnapshot(t *testing.T) {
	srv, repo, _ := newLiveQuizWSServer(t)

	sess, err := classroom.NewLiveQuizSession(
		"019e3000-0000-7000-8000-000000000001", // quizID
		"019e3000-0000-7000-8000-000000000002", // tenantID
		"019e3000-0000-7000-9000-000000000003", // instructorGCID
	)
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	if err := sess.Start(time.Now().UTC()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	repo.Save(sess)

	ws := dialLiveQuiz(t, srv.URL, sess.ID, sess.TenantID)
	defer ws.Close()

	snap := receiveEnvelope(t, ws, 2*time.Second)
	if snap.Type != "snapshot" {
		t.Fatalf("first envelope Type = %q, want snapshot", snap.Type)
	}
	if snap.Timestamp.IsZero() {
		t.Fatalf("snapshot Timestamp is zero")
	}
	// Snapshot payload must round-trip a state field.
	payloadJSON, _ := json.Marshal(snap.Payload)
	var p map[string]any
	_ = json.Unmarshal(payloadJSON, &p)
	if state, _ := p["state"].(string); state != string(classroom.LiveQuizSessionStateLive) {
		t.Fatalf("snapshot payload.state = %q, want LIVE", state)
	}
}

func TestLiveQuizWSHandler_FansOutPublishedEvents(t *testing.T) {
	srv, repo, broker := newLiveQuizWSServer(t)

	sess, _ := classroom.NewLiveQuizSession(
		"019e3000-0000-7000-8000-000000000001",
		"019e3000-0000-7000-8000-000000000002",
		"019e3000-0000-7000-9000-000000000003",
	)
	_ = sess.Start(time.Now().UTC())
	repo.Save(sess)

	ws := dialLiveQuiz(t, srv.URL, sess.ID, sess.TenantID)
	defer ws.Close()

	// Drain the initial snapshot.
	_ = receiveEnvelope(t, ws, 2*time.Second)

	// Publish two events as the REST mutation handlers would.
	broker.Publish(sess.ID, wsadapter.Message{
		Type:    "event",
		Payload: map[string]any{"kind": "response_submitted", "question_id": "q1"},
	})
	broker.Publish(sess.ID, wsadapter.Message{
		Type:    "event",
		Payload: map[string]any{"kind": "session_closed"},
	})

	got1 := receiveEnvelope(t, ws, 2*time.Second)
	if got1.Type != "event" {
		t.Fatalf("got1.Type = %q, want event", got1.Type)
	}
	got2 := receiveEnvelope(t, ws, 2*time.Second)
	if got2.Type != "event" {
		t.Fatalf("got2.Type = %q, want event", got2.Type)
	}
}

func TestLiveQuizWSHandler_CrossSessionIsolation(t *testing.T) {
	srv, repo, broker := newLiveQuizWSServer(t)

	sessA, _ := classroom.NewLiveQuizSession("q-A", "tenant-A", "instr-A")
	_ = sessA.Start(time.Now().UTC())
	repo.Save(sessA)

	sessB, _ := classroom.NewLiveQuizSession("q-B", "tenant-B", "instr-B")
	_ = sessB.Start(time.Now().UTC())
	repo.Save(sessB)

	wsA := dialLiveQuiz(t, srv.URL, sessA.ID, sessA.TenantID)
	defer wsA.Close()
	wsB := dialLiveQuiz(t, srv.URL, sessB.ID, sessB.TenantID)
	defer wsB.Close()

	// Drain initial snapshots.
	_ = receiveEnvelope(t, wsA, 2*time.Second)
	_ = receiveEnvelope(t, wsB, 2*time.Second)

	broker.Publish(sessA.ID, wsadapter.Message{Type: "event", Payload: "A-only"})

	// wsA must receive the event.
	got := receiveEnvelope(t, wsA, 2*time.Second)
	if got.Payload != "A-only" {
		t.Fatalf("wsA got payload=%v, want A-only", got.Payload)
	}

	// wsB must NOT receive the session-A event (200ms grace).
	_ = wsB.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var leaked wsadapter.Message
	err := websocket.JSON.Receive(wsB, &leaked)
	if err == nil {
		t.Fatalf("wsB received unrelated session-A event: %+v", leaked)
	}
}

func TestLiveQuizWSHandler_RepoMissReturns404(t *testing.T) {
	srv, _, _ := newLiveQuizWSServer(t)

	// Skip the WS dial — use a plain HTTP GET so we can read the status code
	// (a 404 means the upgrade was never attempted). The websocket.Dial
	// helper hides non-101 status codes behind ErrBadStatus. Identity headers
	// are supplied (CHO-1616) so the request clears the auth gate and reaches
	// the repo lookup; an authenticated caller probing an unknown id gets 404.
	got := getWSStatus(t, srv.URL+"/api/v1/live-quizzes/nonexistent-id/ws", "any-tenant", wsTestGCID)
	if got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

// TestLiveQuizWSHandler_MissingTenantReturns401 — a WS upgrade to an EXISTING
// session WITHOUT X-Tenant-Id is rejected 401 BEFORE the session lookup, so an
// unauthenticated caller cannot use the 404-vs-401 distinction as an existence
// oracle (CHO-1616).
func TestLiveQuizWSHandler_MissingTenantReturns401(t *testing.T) {
	srv, repo, _ := newLiveQuizWSServer(t)
	sess, _ := classroom.NewLiveQuizSession("q-1", "tenant-real", "instr-1")
	_ = sess.Start(time.Now().UTC())
	repo.Save(sess)

	got := getWSStatus(t, srv.URL+"/api/v1/live-quizzes/"+sess.ID+"/ws", "", wsTestGCID)
	if got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (missing X-Tenant-Id)", got)
	}
}

// TestLiveQuizWSHandler_MissingGCIDReturns401 — a WS upgrade with a tenant but
// no gcid (caller identity) is rejected 401.
func TestLiveQuizWSHandler_MissingGCIDReturns401(t *testing.T) {
	srv, repo, _ := newLiveQuizWSServer(t)
	sess, _ := classroom.NewLiveQuizSession("q-1", "tenant-real", "instr-1")
	_ = sess.Start(time.Now().UTC())
	repo.Save(sess)

	got := getWSStatus(t, srv.URL+"/api/v1/live-quizzes/"+sess.ID+"/ws", sess.TenantID, "")
	if got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (missing gcid)", got)
	}
}

// TestLiveQuizWSHandler_WrongTenantReturns404 — a WS upgrade whose caller
// tenant differs from the session's tenant returns 404 (indistinguishable from
// not-found) so cross-tenant session existence never leaks (CHO-1616).
func TestLiveQuizWSHandler_WrongTenantReturns404(t *testing.T) {
	srv, repo, _ := newLiveQuizWSServer(t)
	sess, _ := classroom.NewLiveQuizSession("q-1", "tenant-real", "instr-1")
	_ = sess.Start(time.Now().UTC())
	repo.Save(sess)

	got := getWSStatus(t, srv.URL+"/api/v1/live-quizzes/"+sess.ID+"/ws", "tenant-other", wsTestGCID)
	if got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (cross-tenant — no existence leak)", got)
	}
}

func TestLiveQuizWSHandler_MissingSessionIDReturns404(t *testing.T) {
	srv, _, _ := newLiveQuizWSServer(t)

	resp, err := http.Get(srv.URL + "/api/v1/live-quizzes//ws")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestLiveQuizWSHandler_NilBrokerReturns503(t *testing.T) {
	repo := newFakeLiveQuizSessionRepo()
	mux := http.NewServeMux()
	mux.Handle("/api/v1/live-quizzes/", httpapi.LiveQuizWSHandler(repo, nil, nil, nil, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/live-quizzes/some-id/ws")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestLiveQuizWSHandler_NilRepoReturns503(t *testing.T) {
	broker := wsadapter.NewBroker(0)
	defer broker.Close()
	mux := http.NewServeMux()
	mux.Handle("/api/v1/live-quizzes/", httpapi.LiveQuizWSHandler(nil, nil, broker, nil, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/live-quizzes/some-id/ws")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestLiveQuizWSHandler_DisconnectCleansUpSubscriber(t *testing.T) {
	srv, repo, broker := newLiveQuizWSServer(t)

	sess, _ := classroom.NewLiveQuizSession("q-1", "t-1", "i-1")
	_ = sess.Start(time.Now().UTC())
	repo.Save(sess)

	ws := dialLiveQuiz(t, srv.URL, sess.ID, sess.TenantID)
	// Drain initial snapshot.
	_ = receiveEnvelope(t, ws, 2*time.Second)

	// Verify a subscriber is registered.
	if got := broker.SubscriberCountForSession(sess.ID); got != 1 {
		t.Fatalf("SubscriberCountForSession = %d, want 1", got)
	}

	_ = ws.Close()

	// Trigger handler-side cleanup by publishing — the next send will
	// observe the closed connection and unsubscribe. Then poll for the
	// SubscriberCount to drop.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		broker.Publish(sess.ID, wsadapter.Message{Type: "event", Payload: "noop"})
		if broker.SubscriberCountForSession(sess.ID) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("subscriber not cleaned up within 2s; SubscriberCountForSession = %d", broker.SubscriberCountForSession(sess.ID))
}

// -----------------------------------------------------------------------------
// Concurrency — N clients on one session each receive M published events
// -----------------------------------------------------------------------------

func TestLiveQuizWSHandler_MultipleClientsAllReceive(t *testing.T) {
	srv, repo, broker := newLiveQuizWSServer(t)

	sess, _ := classroom.NewLiveQuizSession("q-1", "t-1", "i-1")
	_ = sess.Start(time.Now().UTC())
	repo.Save(sess)

	const clients = 5
	conns := make([]*websocket.Conn, clients)
	for i := 0; i < clients; i++ {
		conns[i] = dialLiveQuiz(t, srv.URL, sess.ID, sess.TenantID)
		defer conns[i].Close()
		// Drain snapshot.
		_ = receiveEnvelope(t, conns[i], 2*time.Second)
	}

	broker.Publish(sess.ID, wsadapter.Message{Type: "event", Payload: "broadcast"})

	for i, ws := range conns {
		got := receiveEnvelope(t, ws, 2*time.Second)
		if got.Type != "event" || got.Payload != "broadcast" {
			t.Fatalf("client[%d] got Type=%q Payload=%v, want event/broadcast", i, got.Type, got.Payload)
		}
	}
}
