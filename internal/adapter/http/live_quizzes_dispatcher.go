// live_quizzes_dispatcher.go — composite /api/v1/live-quizzes/ dispatcher
// for the R+ Wave-6 integration (2026-05-26).
//
// THREE handlers share the `/api/v1/live-quizzes/` subtree, each owning
// a different leaf-shape:
//
//	κ liveQuizzesSubHandler        — GET/PATCH/POST {id}/publish (CRUD)
//	ι liveQuizSessionsRootHandler  — POST {quizId}/sessions (start session)
//	μ LiveQuizWSHandler             — WS upgrade on {sessionId}/ws
//
// net/http mux only registers ONE handler per pattern, so we install a
// single composite dispatcher on `/api/v1/live-quizzes/` that inspects
// the path beyond the prefix and routes to the correct sub-handler.
//
// WS path bypasses tenantRequired (the middleware sets
// Content-Type: application/json which conflicts with the
// 101 Switching Protocols handshake per μ's manifest note).
//
// Port-adapter helpers below convert the agent ι/μ Get(id)-style repo
// signatures to μ's context-aware port interfaces. Kept inline here so
// the integration is local — domain repos stay free of WS concerns.
package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// liveQuizzesUnifiedDispatcher routes /api/v1/live-quizzes/{...} to the
// appropriate sub-handler based on the leaf-segment.
//
// Branch order — most-specific to least-specific:
//  1. Path ends `/ws`               → μ LiveQuizWSHandler (no tenantRequired)
//  2. Path ends `/sessions`         → ι liveQuizSessionsRootHandler
//  3. Otherwise (bare {id} + /publish + /grade etc.) → κ liveQuizzesSubHandler
//
// 503 (fail-loud per feedback_no_stubs_real_wiring) when none of the
// sub-handlers' deps are wired.
func liveQuizzesUnifiedDispatcher(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// (1) WS upgrade — bypass tenantRequired entirely.
		if strings.HasSuffix(path, "/ws") {
			if deps.ClassroomSessions == nil || deps.ClassroomRealtimeBroker == nil {
				writeError(w, http.StatusServiceUnavailable, "live-quiz WS not wired")
				return
			}
			handler := LiveQuizWSHandler(
				liveQuizSessionPortFrom(deps.ClassroomSessions),
				deps.LiveQuizzes,
				deps.ClassroomRealtimeBroker,
				deps.RealtimeTally,
				deps.RealtimeLeaderboard,
			)
			handler.ServeHTTP(w, r)
			return
		}

		// (2) Sessions sub-path — start a LiveQuizSession from a LiveQuiz.
		if strings.HasSuffix(path, "/sessions") {
			if deps.ClassroomSessions == nil {
				writeError(w, http.StatusServiceUnavailable, "classroom-sessions repo not wired")
				return
			}
			tenantRequired(liveQuizSessionsRootHandler(deps))(w, r)
			return
		}

		// (3) Default — κ CRUD sub-handler (GET/PATCH on {id}, POST publish).
		if deps.LiveQuizzes == nil {
			writeError(w, http.StatusServiceUnavailable, "live-quizzes repo not wired")
			return
		}
		tenantRequired(liveQuizzesSubHandler(deps))(w, r)
	}
}

// -----------------------------------------------------------------------------
// Port adapters — bridge the agent ι/μ repo Get(id) signature to the μ-side
// context-aware port interfaces.
// -----------------------------------------------------------------------------

// liveQuizSessionPortAdapter satisfies μ's LiveQuizSessionPort by delegating
// to a classroom.SessionStore (whose Get omits the context parameter). The
// store is inmem (dev) or pg (prod multi-pod) per ADR-168 gap 1.
type liveQuizSessionPortAdapter struct {
	repo classroom.SessionStore
}

func liveQuizSessionPortFrom(repo classroom.SessionStore) LiveQuizSessionPort {
	return &liveQuizSessionPortAdapter{repo: repo}
}

func (a *liveQuizSessionPortAdapter) Get(_ context.Context, id string) (*classroom.LiveQuizSession, bool, error) {
	return a.repo.Get(id)
}

// livePollPortAdapter satisfies μ's LivePollPort by delegating to a
// classroom.PollStore (inmem dev | pg prod multi-pod per ADR-168 gap-1b).
type livePollPortAdapter struct {
	repo classroom.PollStore
}

func livePollPortFrom(repo classroom.PollStore) LivePollPort {
	return &livePollPortAdapter{repo: repo}
}

func (a *livePollPortAdapter) Get(_ context.Context, id string) (*classroom.LivePoll, bool, error) {
	return a.repo.Get(id)
}
