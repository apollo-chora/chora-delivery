// Package realtime is the classroom-realtime adapter layer (ADR-168) for the
// Live Classroom live-quiz / live-poll stack.
//
// It separates the two concerns the original pod-local design conflated:
//
//   - Hot path (this package): a low-latency cross-pod fan-out Backplane +
//     authoritative TallyStore (per-question option counts) + LeaderboardStore
//     (per-session cumulative scores). Production binds these to Memorystore
//     Redis 7.2 (pub/sub + HINCRBY + ZINCRBY/ZREVRANGE); a no-Redis dev
//     fallback uses the Mem* in-memory impls (POD-LOCAL — single-pod only).
//   - Durable path (outbox/events, NOT here): session lifecycle + final
//     results published via the TransactionalPublisher → Cloud Pub/Sub + DLQ
//     for cross-domain consumers (chora-sharing economy, observability).
//
// The pod-local ws.Broker stays as the per-pod *leaf*: a FanIn subscribes to
// the Backplane and re-emits each message into the local broker, which fans
// out to that pod's WebSocket clients. Every pod runs one FanIn, so a vote
// recorded on pod A reaches WS clients on pods A..N.
//
// Hexagonal: this package defines ports + adapters; it never imports the
// domain. The FanIn depends on a deliver callback (dependency inversion) so
// it does not import the ws package directly — boot wiring binds the callback
// to broker.Publish.
package realtime

import (
	"context"
	"strings"
)

// channelPrefix namespaces all realtime pub/sub channels + Redis keys.
const channelPrefix = "rt:"

// QuizChannel is the backplane channel for a live-quiz session's events.
func QuizChannel(sessionID string) string { return channelPrefix + "quiz:" + sessionID }

// PollChannel is the backplane channel for a live-poll's events.
func PollChannel(pollID string) string { return channelPrefix + "poll:" + pollID }

// SessionIDFromChannel extracts the session/poll id from a realtime channel
// (the segment after the last colon). Returns "" for non-realtime channels.
func SessionIDFromChannel(channel string) string {
	if !strings.HasPrefix(channel, channelPrefix) {
		return ""
	}
	i := strings.LastIndex(channel, ":")
	if i < 0 || i+1 >= len(channel) {
		return ""
	}
	return channel[i+1:]
}

// BackplaneMessage is one message received from a pattern subscription.
type BackplaneMessage struct {
	Channel string
	Payload []byte
}

// Backplane is the cross-pod fan-out primitive (Redis pub/sub in production).
type Backplane interface {
	// Publish broadcasts payload on channel to every subscriber across all pods.
	Publish(ctx context.Context, channel string, payload []byte) error
	// PSubscribe returns a channel of messages matching a glob pattern (trailing
	// '*' supported), a cancel func to unsubscribe, and an error. The returned
	// channel is closed when cancel is called or ctx is done.
	PSubscribe(ctx context.Context, pattern string) (<-chan BackplaneMessage, func() error, error)
}

// TallyStore holds authoritative per-(session, question) option counts so
// tallies stay coherent across HPA-scaled pods (no split-brain).
type TallyStore interface {
	// Incr atomically bumps the count for one option choice and returns the
	// new total (Redis HINCRBY).
	Incr(ctx context.Context, sessionID, questionID, choice string) (int64, error)
	// Snapshot returns choice→count for one question (Redis HGETALL, filtered).
	Snapshot(ctx context.Context, sessionID, questionID string) (map[string]int64, error)
}

// LeaderboardEntry is one ranked participant in a session leaderboard.
type LeaderboardEntry struct {
	GCID  string `json:"gcid"`
	Score int64  `json:"score"`
	Rank  int    `json:"rank"`
}

// LeaderboardStore holds per-session cumulative scores as a sorted set so the
// live leaderboard is authoritative + O(log n) to update across pods.
type LeaderboardStore interface {
	// Credit adds points to a participant's cumulative score and returns the
	// new total (Redis ZINCRBY).
	Credit(ctx context.Context, sessionID, gcid string, points int64) (int64, error)
	// Top returns the highest-scoring participants, rank 1 first, capped at n
	// (Redis ZREVRANGE WITHSCORES).
	Top(ctx context.Context, sessionID string, n int) ([]LeaderboardEntry, error)
}
