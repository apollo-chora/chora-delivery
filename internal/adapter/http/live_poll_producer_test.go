// live_poll_producer_test.go — ADR-168 LivePoll producer hot-path TDD.
//
// Mirrors live_quiz_producer_test.go but for the simpler LivePoll aggregate
// (single question, per-option tallies, no scoring/leaderboard):
//
//	POST /api/v1/live-polls               — create DRAFT poll  [instructor]
//	POST /api/v1/live-polls/{id}/open     — DRAFT → OPEN        [instructor]
//	POST /api/v1/live-polls/{id}/votes    — CastVote + fan-out  [learner]
//	POST /api/v1/live-polls/{id}/close    — OPEN → CLOSED       [instructor]
//	GET  /api/v1/live-polls/{id}          — poll snapshot       [any in-tenant]
//
// Each mutation bumps the authoritative cross-pod TallyStore (for votes) and
// fans out on realtime.PollChannel(pollID); the per-pod FanIn re-emits into
// the local ws.Broker so connected WS clients on any pod see the update.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/adapter/realtime"
	wsadapter "github.com/apollo-chora/chora-delivery/internal/adapter/ws"
)

type pollRig struct {
	polls  *inmem.LivePollRepo
	bp     *realtime.MemBackplane
	tally  *realtime.MemTally
	broker *wsadapter.Broker
}

func buildPollServer(t *testing.T) (http.Handler, *pollRig) {
	t.Helper()
	polls := inmem.NewLivePollRepo()
	bp, tally := realtime.NewMemBackplane(), realtime.NewMemTally()
	broker := wsadapter.NewBroker(0)
	t.Cleanup(broker.Close)
	srv := httpapi.NewServer(httpapi.Deps{
		LivePolls:               polls,
		RealtimeBackplane:       bp,
		RealtimeTally:           tally,
		ClassroomRealtimeBroker: broker,
	})
	return srv, &pollRig{polls: polls, bp: bp, tally: tally, broker: broker}
}

// createPoll POSTs a DRAFT poll and returns its id.
func createPoll(t *testing.T, srv http.Handler) string {
	t.Helper()
	rec := doCS(t, srv, "POST", "/api/v1/live-polls",
		`{"question":"How is the pace?","options":["Too slow","Just right","Too fast"]}`,
		csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create poll status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode create resp: %v", err)
	}
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("create poll returned no id: %s", rec.Body.String())
	}
	return id
}

// quizFanOutHarness wires a LIVE session for a published quiz with realtime
// deps, returning the server + the backplane to assert fan-out frame shapes.
func quizFanOutHarness(t *testing.T) (http.Handler, *producerRig) {
	return buildProducerServer(t)
}

