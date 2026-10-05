// live_poll_ws_handler.go — HTTP/WebSocket adapter for the Live Poll
// fan-out endpoint (Wave-5, R+ classroom-realtime M11).
//
// One endpoint:
//
//	GET /api/v1/live-polls/{pollId}/ws  — WebSocket upgrade.
//
// Pattern mirrors live_quiz_ws_handler.go exactly — see that file for the
// design rationale. The poll variant is a simpler aggregate (single
// question + per-option tallies vs. per-(learner, question) responses) so
// the snapshot payload is smaller but the lifecycle is identical:
//
//  1. Resolve pollId via LivePollPort.Get (404 on miss).
//  2. Subscribe to the broker for pollId.
//  3. Send `snapshot` envelope.
//  4. Pump every subsequent broker Message.
//  5. Cleanup on disconnect.
//
// What is NOT here:
//   - Learner POST /api/v1/live-polls/{id}/votes — REST mutation owned by
//     agent ι. When it lands, it MUST call
//     deps.LivePollsBroker.Publish(pollID, Message{Type: "event",
//     Payload: <vote_distribution_snapshot>}).
//   - Instructor POST /api/v1/live-polls/{id}/open + .../close — same.
//
// Per `.claude/rules/ddd-enforcement.md` cross-DB queries FORBIDDEN.
package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"github.com/apollo-chora/chora-delivery/internal/adapter/realtime"
	wsadapter "github.com/apollo-chora/chora-delivery/internal/adapter/ws"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// -----------------------------------------------------------------------------
// Port — what the live-poll WS handler needs from the repo
// -----------------------------------------------------------------------------

// LivePollPort is the minimal repo surface for the WS fan-out handler.
// Production (M12+) wires a Postgres adapter; in-memory adapter satisfies
// trivially.
//
// The error is part of the contract (CHO-2184): a dead backing store must not
// arrive here as ok=false, or the WS 404s an outage as an absent poll.
type LivePollPort interface {
	Get(ctx context.Context, id string) (*classroom.LivePoll, bool, error)
}

// -----------------------------------------------------------------------------
// LivePollWSHandler — public constructor wired by handlers.go integration diff
// -----------------------------------------------------------------------------

// LivePollWSHandler returns the http.Handler for
// GET /api/v1/live-polls/{pollId}/ws.
//
// tally is the ADR-168 cross-pod TallyStore. When non-nil the snapshot-on-
// connect sources per-option vote counts from it (authoritative across HPA-
// scaled pods) so a client connecting to a non-mutating pod still sees the
// live distribution. When nil (no Redis in dev) it falls back to the pod-local
// aggregate's VoteCount.
func LivePollWSHandler(repo LivePollPort, broker *wsadapter.Broker, tally realtime.TallyStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repo == nil || broker == nil {
			writeError(w, http.StatusServiceUnavailable, "live-poll WS not wired")
			return
		}

		pollID, ok := extractLivePollWSPollID(r.URL.Path)
		if !ok {
			writeError(w, http.StatusNotFound, "poll id required")
			return
		}

		// ADR-168 / CHO-1616 — authenticate BEFORE the lookup (no existence
		// oracle); gateway stamps X-Tenant-Id + gcid from the validated JWT.
		callerTenant, ok := requireWSCallerIdentity(w, r)
		if !ok {
			return
		}

		poll, found, err := repo.Get(r.Context(), pollID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "live poll lookup failed: "+err.Error())
			return
		}
		if !found || poll == nil {
			writeError(w, http.StatusNotFound, "live poll not found")
			return
		}

		// Tenant-scope the subscribe — cross-tenant caller gets 404 (no leak).
		if !wsTenantMatches(callerTenant, poll.TenantID) {
			writeError(w, http.StatusNotFound, "live poll not found")
			return
		}

		ch, cleanup, err := broker.Subscribe(pollID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "broker unavailable: "+err.Error())
			return
		}

		snapshot := wsadapter.Message{
			Type:      "snapshot",
			Timestamp: time.Now().UTC(),
			Payload:   buildLivePollSnapshot(r.Context(), poll, tally),
		}

		handler := websocket.Handler(func(conn *websocket.Conn) {
			defer cleanup()
			defer conn.Close()

			if err := websocket.JSON.Send(conn, snapshot); err != nil {
				return
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				var sink any
				for {
					if err := websocket.JSON.Receive(conn, &sink); err != nil {
						return
					}
				}
			}()
			for {
				select {
				case <-done:
					return
				case msg, ok := <-ch:
					if !ok {
						return
					}
					if err := websocket.JSON.Send(conn, msg); err != nil {
						return
					}
				}
			}
		})

		handler.ServeHTTP(w, r)
	})
}

