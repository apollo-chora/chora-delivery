// Command smoke-classroom is the ADR-168 live-classroom acceptance smoke.
//
// It drives the full producer→realtime→durable loop against a RUNNING
// chora-delivery (via kubectl port-forward), asserting the Redis-backed hot
// path works end-to-end:
//
//	create quiz → patch question → publish → start session → LIVE →
//	open WS → advance → submit graded response →
//	assert the WS receives an `answer_graded` frame carrying the
//	authoritative response_counts (Redis HGETALL) + top-N leaderboard
//	(Redis ZREVRANGE).
//
// It ALSO probes the cross-pod behaviour: a second base URL (-b, a DIFFERENT
// pod) is asked to open a WS for the SAME session. Because the LiveQuizSession
// aggregate is currently an in-memory per-pod repo (cmd/server/main.go wires
// inmem.NewClassroomSessionRepo), the non-owning pod cannot resolve the session
// and 404s — the smoke reports this explicitly so the per-pod-session limit
// (vs the cross-pod-correct Redis backplane) is documented, not hidden.
//
// Usage (after `kubectl port-forward` of two pods to two local ports):
//
//	go run ./cmd/smoke-classroom \
//	  -a http://127.0.0.1:18081 -b http://127.0.0.1:18082 \
//	  -tenant <uuid> -instructor <uuid> -learner <uuid>
//
// Exit 0 = same-pod loop PASS. Non-zero = a hard assertion failed.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

func main() {
	a := flag.String("a", "http://127.0.0.1:18081", "pod A base URL (owns the session)")
	b := flag.String("b", "", "pod B base URL (cross-pod probe; optional)")
	tenant := flag.String("tenant", "019e2f93-d586-71b5-8c3d-e2b0d0d50300", "X-Tenant-Id")
	instructor := flag.String("instructor", "019e2f93-d586-71b5-8c3d-e2b0d0d50310", "instructor gcid")
	learner := flag.String("learner", "019e2f93-d586-71b5-8c3d-e2b0d0d50320", "learner gcid")
	flag.Parse()

	sc := &client{base: *a, tenant: *tenant}

	log.Printf("[1] create quiz")
	quiz := sc.do("POST", "/api/v1/live-quizzes", *instructor, "instructor",
		map[string]any{"title": "ADR-168 Smoke"})
	quizID := str(quiz["id"])
	must(quizID != "", "create quiz returned no id: %v", quiz)

	log.Printf("[2] patch question q1 (correct=A)")
	sc.do("PATCH", "/api/v1/live-quizzes/"+quizID, *instructor, "instructor", map[string]any{
		"title": "ADR-168 Smoke",
		"questions": []map[string]any{{
			"question_id":   "q1",
			"prompt":        "Pick A",
			"timer_seconds": 30,
			"points":        1000,
			"options": []map[string]any{
				{"label": "A", "is_correct": true},
				{"label": "B", "is_correct": false},
			},
		}},
	})

	log.Printf("[3] publish quiz")
	sc.do("POST", "/api/v1/live-quizzes/"+quizID+"/publish", *instructor, "instructor", nil)

	log.Printf("[4] start session (ARMED)")
	sess := sc.do("POST", "/api/v1/live-quizzes/"+quizID+"/sessions", *instructor, "instructor", nil)
	sessionID := str(sess["id"])
	must(sessionID != "", "start session returned no id: %v", sess)

	log.Printf("[5] transition LIVE")
	sc.do("POST", "/api/v1/classroom-sessions/"+sessionID+"/start", *instructor, "instructor", nil)

	// Cross-pod session probe (ADR-168 Gap-1 — pg-backed LiveQuizSession).
	if *b != "" {
		probeCrossPod(*b, sessionID, *tenant, *instructor)
	}

	// Cross-pod LivePoll probe (ADR-168 Gap-1b — pg-backed PollStore). A poll
	// created+opened on pod A must resolve its WS on the NON-owning pod B.
	if *b != "" {
		log.Printf("[poll] create + open LivePoll on pod A")
		poll := sc.do("POST", "/api/v1/live-polls", *instructor, "instructor",
			map[string]any{"question": "ADR-168 Poll Smoke", "options": []string{"Red", "Blue"}})
		pollID := str(poll["id"])
		must(pollID != "", "create poll returned no id: %v", poll)
		sc.do("POST", "/api/v1/live-polls/"+pollID+"/open", *instructor, "instructor", nil)
		probeCrossPodPoll(*b, pollID, *tenant, *instructor)
	}

	log.Printf("[6] open WS to pod A for %s", sessionID)
	ws := dialWS(*a, sessionID, *tenant, *instructor)
	defer ws.Close()
	snap := recv(ws, 5*time.Second)
	must(snap.Type == "snapshot", "first frame = %q, want snapshot", snap.Type)
	log.Printf("    ✓ snapshot received (state=%v)", snap.Payload["state"])

	log.Printf("[7] advance q1")
	sc.do("POST", "/api/v1/classroom-sessions/"+sessionID+"/advance", *instructor, "instructor",
		map[string]any{"question_id": "q1"})

	log.Printf("[8] submit graded response (learner, choice=A)")
	sc.do("POST", "/api/v1/classroom-sessions/"+sessionID+"/responses", *learner, "learner",
		map[string]any{"question_id": "q1", "choice": "A"})

	log.Printf("[9] await answer_graded frame over Redis-backed fan-out")
	var graded wsFrame
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		f := recv(ws, 8*time.Second)
		if f.Type == "answer_graded" {
			graded = f
			break
		}
		log.Printf("    (saw %q, waiting for answer_graded)", f.Type)
	}
	must(graded.Type == "answer_graded", "no answer_graded frame received within deadline")

	counts, _ := graded.Payload["response_counts"].(map[string]any)
	q1, _ := counts["q1"].(map[string]any)
	must(q1 != nil && num(q1["A"]) == 1, "response_counts[q1][A] != 1 (Redis tally): %v", counts)
	lb, _ := graded.Payload["leaderboard"].([]any)
	must(len(lb) == 1, "leaderboard should have 1 entry (Redis ZSET): %v", graded.Payload["leaderboard"])
	first, _ := lb[0].(map[string]any)
	must(num(first["score"]) > 0, "leaderboard top score should be > 0: %v", first)

	log.Printf("    ✓ answer_graded: counts[q1][A]=%v, leaderboard top score=%v (learner=%v, correct=%v)",
		num(q1["A"]), num(first["score"]), str(first["gcid"]), graded.Payload["correct"])

	fmt.Printf("\nSMOKE PASS — same-pod ADR-168 loop end-to-end over live Redis (tally HGETALL + ZSET ZREVRANGE in the fan-out frame).\n")
	fmt.Printf("Session %s on pod A. Durable score_awarded emitted → verify chora-sharing Ranker separately.\n", sessionID)
}