// TestAnswerGraded_FrameIsCoherentSnapshot locks the chora-web play-view
// contract (feedback_parallel_agent_contract_drift): the FE applyFrame runs
// decodeSnapshot on EVERY event frame, so an answer_graded frame MUST be a
// valid snapshot (id + LIVE state + nested response_counts) — otherwise it
// clobbers the play view to a blank ARMED snapshot — AND carry the top-N
// leaderboard the FE accumulates.
func TestAnswerGraded_FrameIsCoherentSnapshot(t *testing.T) {
	srv, rig := quizFanOutHarness(t)
	ch, cancel, _ := rig.bp.PSubscribe(context.Background(), "rt:quiz:*")
	defer cancel()

	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	<-ch // drain question_advanced

	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/responses",
		`{"question_id":"q1","choice":"Sprint Planning"}`, csTenantA, csLearner1GCID, "learner")

	msg := <-ch
	var frame struct {
		Type    string `json:"type"`
		Payload struct {
			ID             string                      `json:"id"`
			State          string                      `json:"state"`
			ResponseCounts map[string]map[string]int   `json:"response_counts"`
			Leaderboard    []realtime.LeaderboardEntry `json:"leaderboard"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(msg.Payload, &frame); err != nil {
		t.Fatalf("decode answer_graded frame: %v", err)
	}
	if frame.Type != "answer_graded" {
		t.Fatalf("frame type = %q, want answer_graded", frame.Type)
	}
	if frame.Payload.ID != rig.sessionID {
		t.Fatalf("frame missing session id (would clobber FE snapshot): %s", msg.Payload)
	}
	if frame.Payload.State != "LIVE" {
		t.Fatalf("frame state = %q, want LIVE (a blank ARMED clobbers the play view)", frame.Payload.State)
	}
	if frame.Payload.ResponseCounts["q1"]["Sprint Planning"] != 1 {
		t.Fatalf("frame response_counts not nested by question: %s", msg.Payload)
	}
	if len(frame.Payload.Leaderboard) != 1 || frame.Payload.Leaderboard[0].Score <= 0 {
		t.Fatalf("frame leaderboard missing/empty: %#v", frame.Payload.Leaderboard)
	}
	// ADR-179 D3 leak #2 closure: the broadcast is a single in-tenant fan-out
	// with no role gate, so it must carry NO correctness and NO points. The
	// submitter's own grade travels only on their REST 201 (`your_result`).
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(msg.Payload, &envelope); err != nil {
		t.Fatalf("re-decode frame envelope: %v", err)
	}
	var payloadKeys map[string]json.RawMessage
	if err := json.Unmarshal(envelope["payload"], &payloadKeys); err != nil {
		t.Fatalf("decode frame payload keys: %v", err)
	}
	for _, leak := range []string{"correct", "awarded_points", "cumulative_score"} {
		if _, present := payloadKeys[leak]; present {
			t.Fatalf("answer_graded broadcast leaks %q (ADR-179 D3): %s", leak, msg.Payload)
		}
	}
}

func TestQuestionAdvanced_FrameIsCoherentSnapshot(t *testing.T) {
	srv, rig := quizFanOutHarness(t)
	ch, cancel, _ := rig.bp.PSubscribe(context.Background(), "rt:quiz:*")
	defer cancel()

	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	msg := <-ch
	var frame struct {
		Type    string `json:"type"`
		Payload struct {
			ID                string `json:"id"`
			State             string `json:"state"`
			CurrentQuestionID string `json:"current_question_id"`
			QuestionID        string `json:"question_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(msg.Payload, &frame); err != nil {
		t.Fatalf("decode question_advanced frame: %v", err)
	}
	if frame.Payload.ID != rig.sessionID || frame.Payload.State != "LIVE" {
		t.Fatalf("question_advanced frame not a coherent snapshot: %s", msg.Payload)
	}
	if frame.Payload.CurrentQuestionID != "q1" || frame.Payload.QuestionID != "q1" {
		t.Fatalf("question_advanced frame missing current question: %s", msg.Payload)
	}
}

// TestSessionLifecycle_EmitsStartedAndEnded locks ADR-168 §2.3c: the
// ARMED→LIVE and LIVE→CLOSED transitions emit the durable lifecycle events
// that infra provisioned topics for.
func TestSessionLifecycle_EmitsStartedAndEnded(t *testing.T) {
	sessions := inmem.NewClassroomSessionRepo()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{ClassroomSessions: sessions, Publisher: pub})

	// Seed an ARMED session directly via the start-session route.
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions",
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("start session: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	sid, _ := created["id"].(string)

	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor"); rec.Code != http.StatusOK {
		t.Fatalf("start transition: %d", rec.Code)
	}
	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/close", "", csTenantA, csInstructorGCID, "instructor"); rec.Code != http.StatusOK {
		t.Fatalf("close transition: %d", rec.Code)
	}

	var sawStarted, sawEnded bool
	for _, e := range pub.History() {
		switch e.Topic {
		case events.TopicLiveQuizSessionStarted:
			sawStarted = true
			if e.Payload["session_id"] != sid {
				t.Errorf("started event wrong session: %#v", e.Payload)
			}
		case events.TopicLiveQuizSessionEnded:
			sawEnded = true
		}
	}
	if !sawStarted || !sawEnded {
		t.Fatalf("missing lifecycle events: started=%v ended=%v", sawStarted, sawEnded)
	}
}