// -----------------------------------------------------------------------------
// Snapshot payload
// -----------------------------------------------------------------------------

// livePollSnapshotPayload is the wire shape inside Message.Payload for the
// initial `snapshot` envelope.
type livePollSnapshotPayload struct {
	ID             string                  `json:"id"`
	TenantID       string                  `json:"tenant_id"`
	CourseID       string                  `json:"course_id,omitempty"`
	InstructorGCID string                  `json:"instructor_gcid"`
	Question       string                  `json:"question"`
	Options        []livePollOptionPayload `json:"options"`
	State          string                  `json:"state"`
	OpenedAt       *time.Time              `json:"opened_at,omitempty"`
	ClosedAt       *time.Time              `json:"closed_at,omitempty"`
	TotalVotes     int                     `json:"total_votes"`
}

type livePollOptionPayload struct {
	Label     string `json:"label"`
	VoteCount int    `json:"vote_count"`
}

// buildLivePollSnapshot projects the aggregate to the wire shape. When the
// cross-pod TallyStore is wired (tally non-nil per ADR-168), per-option vote
// counts come from it (authoritative across pods) instead of the pod-local
// aggregate VoteCount, so a late joiner on a non-mutating pod sees the live
// distribution. The option SET is still defined by the aggregate (frozen at
// construction); only the counts are overlaid.
func buildLivePollSnapshot(ctx context.Context, p *classroom.LivePoll, tally realtime.TallyStore) livePollSnapshotPayload {
	var tallied map[string]int64
	if tally != nil {
		if snap, err := tally.Snapshot(ctx, p.ID, pollTallyQuestion); err == nil && len(snap) > 0 {
			tallied = snap
		}
	}
	opts := make([]livePollOptionPayload, 0, len(p.Options))
	total := 0
	for _, o := range p.Options {
		count := o.VoteCount
		if tallied != nil {
			count = int(tallied[o.Label])
		}
		total += count
		opts = append(opts, livePollOptionPayload{Label: o.Label, VoteCount: count})
	}
	if tallied == nil {
		total = p.TotalVotes()
	}
	return livePollSnapshotPayload{
		ID:             p.ID,
		TenantID:       p.TenantID,
		CourseID:       p.CourseID,
		InstructorGCID: p.InstructorGCID,
		Question:       p.Question,
		Options:        opts,
		State:          string(p.State),
		OpenedAt:       p.OpenedAt,
		ClosedAt:       p.ClosedAt,
		TotalVotes:     total,
	}
}

// -----------------------------------------------------------------------------
// Path parsing
// -----------------------------------------------------------------------------

// extractLivePollWSPollID parses /api/v1/live-polls/{pollId}/ws. Tolerates
// Go's http.ServeMux path-canonicalisation (// collapsed to /) by treating
// the degenerate /api/v1/live-polls/ws shape as missing-id.
func extractLivePollWSPollID(urlPath string) (string, bool) {
	const prefix = "/api/v1/live-polls/"
	const suffix = "/ws"
	if !strings.HasPrefix(urlPath, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(urlPath, prefix)
	if !strings.HasSuffix(rest, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(rest, suffix)
	id = strings.Trim(id, "/")
	if id == "" {
		return "", false
	}
	return id, true
}
