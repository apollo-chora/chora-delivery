// live_poll_producer.go — ADR-168 classroom-realtime PRODUCER side for the
// LivePoll aggregate. The sibling of live_quiz_producer.go.
//
// A LivePoll is the simpler classroom-realtime primitive: one multi-choice
// question, per-option vote tallies, no scoring/leaderboard. The producer
// surface mirrors the live-quiz one:
//
//	POST /api/v1/live-polls               — create DRAFT poll  [instructor]
//	POST /api/v1/live-polls/{id}/open     — DRAFT → OPEN        [instructor]
//	POST /api/v1/live-polls/{id}/votes    — CastVote + fan-out  [learner]
//	POST /api/v1/live-polls/{id}/close    — OPEN → CLOSED       [instructor]
//	GET  /api/v1/live-polls/{id}          — poll snapshot       [any in-tenant]
//
// Hot path: a vote bumps the authoritative cross-pod TallyStore (Redis HINCRBY
// in prod) and fans out on realtime.PollChannel(pollID); each pod's poll FanIn
// (rt:poll:*) re-emits into its local ws.Broker so connected clients on any pod
// see the update. open/close fan out a state-change frame. All realtime deps
// are nil-guarded — when unwired (no Redis in dev), the aggregate mutation +
// repo Save still happen; only the live fan-out / cross-pod tally are skipped
// (the LivePoll aggregate keeps its own per-pod authoritative VoteCount).
//
// Per .claude/rules/ddd-enforcement.md cross-DB queries FORBIDDEN — the poll
// is resolved only by primary key via the in-mem repo.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/realtime"
	wsadapter "github.com/apollo-chora/chora-delivery/internal/adapter/ws"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// pollTallyQuestion is the sentinel "question id" under which a poll's option
// counts are stored in the TallyStore. A LivePoll has a single implicit
// question, so one fixed key namespaces all its option tallies per poll id.
const pollTallyQuestion = "_poll"

// createLivePollReq is the POST /api/v1/live-polls body.
type createLivePollReq struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	CourseID string   `json:"course_id,omitempty"`
}

// castVoteReq is the POST /api/v1/live-polls/{id}/votes body. `choice` is the
// author-typed option label (per feedback_mcq_option_naming) — never a marker.
type castVoteReq struct {
	Choice string `json:"choice"`
}

// handleCreatePoll creates a DRAFT LivePoll (instructor action).
func handleCreatePoll(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.LivePolls == nil {
		writeError(w, http.StatusServiceUnavailable, "live-polls repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasClassroomInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req createLivePollReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID:       tenantID,
		CourseID:       req.CourseID,
		InstructorGCID: gcid,
		Question:       req.Question,
		Options:        req.Options,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !persistOK(w, deps.LivePolls.Save(p)) {
		return
	}
	writeJSON(w, http.StatusCreated, livePollDTO(p))
}

// handleOpenPoll transitions a poll DRAFT → OPEN (instructor) + fans out.
func handleOpenPoll(deps Deps, pollID string, w http.ResponseWriter, r *http.Request) {
	p, ok := pollForInstructorMutation(deps, pollID, w, r)
	if !ok {
		return
	}
	if err := p.Open(time.Now().UTC()); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if !persistOK(w, deps.LivePolls.Save(p)) {
		return
	}
	fanOutPoll(r.Context(), deps, p, "poll_opened")
	writeJSON(w, http.StatusOK, livePollDTO(p))
}

// handleClosePoll transitions a poll OPEN → CLOSED (instructor) + fans out.
func handleClosePoll(deps Deps, pollID string, w http.ResponseWriter, r *http.Request) {
	p, ok := pollForInstructorMutation(deps, pollID, w, r)
	if !ok {
		return
	}
	if err := p.Close(time.Now().UTC()); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if !persistOK(w, deps.LivePolls.Save(p)) {
		return
	}
	fanOutPoll(r.Context(), deps, p, "poll_closed")
	writeJSON(w, http.StatusOK, livePollDTO(p))
}

// handleCastVote records a learner's vote, bumps the authoritative cross-pod
// tally, and fans out the updated distribution.
func handleCastVote(deps Deps, pollID string, w http.ResponseWriter, r *http.Request) {
	if deps.LivePolls == nil {
		writeError(w, http.StatusServiceUnavailable, "live-polls repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasClassroomLearnerOrInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks learner/instructor role")
		return
	}
	p, ok, err := deps.LivePolls.Get(pollID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "live poll lookup failed: "+err.Error())
		return
	}
	if !ok || p == nil || p.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "live poll not found")
		return
	}
	var req castVoteReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Choice) == "" {
		writeError(w, http.StatusBadRequest, "choice required")
		return
	}
	if err := p.CastVote(gcid, req.Choice, time.Now().UTC()); err != nil {
		switch err {
		case classroom.ErrLivePollDuplicateVote, classroom.ErrLivePollNotOpen:
			writeError(w, http.StatusConflict, err.Error())
		case classroom.ErrLivePollUnknownOption:
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if !persistOK(w, deps.LivePolls.Save(p)) {
		return
	}
	// Authoritative cross-pod tally (no split-brain under HPA).
	if deps.RealtimeTally != nil {
		_, _ = deps.RealtimeTally.Incr(r.Context(), pollID, pollTallyQuestion, req.Choice)
	}
	fanOutPoll(r.Context(), deps, p, "vote_cast")
	writeJSON(w, http.StatusOK, livePollDTO(p))
}

// handleGetPoll returns the poll snapshot (any in-tenant caller).
func handleGetPoll(deps Deps, pollID string, w http.ResponseWriter, r *http.Request) {
	if deps.LivePolls == nil {
		writeError(w, http.StatusServiceUnavailable, "live-polls repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	p, ok, err := deps.LivePolls.Get(pollID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "live poll lookup failed: "+err.Error())
		return
	}
	if !ok || p == nil || p.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "live poll not found")
		return
	}
	writeJSON(w, http.StatusOK, livePollDTO(p))
}