func TestPoll_CreateThenOpenFansOut(t *testing.T) {
	srv, rig := buildPollServer(t)
	ch, cancel, _ := rig.bp.PSubscribe(context.Background(), "rt:poll:*")
	defer cancel()

	id := createPoll(t, srv)
	if p, ok, _ := rig.polls.Get(id); !ok || p.State != "DRAFT" {
		t.Fatalf("poll not persisted DRAFT: %+v", p)
	}

	rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("open status=%d body=%s", rec.Code, rec.Body.String())
	}
	if p, _, _ := rig.polls.Get(id); p.State != "OPEN" {
		t.Fatalf("poll not OPEN after open: %+v", p)
	}
	select {
	case msg := <-ch:
		if msg.Channel != realtime.PollChannel(id) {
			t.Fatalf("open fan-out wrong channel: %s", msg.Channel)
		}
	case <-time.After(time.Second):
		t.Fatalf("no poll_opened fan-out")
	}
}

func TestPoll_VoteBumpsTallyAndFansOut(t *testing.T) {
	srv, rig := buildPollServer(t)
	id := createPoll(t, srv)
	_ = doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")

	ch, cancel, _ := rig.bp.PSubscribe(context.Background(), "rt:poll:*")
	defer cancel()

	rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/votes",
		`{"choice":"Just right"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("vote status=%d body=%s", rec.Code, rec.Body.String())
	}
	if snap, _ := rig.tally.Snapshot(context.Background(), id, "_poll"); snap["Just right"] != 1 {
		t.Fatalf("tally not bumped: %#v", snap)
	}
	select {
	case msg := <-ch:
		if msg.Channel != realtime.PollChannel(id) {
			t.Fatalf("vote fan-out wrong channel: %s", msg.Channel)
		}
	case <-time.After(time.Second):
		t.Fatalf("no vote_cast fan-out")
	}
}

func TestPoll_DuplicateVoteRejected(t *testing.T) {
	srv, rig := buildPollServer(t)
	_ = rig
	id := createPoll(t, srv)
	_ = doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")
	if rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/votes",
		`{"choice":"Too slow"}`, csTenantA, csLearner1GCID, "learner"); rec.Code != http.StatusOK {
		t.Fatalf("first vote status=%d", rec.Code)
	}
	if rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/votes",
		`{"choice":"Too fast"}`, csTenantA, csLearner1GCID, "learner"); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate vote want 409 got %d", rec.Code)
	}
}

func TestPoll_VoteBeforeOpenRejected(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	if rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/votes",
		`{"choice":"Too slow"}`, csTenantA, csLearner1GCID, "learner"); rec.Code != http.StatusConflict {
		t.Fatalf("vote on DRAFT poll want 409 got %d", rec.Code)
	}
}

func TestPoll_CloseFansOutAndFreezes(t *testing.T) {
	srv, rig := buildPollServer(t)
	ch, cancel, _ := rig.bp.PSubscribe(context.Background(), "rt:poll:*")
	defer cancel()

	id := createPoll(t, srv)
	_ = doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")
	// drain the poll_opened message
	<-ch

	rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/close", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("close status=%d body=%s", rec.Code, rec.Body.String())
	}
	if p, _, _ := rig.polls.Get(id); p.State != "CLOSED" {
		t.Fatalf("poll not CLOSED: %+v", p)
	}
	select {
	case msg := <-ch:
		if msg.Channel != realtime.PollChannel(id) {
			t.Fatalf("close fan-out wrong channel: %s", msg.Channel)
		}
	case <-time.After(time.Second):
		t.Fatalf("no poll_closed fan-out")
	}
	// voting on a CLOSED poll is rejected.
	if rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/votes",
		`{"choice":"Too slow"}`, csTenantA, csLearner2GCID, "learner"); rec.Code != http.StatusConflict {
		t.Fatalf("vote on CLOSED poll want 409 got %d", rec.Code)
	}
}

func TestPoll_OpenRejectsNonInstructor(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	if rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csLearner1GCID, "learner"); rec.Code != http.StatusForbidden {
		t.Fatalf("learner opening poll want 403 got %d", rec.Code)
	}
}

func TestPoll_CrossTenantNotFound(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	if rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantB, csInstructorGCID, "instructor"); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant open want 404 got %d", rec.Code)
	}
}

func TestPoll_GetSnapshot(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	rec := doCS(t, srv, "GET", "/api/v1/live-polls/"+id, "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("get snapshot status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if got["id"] != id {
		t.Fatalf("snapshot id mismatch: %#v", got)
	}
}
