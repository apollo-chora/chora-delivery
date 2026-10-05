// Package ws provides an in-process WebSocket fan-out broker for the R+
// classroom-realtime stack — Live Quiz (M10) + Live Poll (M11).
//
// Wave-5 brief — server-side WebSocket transport for the two endpoints:
//
//	GET /api/v1/live-quizzes/{sessionId}/ws  — fan-out instructor advancements
//	                                            + response-distribution updates
//	                                            to every connected client
//	GET /api/v1/live-polls/{pollId}/ws       — fan-out vote tally updates to
//	                                            every connected client
//
// Hexagonal: ADAPTER. The broker is wired into Deps at cmd/server boot, then
// injected into both the live-quiz + live-poll WS handlers + the REST mutation
// handlers (instructor advance, learner submit, instructor open/close,
// learner vote) which call Publish on every state change. The classroom
// domain (internal/domain/classroom) does NOT know this broker exists.
//
// Design (mirrors the chora-payments SSE event_broker at 12bb1a2d):
//
//   - **Per-session topic**: every (LiveQuizSession.id, LivePoll.id) maps to
//     its own logical topic. Subscribe(sessionID) returns a fresh per-client
//     bounded channel; Publish(sessionID, msg) routes only to subscribers
//     of that session — strict isolation between concurrent rooms.
//
//   - **Bounded channel + drop-oldest**: the per-client channel is sized at
//     construction (DefaultChannelBuffer = 64). When full, Publish drops the
//     oldest message and queues the new one — a slow FE (laggy network /
//     phone in background) never blocks the publishing handler, which would
//     ripple back into HTTP latency for the instructor's advance click /
//     learner's submit. Drop count is tracked via DroppedTotal for ops.
//
//   - **Cleanup-once**: the closure returned by Subscribe is idempotent +
//     race-free with the broker's own Close() — the channel is closed
//     exactly once, then unregistered. Handlers MUST defer cleanup() on the
//     WS connection's exit path so the broker doesn't leak goroutine-side
//     state per orphaned client.
//
//   - **POD-LOCAL leaf** — this broker fans out only to THIS pod's WS clients.
//     Cross-pod fan-out is provided by the realtime Backplane (ADR-168): a
//     per-pod realtime.FanIn pattern-subscribes to the Redis backplane and
//     re-emits each message into this broker via broker.Publish. The earlier
//     "ride the durable Cloud Pub/Sub outbox for redistribution" sketch is
//     SUPERSEDED by ADR-168 — the hot path is Redis pub/sub + Redis atomic
//     tally/leaderboard; the outbox/DLQ is reserved for durable lifecycle +
//     score_awarded events only. See internal/adapter/realtime/.
//
// Per `.claude/rules/ddd-enforcement.md` cross-DB queries are FORBIDDEN —
// nothing here touches storage; the broker is pure in-memory pub/sub on
// already-mutated domain aggregates.
package ws

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Constants
// -----------------------------------------------------------------------------

// DefaultChannelBuffer is the per-client bounded-channel size. 64 is a
// reasonable middle ground: ~1 second of capture at 64 ev/s peak before
// backpressure kicks in. Matches the chora-payments event_broker default
// at 12bb1a2d.
const DefaultChannelBuffer = 64

// -----------------------------------------------------------------------------
// Message — the envelope every subscriber receives
// -----------------------------------------------------------------------------

// Message is the canonical WebSocket envelope shape per the Wave-5 brief.
//
// Type is either "snapshot" (full current state, sent on Subscribe) or
// "event" (an incremental mutation — instructor advanced, learner submitted,
// poll closed, etc.).
//
// Timestamp is the server-side UTC wall clock at publish time. Payload is
// the typed inner body; the WS handler json-encodes the whole Message before
// sending.
type Message struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload"`
}

// -----------------------------------------------------------------------------
// Broker — per-session in-process fan-out
// -----------------------------------------------------------------------------

