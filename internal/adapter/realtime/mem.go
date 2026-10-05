// mem.go — in-memory realtime adapters (ADR-168). These are the no-Redis dev
// fallback AND the unit-test fake. They are POD-LOCAL — correct only on a
// single pod — so production MUST bind the Redis impls (redis.go). Boot wiring
// logs a loud warning when it falls back to these (per feedback_no_stubs_real_wiring:
// real wiring, fail loud about the single-pod limitation).
package realtime

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// -----------------------------------------------------------------------------
// MemBackplane — in-process pub/sub with trailing-'*' glob pattern matching.
// -----------------------------------------------------------------------------

type memSub struct {
	pattern string
	ch      chan BackplaneMessage
}

// MemBackplane is a pod-local pub/sub. No cross-pod distribution.
type MemBackplane struct {
	mu   sync.Mutex
	subs map[*memSub]struct{}
}

// NewMemBackplane returns an empty in-process backplane.
func NewMemBackplane() *MemBackplane {
	return &MemBackplane{subs: make(map[*memSub]struct{})}
}

func patternMatch(pattern, channel string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(channel, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == channel
}

// Publish delivers payload to every subscriber whose pattern matches channel.
// Non-blocking per subscriber (drop on full buffer) so a slow consumer never
// stalls the publisher — mirrors the ws.Broker drop-oldest discipline.
func (b *MemBackplane) Publish(_ context.Context, channel string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		if !patternMatch(s.pattern, channel) {
			continue
		}
		select {
		case s.ch <- BackplaneMessage{Channel: channel, Payload: payload}:
		default:
		}
	}
	return nil
}

// PSubscribe registers a pattern subscriber. The cancel func unsubscribes and
// closes the channel; it is idempotent.
func (b *MemBackplane) PSubscribe(ctx context.Context, pattern string) (<-chan BackplaneMessage, func() error, error) {
	s := &memSub{pattern: pattern, ch: make(chan BackplaneMessage, 256)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() error {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, s)
			b.mu.Unlock()
			close(s.ch)
		})
		return nil
	}
	// auto-unsubscribe on ctx cancellation
	go func() {
		<-ctx.Done()
		_ = cancel()
	}()
	return s.ch, cancel, nil
}

// -----------------------------------------------------------------------------
// MemTally — per-(session, question) option counts.
// -----------------------------------------------------------------------------

// MemTally is a pod-local TallyStore.
type MemTally struct {
	mu sync.Mutex
	// session → question → choice → count
	by map[string]map[string]map[string]int64
}

// NewMemTally returns an empty in-memory tally store.
func NewMemTally() *MemTally {
	return &MemTally{by: make(map[string]map[string]map[string]int64)}
}

// Incr bumps the count for (session, question, choice) and returns the total.
func (t *MemTally) Incr(_ context.Context, sessionID, questionID, choice string) (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.by[sessionID] == nil {
		t.by[sessionID] = make(map[string]map[string]int64)
	}
	if t.by[sessionID][questionID] == nil {
		t.by[sessionID][questionID] = make(map[string]int64)
	}
	t.by[sessionID][questionID][choice]++
	return t.by[sessionID][questionID][choice], nil
}

// Snapshot returns a fresh choice→count copy for one question.
func (t *MemTally) Snapshot(_ context.Context, sessionID, questionID string) (map[string]int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]int64)
	for choice, n := range t.by[sessionID][questionID] {
		out[choice] = n
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// MemLeaderboard — per-session cumulative scores.
// -----------------------------------------------------------------------------

// MemLeaderboard is a pod-local LeaderboardStore.
type MemLeaderboard struct {
	mu sync.Mutex
	// session → gcid → cumulative score
	by map[string]map[string]int64
}

// NewMemLeaderboard returns an empty in-memory leaderboard store.
func NewMemLeaderboard() *MemLeaderboard {
	return &MemLeaderboard{by: make(map[string]map[string]int64)}
}

// Credit adds points to a participant's cumulative score and returns the total.
func (l *MemLeaderboard) Credit(_ context.Context, sessionID, gcid string, points int64) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.by[sessionID] == nil {
		l.by[sessionID] = make(map[string]int64)
	}
	l.by[sessionID][gcid] += points
	return l.by[sessionID][gcid], nil
}

// Top returns the n highest scorers, rank 1 first. Ties break by GCID for a
// stable order.
func (l *MemLeaderboard) Top(_ context.Context, sessionID string, n int) ([]LeaderboardEntry, error) {
	l.mu.Lock()
	entries := make([]LeaderboardEntry, 0, len(l.by[sessionID]))
	for gcid, score := range l.by[sessionID] {
		entries = append(entries, LeaderboardEntry{GCID: gcid, Score: score})
	}
	l.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		return entries[i].GCID < entries[j].GCID
	})
	if n >= 0 && len(entries) > n {
		entries = entries[:n]
	}
	for i := range entries {
		entries[i].Rank = i + 1
	}
	return entries, nil
}