// pollForInstructorMutation resolves + tenant-scopes a poll and enforces the
// instructor role. Returns ok=false (and writes the response) on any failure.
func pollForInstructorMutation(deps Deps, pollID string, w http.ResponseWriter, r *http.Request) (*classroom.LivePoll, bool) {
	if deps.LivePolls == nil {
		writeError(w, http.StatusServiceUnavailable, "live-polls repo not wired")
		return nil, false
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return nil, false
	}
	if !hasClassroomInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return nil, false
	}
	p, ok, err := deps.LivePolls.Get(pollID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "live poll lookup failed: "+err.Error())
		return nil, false
	}
	if !ok || p == nil || p.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "live poll not found")
		return nil, false
	}
	return p, true
}

// fanOutPoll marshals a ws.Message carrying the poll's current vote
// distribution + state and publishes it on the poll's backplane channel. The
// per-pod poll FanIn re-emits it into the local ws.Broker. No-op when the
// backplane is unwired.
func fanOutPoll(ctx context.Context, deps Deps, p *classroom.LivePoll, msgType string) {
	if deps.RealtimeBackplane == nil {
		return
	}
	b, err := json.Marshal(wsadapter.Message{
		Type:      msgType,
		Timestamp: time.Now().UTC(),
		Payload:   livePollDTO(p),
	})
	if err != nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_ = deps.RealtimeBackplane.Publish(ctx, realtime.PollChannel(p.ID), b)
}

// livePollDTO renders a LivePoll for the JSON wire — the shape shared by the
// REST responses and the WS fan-out payloads.
func livePollDTO(p *classroom.LivePoll) map[string]any {
	opts := make([]map[string]any, 0, len(p.Options))
	for _, o := range p.Options {
		opts = append(opts, map[string]any{"label": o.Label, "vote_count": o.VoteCount})
	}
	out := map[string]any{
		"id":              p.ID,
		"tenant_id":       p.TenantID,
		"course_id":       p.CourseID,
		"instructor_gcid": p.InstructorGCID,
		"question":        p.Question,
		"options":         opts,
		"state":           string(p.State),
		"total_votes":     p.TotalVotes(),
		"created_at":      p.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":      p.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if p.OpenedAt != nil {
		out["opened_at"] = p.OpenedAt.UTC().Format(time.RFC3339)
	}
	if p.ClosedAt != nil {
		out["closed_at"] = p.ClosedAt.UTC().Format(time.RFC3339)
	}
	return out
}