// Broker is the in-memory pub/sub primitive. Safe for concurrent
// Subscribe / Publish / Unsubscribe / Close — every public method is
// goroutine-safe.
//
// The zero value is NOT usable; always construct via NewBroker.
type Broker struct {
	bufferSize int

	mu          sync.RWMutex
	subscribers map[string]map[string]*subscriber // sessionID → subscriberID → subscriber
	closed      atomic.Bool

	droppedTotal atomic.Int64
}

// subscriber is one (client, session) tuple.
type subscriber struct {
	id        string
	sessionID string
	ch        chan Message

	// cleanedUp guards the cleanup closure so it runs the close(ch) +
	// unregister exactly once even on concurrent Close + handler-side
	// defer cleanup() races.
	cleanedUp atomic.Bool

	// closeMu serialises the broker fan-out send with the cleanup-time
	// channel close. Sender takes the read lock, checks cleanedUp under it,
	// then sends. Closer takes the write lock, sets cleanedUp, then closes
	// the channel. Because RWMutex.RLock blocks while a writer holds the
	// lock, the channel can never be observed in a concurrent close + send
	// state. This is what prevents the classic "send on closed channel"
	// panic that a naive cleanedUp.Load() check before send would race on.
	closeMu sync.RWMutex
}

// NewBroker constructs a Broker with the given per-client channel buffer.
// bufferSize ≤ 0 → DefaultChannelBuffer.
func NewBroker(bufferSize int) *Broker {
	if bufferSize <= 0 {
		bufferSize = DefaultChannelBuffer
	}
	return &Broker{
		bufferSize:  bufferSize,
		subscribers: make(map[string]map[string]*subscriber),
	}
}

// -----------------------------------------------------------------------------
// Subscribe — open a per-client feed for sessionID
// -----------------------------------------------------------------------------

// Subscribe opens a per-client bounded channel scoped to sessionID. Returns
// the receive-only channel + the cleanup closure the handler MUST defer
// (typically as `defer cleanup()` right after the upgrade succeeds).
//
// Returns an error only if the broker has been Close()d.
func (b *Broker) Subscribe(sessionID string) (<-chan Message, func(), error) {
	if b.closed.Load() {
		return nil, func() {}, errors.New("ws.Broker: closed")
	}
	sub := &subscriber{
		id:        newSubscriberID(),
		sessionID: sessionID,
		ch:        make(chan Message, b.bufferSize),
	}
	b.mu.Lock()
	bySession, ok := b.subscribers[sessionID]
	if !ok {
		bySession = make(map[string]*subscriber)
		b.subscribers[sessionID] = bySession
	}
	bySession[sub.id] = sub
	b.mu.Unlock()

	cleanup := func() {
		b.unsubscribe(sub)
	}
	return sub.ch, cleanup, nil
}

// Unsubscribe is exposed for handlers that want an explicit-name idiom
// instead of the closure returned by Subscribe. Either works — same effect
// (idempotent, single close, unregister).
func (b *Broker) Unsubscribe(sessionID string, ch <-chan Message) {
	b.mu.RLock()
	bySession := b.subscribers[sessionID]
	var target *subscriber
	for _, sub := range bySession {
		if sub.ch == ch {
			target = sub
			break
		}
	}
	b.mu.RUnlock()
	if target != nil {
		b.unsubscribe(target)
	}
}

func (b *Broker) unsubscribe(sub *subscriber) {
	// Take the per-subscriber write lock so any concurrent delivery in
	// progress finishes its select-send before we close the channel. Once
	// cleanedUp is set under the write lock, subsequent deliveries will
	// observe it under the read lock and return without sending.
	sub.closeMu.Lock()
	if sub.cleanedUp.Swap(true) {
		sub.closeMu.Unlock()
		return
	}
	close(sub.ch)
	sub.closeMu.Unlock()

	b.mu.Lock()
	if bySession, ok := b.subscribers[sub.sessionID]; ok {
		delete(bySession, sub.id)
		if len(bySession) == 0 {
			delete(b.subscribers, sub.sessionID)
		}
	}
	b.mu.Unlock()
}