// -----------------------------------------------------------------------------

type client struct {
	base, tenant string
}

func (c *client) do(method, path, gcid, roles string, body any) map[string]any {
	var rdr io.Reader
	if body != nil {
		bz, _ := json.Marshal(body)
		rdr = bytes.NewReader(bz)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	must(err == nil, "new request %s %s: %v", method, path, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", c.tenant)
	req.Header.Set("gcid", gcid)
	req.Header.Set("x-mesh-user-roles", roles)
	resp, err := http.DefaultClient.Do(req)
	must(err == nil, "%s %s: %v", method, path, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	must(resp.StatusCode >= 200 && resp.StatusCode < 300,
		"%s %s → %d: %s", method, path, resp.StatusCode, string(raw))
	out := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

type wsFrame struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// dialWSAuthed opens an authenticated classroom-realtime WS handshake. In prod
// the gateway stamps X-Tenant-Id + gcid from the validated session JWT; here we
// set them directly so the WS handler's caller-identity + tenant-scope gate
// (CHO-1616) admits the subscribe. Browsers carry the JWT in ?access_token=,
// but this in-cluster smoke talks to chora-delivery past the gateway, so it
// supplies the mesh-trust headers the gateway would have stamped.
func dialWSAuthed(wsURL, tenant, gcid string) (*websocket.Conn, error) {
	cfg, err := websocket.NewConfig(wsURL, "http://localhost/")
	if err != nil {
		return nil, err
	}
	cfg.Header = http.Header{}
	cfg.Header.Set("X-Tenant-Id", tenant)
	cfg.Header.Set("gcid", gcid)
	return websocket.DialConfig(cfg)
}

func dialWS(base, sessionID, tenant, gcid string) *websocket.Conn {
	url := "ws" + strings.TrimPrefix(base, "http") + "/api/v1/live-quizzes/" + sessionID + "/ws"
	ws, err := dialWSAuthed(url, tenant, gcid)
	must(err == nil, "WS dial pod A (%s): %v", url, err)
	return ws
}

func recv(ws *websocket.Conn, timeout time.Duration) wsFrame {
	_ = ws.SetReadDeadline(time.Now().Add(timeout))
	var f wsFrame
	must(websocket.JSON.Receive(ws, &f) == nil, "WS receive timed out / failed")
	return f
}

func probeCrossPod(base, sessionID, tenant, gcid string) {
	url := "ws" + strings.TrimPrefix(base, "http") + "/api/v1/live-quizzes/" + sessionID + "/ws"
	ws, err := dialWSAuthed(url, tenant, gcid)
	// Post-Gap-1 the session/quiz stores are Postgres-backed, so the
	// non-owning pod MUST resolve the session. A failure here is now a
	// REGRESSION (pg ClassroomSessionRepo not wired / DSN unset), not the old
	// "expected per-pod 404".
	must(err == nil, "[cross-pod] WS to pod B FAILED — LiveQuizSession did not resolve cross-pod (pg ClassroomSessionRepo gap / CHORA_DB_DSN_SECRET_ID unset): %v", err)
	ws.Close()
	log.Printf("[cross-pod] WS to pod B SUCCEEDED — session resolves cross-pod (pg-backed session store in place).")
}

// probeCrossPodPoll mirrors probeCrossPod for LivePoll (ADR-168 Gap-1b). A poll
// opened on pod A must resolve its WS on the non-owning pod B once the
// PollStore is Postgres-backed; a failure is a regression.
func probeCrossPodPoll(base, pollID, tenant, gcid string) {
	url := "ws" + strings.TrimPrefix(base, "http") + "/api/v1/live-polls/" + pollID + "/ws"
	ws, err := dialWSAuthed(url, tenant, gcid)
	must(err == nil, "[poll cross-pod] WS to pod B FAILED — LivePoll did not resolve cross-pod (pg PollStore gap): %v", err)
	ws.Close()
	log.Printf("[poll cross-pod] WS to pod B SUCCEEDED — LivePoll resolves cross-pod (pg-backed PollStore in place).")
}

func str(v any) string  { s, _ := v.(string); return s }
func num(v any) float64 { f, _ := v.(float64); return f }

func must(cond bool, format string, args ...any) {
	if !cond {
		fmt.Fprintf(os.Stderr, "SMOKE FAIL — "+format+"\n", args...)
		os.Exit(1)
	}
}
