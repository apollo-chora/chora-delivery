// live_polls_dispatcher.go — composite /api/v1/live-polls/ dispatcher
// (ADR-168). Mirrors live_quizzes_dispatcher.go.
//
// The /api/v1/live-polls/ subtree carries several leaf-shapes that net/http's
// mux cannot register separately (one handler per pattern), so a single
// composite dispatcher inspects the path beyond the prefix and routes:
//
//	{pollId}/ws      → μ LivePollWSHandler (WS upgrade, no tenantRequired)
//	{pollId}/votes   → handleCastVote      (learner)
//	{pollId}/open    → handleOpenPoll      (instructor)
//	{pollId}/close   → handleClosePoll     (instructor)
//	{pollId}         → handleGetPoll       (GET snapshot, any in-tenant caller)
//
// The WS path branches BEFORE tenantRequired — the tenantRequired middleware
// sets Content-Type: application/json which conflicts with the 101 Switching
// Protocols handshake (per μ's manifest note + ADR-166 §D5).
package httpapi

import (
	"net/http"
	"strings"
)

// livePollsRootHandler dispatches POST /api/v1/live-polls (create DRAFT poll).
func livePollsRootHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handleCreatePoll(deps, w, r)
	}
}

// livePollsUnifiedDispatcher routes /api/v1/live-polls/{...} to the
// appropriate sub-handler based on the leaf-segment.
func livePollsUnifiedDispatcher(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// (1) WS upgrade — bypass tenantRequired entirely.
		if strings.HasSuffix(path, "/ws") {
			if deps.LivePolls == nil || deps.ClassroomRealtimeBroker == nil {
				writeError(w, http.StatusServiceUnavailable, "live-poll WS not wired")
				return
			}
			LivePollWSHandler(livePollPortFrom(deps.LivePolls), deps.ClassroomRealtimeBroker, deps.RealtimeTally).ServeHTTP(w, r)
			return
		}

		rest := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/live-polls/"), "/")
		if rest == "" {
			writeError(w, http.StatusNotFound, "poll id required")
			return
		}
		parts := strings.Split(rest, "/")
		pollID := parts[0]
		if pollID == "" {
			writeError(w, http.StatusNotFound, "poll id required")
			return
		}

		switch len(parts) {
		case 1:
			// /{id} — GET snapshot.
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			tenantRequired(func(w http.ResponseWriter, r *http.Request) {
				handleGetPoll(deps, pollID, w, r)
			})(w, r)
		case 2:
			// /{id}/{action}.
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			action := parts[1]
			handler := func(w http.ResponseWriter, r *http.Request) {
				switch action {
				case "open":
					handleOpenPoll(deps, pollID, w, r)
				case "close":
					handleClosePoll(deps, pollID, w, r)
				case "votes":
					handleCastVote(deps, pollID, w, r)
				default:
					writeError(w, http.StatusNotFound, "not found")
				}
			}
			tenantRequired(handler)(w, r)
		default:
			writeError(w, http.StatusNotFound, "not found")
		}
	}
}