// -----------------------------------------------------------------------------
// Publish — fan out msg to every subscriber on sessionID
// -----------------------------------------------------------------------------

// Publish delivers msg to every subscriber registered for sessionID at the
// time of the call. Non-blocking sends with drop-oldest backpressure: if a
// subscriber's channel is full, the oldest queued Message is dropped + the
// new one is enqueued (DroppedTotal incremented). After-Close Publish is a
// silent no-op.
func (b *Broker) Publish(sessionID string, msg Message) {
	if b.closed.Load() {
		return
	}

	// Stamp the timestamp if the caller did not (defensive — callers in this
	// package always stamp it themselves, but downstream consumers expect
	// monotonically increasing UTC timestamps).
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	b.mu.RLock()
	bySession := b.subscribers[sessionID]
	matches := make([]*subscriber, 0, len(bySession))
	for _, sub := range bySession {
		matches = append(matches, sub)
	}
	b.mu.RUnlock()

	for _, sub := range matches {
		b.deliver(sub, msg)
	}
}

// deliver does a non-blocking send with drop-oldest backpressure. Race-free
// against concurrent unsubscribe / Close via the per-subscriber closeMu —
// see the subscriber.closeMu doc-comment for the protocol.
func (b *Broker) deliver(sub *subscriber, msg Message) {
	sub.closeMu.RLock()
	defer sub.closeMu.RUnlock()
	if sub.cleanedUp.Load() {
		return
	}
	select {
	case sub.ch <- msg:
		return
	default:
		// Channel full — drop oldest, retry once. Both ops are under the
		// closeMu read lock so the channel can't close mid-step.
		select {
		case <-sub.ch:
			b.droppedTotal.Add(1)
		default:
		}
		select {
		case sub.ch <- msg:
			// delivered after drop-oldest
		default:
			// Still full (another concurrent publisher refilled it) — give
			// up + count as dropped.
			b.droppedTotal.Add(1)
		}
	}
}

// -----------------------------------------------------------------------------
// Lifecycle
// -----------------------------------------------------------------------------

// Close drains all subscribers + marks the broker so further Subscribe
// returns an error + further Publish is a no-op. Idempotent.
func (b *Broker) Close() {
	if b.closed.Swap(true) {
		return
	}
	// Snapshot subscribers under the broker lock, then close each under
	// its own per-subscriber write lock so any in-flight deliver() finishes
	// before we close the channel. Doing the closes outside the broker lock
	// avoids holding b.mu while we wait for in-flight RLocks to drain.
	b.mu.Lock()
	snap := make([]*subscriber, 0)
	for _, bySession := range b.subscribers {
		for _, sub := range bySession {
			snap = append(snap, sub)
		}
	}
	b.subscribers = make(map[string]map[string]*subscriber)
	b.mu.Unlock()

	for _, sub := range snap {
		sub.closeMu.Lock()
		if !sub.cleanedUp.Swap(true) {
			close(sub.ch)
		}
		sub.closeMu.Unlock()
	}
}

// SubscriberCount returns the total number of attached subscribers across
// every session. Exposed for ops dashboards + tests.
func (b *Broker) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	total := 0
	for _, bySession := range b.subscribers {
		total += len(bySession)
	}
	return total
}

// SubscriberCountForSession returns the number of attached subscribers for
// one session. Exposed for ops dashboards + tests.
func (b *Broker) SubscriberCountForSession(sessionID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers[sessionID])
}

// DroppedTotal returns the cumulative number of messages dropped due to
// slow-client backpressure. Exposed for ops dashboards.
func (b *Broker) DroppedTotal() int64 {
	return b.droppedTotal.Load()
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func newSubscriberID() string {
	return "wsclient-" + uuid.Must(uuid.NewV7()).String()
}
