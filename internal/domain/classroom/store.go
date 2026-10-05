// store.go — repository ports for the classroom-realtime aggregates (ADR-168).
//
// These interfaces let the HTTP layer (Deps) + producers depend on an
// abstraction rather than the concrete in-memory repo, so the session/quiz
// state can be backed by Postgres for true multi-pod operation: a participant's
// WebSocket can land on ANY pod and still resolve the session (the in-memory
// repos are per-pod, so cross-pod WS 404s — see
// docs/SMOKE_RPLUS_REALTIME_ADR168_2026-05-29.md gap 1). The Redis backplane /
// tally / ZSET are already cross-pod-correct; this closes the session-resolution
// gap.
//
// Hexagonal: the DOMAIN owns the port; adapters (inmem, pg) implement it. The
// method set matches the existing in-memory repos exactly so the swap is a
// drop-in (no call-site changes beyond the Deps field type). Note: these ports
// intentionally omit context.Context to stay a drop-in for the existing handler
// call sites; the pg implementation applies its own per-query timeout.
package classroom

import "errors"

// ErrSessionNotFound is returned by SessionStore.Mutate when the id resolves
// to no live row — the handler maps it to 404.
var ErrSessionNotFound = errors.New("classroom: session not found")

// SessionStore persists LiveQuizSession aggregates by primary key + tenant.
//
// Save returns an error so a Postgres-backed store can fail loud — a silently
// dropped Save would lose a live session mid-game. Get/GetByJoinCode return an
// error for the same reason (CHO-2184): a read error USED to degrade to
// not-found, so a dead backing store was indistinguishable from an absent
// session and the handler 404'd an outage. ok=false now means a GENUINE miss.
type SessionStore interface {
	// Save inserts or upserts a session by ID (no-op on nil). Returns an error
	// only when a persistent backing store fails; the inmem impl never errors.
	Save(s *LiveQuizSession) error
	// Get returns the session by ID. ok=false is a genuine miss; a read error
	// is returned, never swallowed into not-found (CHO-2184).
	Get(id string) (*LiveQuizSession, bool, error)
	// ListByTenant returns a tenant's sessions (fresh copy; empty on error).
	ListByTenant(tenantID string) []*LiveQuizSession
	// GetByJoinCode resolves a tenant's ACTIVE (ARMED|LIVE) session by its
	// normalised join code, newest first when codes ever collide. ok=false is a
	// genuine miss; a read error is returned (L5.2, ADR-179 ruling 6; CHO-2184).
	GetByJoinCode(tenantID, code string) (*LiveQuizSession, bool, error)
	// Mutate atomically loads, mutates (fn) and persists the session,
	// serialised against concurrent mutators — pg: SELECT … FOR UPDATE inside
	// one tx; inmem: repo-level mutex. Concurrent learner submits are the
	// whole point of the live classroom, and a read-modify-write race on the
	// JSONB snapshot would silently drop responses (L5.2 hardening). An fn
	// error aborts the write and propagates verbatim; an unknown id returns
	// ErrSessionNotFound.
	Mutate(id string, fn func(*LiveQuizSession) error) error
}

// QuizStore persists LiveQuiz aggregates by primary key + tenant.
type QuizStore interface {
	// Save inserts or upserts a quiz by ID (no-op on nil). Returns an error
	// only when a persistent backing store fails; the inmem impl never errors.
	Save(q *LiveQuiz) error
	// Get returns the quiz by ID. ok=false is a genuine miss; a read error is
	// returned, never swallowed into not-found (CHO-2184).
	Get(id string) (*LiveQuiz, bool, error)
	// ListByTenant returns a tenant's quizzes (fresh copy; empty on error).
	ListByTenant(tenantID string) []*LiveQuiz
}

// PollStore persists LivePoll aggregates by primary key + tenant (ADR-168
// gap-1b — the LivePoll sibling of SessionStore/QuizStore). Same per-pod
// limitation: an in-memory poll repo 404s a learner's vote/WS on any non-owning
// pod, so the prod adapter is Postgres-backed for cross-pod resolution.
//
// The LivePoll aggregate carries an UNEXPORTED `voters` set (first-vote-wins),
// which a default JSONB snapshot would drop — the pg adapter relies on
// LivePoll's own MarshalJSON/UnmarshalJSON to keep the snapshot lossless (see
// domain/classroom/live_poll.go). The method set mirrors QuizStore exactly so
// the swap is a drop-in: Save fails loud (a dropped Save loses a live poll's
// votes); Get returns its error too (CHO-2184 — a swallowed read error made a
// dead store look like an absent poll).
type PollStore interface {
	// Save inserts or upserts a poll by ID (no-op on nil). Returns an error
	// only when a persistent backing store fails; the inmem impl never errors.
	Save(p *LivePoll) error
	// Get returns the poll by ID. ok=false is a genuine miss; a read error is
	// returned, never swallowed into not-found (CHO-2184).
	Get(id string) (*LivePoll, bool, error)
	// ListByTenant returns a tenant's polls (fresh copy; empty on error).
	ListByTenant(tenantID string) []*LivePoll
}
