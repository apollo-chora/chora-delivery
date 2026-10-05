// realtime_ext_test.go — coverage top-ups for the in-memory realtime
// adapters (ADR-168) + the thin go-redis wrappers' ERROR paths.
//
// The go-redis wrappers remain a documented plateau for SUCCESS paths: they
// need a live Memorystore/Redis endpoint (miniredis is unavailable offline,
// scale-smoke covers the happy path). What CAN be driven here without a server
// or a client mock is the genuine failure path — pointing the REAL go-redis
// client at a dead loopback port and asserting the error surfaces (dial-refused
// on loopback is immediate, so no dial-timeout wait). That exercises the code
// in redis.go that is not the documented plateau: key building (tallyKey /
// lbKey), error guards, and the Close of an unconnected client.
package realtime

import (
	"context"
	"testing"
	"time"
)

// scriptedBackplane lets a test force PSubscribe failures / closed channels.
type scriptedBackplane struct {
	subErr error
	ch     chan BackplaneMessage
}

func (s *scriptedBackplane) Publish(_ context.Context, _ string, _ []byte) error { return nil }

func (s *scriptedBackplane) PSubscribe(_ context.Context, _ string) (<-chan BackplaneMessage, func() error, error) {
	if s.subErr != nil {
		return nil, nil, s.subErr
	}
	return s.ch, func() error { return nil }, nil
}

func TestPatternMatch_ExactChannel(t *testing.T) {
	// Exact (non-glob) patterns match only the identical channel.
	if !patternMatch("rt:quiz:s1", "rt:quiz:s1") {
		t.Error("exact pattern must match its own channel")
	}
	if patternMatch("rt:quiz:s1", "rt:quiz:s2") {
		t.Error("exact pattern must not match another channel")
	}
	// Glob prefix is not a suffix — a non-matching prefix glob is false.
	if patternMatch("rt:quiz:*", "rt:poll:p1") {
		t.Error("glob pattern must not match an unrelated prefix")
	}
}

func TestMemBackplane_ExactPatternSubscriber(t *testing.T) {
	ctx := context.Background()
	bp := NewMemBackplane()
	ch, cancel, err := bp.PSubscribe(ctx, "rt:quiz:s1") // EXACT — no trailing '*'
	if err != nil {
		t.Fatalf("PSubscribe: %v", err)
	}
	defer cancel()

	if err := bp.Publish(ctx, "rt:quiz:s1", []byte("exact-hit")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// A sibling channel must NOT reach an exact-pattern subscriber.
	_ = bp.Publish(ctx, "rt:quiz:s2", []byte("glob-miss"))

	select {
	case msg := <-ch:
		if msg.Channel != "rt:quiz:s1" || string(msg.Payload) != "exact-hit" {
			t.Fatalf("unexpected message: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("exact-pattern subscriber missed the message")
	}
	select {
	case msg := <-ch:
		t.Fatalf("cross-channel leak to exact subscriber: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMemBackplane_PublishDropsOnFullBuffer(t *testing.T) {
	ctx := context.Background()
	bp := NewMemBackplane()
	_, cancel, err := bp.PSubscribe(ctx, "rt:quiz:*")
	if err != nil {
		t.Fatalf("PSubscribe: %v", err)
	}
	defer cancel()

	// The buffer is 256; a subscriber that never reads must not stall the
	// publisher — surplus messages are dropped (select-default discipline).
	for i := 0; i < 300; i++ {
		if err := bp.Publish(ctx, "rt:quiz:s1", []byte("payload")); err != nil {
			t.Fatalf("Publish #%d: %v", i, err)
		}
	}
}

func TestMemLeaderboard_TopNegativeLimitReturnsAll(t *testing.T) {
	ctx := context.Background()
	lb := NewMemLeaderboard()
	for _, g := range []string{"a", "b", "c"} {
		if _, err := lb.Credit(ctx, "s1", g, 100); err != nil {
			t.Fatalf("Credit: %v", err)
		}
	}
	// n < 0 disables the cap — every entry must come back.
	top, err := lb.Top(ctx, "s1", -1)
	if err != nil {
		t.Fatalf("Top: %v", err)
	}
	if len(top) != 3 {
		t.Fatalf("Top(-1): want 3 entries, got %d", len(top))
	}
	if top[0].Rank != 1 || top[2].Rank != 3 {
		t.Fatalf("ranks wrong: %+v", top)
	}
}

func TestMemLeaderboard_TopZeroLimitReturnsNone(t *testing.T) {
	ctx := context.Background()
	lb := NewMemLeaderboard()
	_, _ = lb.Credit(ctx, "s1", "a", 10)
	top, err := lb.Top(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Top: %v", err)
	}
	if len(top) != 0 {
		t.Fatalf("Top(0): want 0 entries, got %d", len(top))
	}
}

func TestMemLeaderboard_TieBreaksByGCID(t *testing.T) {
	ctx := context.Background()
	lb := NewMemLeaderboard()
	// Identical scores — the stable order must be ascending GCID.
	for _, g := range []string{"b", "a", "c"} {
		if _, err := lb.Credit(ctx, "s1", g, 42); err != nil {
			t.Fatalf("Credit: %v", err)
		}
	}
	top, err := lb.Top(ctx, "s1", 3)
	if err != nil {
		t.Fatalf("Top: %v", err)
	}
	if top[0].GCID != "a" || top[1].GCID != "b" || top[2].GCID != "c" {
		t.Fatalf("tie order wrong: %+v", top)
	}
}

func TestFanIn_SubscribeErrorPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bp := &scriptedBackplane{subErr: context.Canceled}
	fan := NewFanIn(bp, "rt:quiz:*", func(string, []byte) {})
	if err := fan.Run(ctx); err == nil {
		t.Fatal("a PSubscribe failure must surface from Run")
	}
}

func TestFanIn_ClosedChannelIsCleanShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := make(chan BackplaneMessage)
	close(closed)
	bp := &scriptedBackplane{ch: closed}
	fan := NewFanIn(bp, "rt:quiz:*", func(string, []byte) {})
	if err := fan.Run(ctx); err != nil {
		t.Fatalf("closed subscription channel must be a clean nil return; got %v", err)
	}
}

func TestSessionIDFromChannel_EdgeForms(t *testing.T) {
	// Prefix present but nothing is left after the LAST colon.
	if got := SessionIDFromChannel("rt:"); got != "" {
		t.Fatalf("rt: must yield empty; got %q", got)
	}
	if got := SessionIDFromChannel("rt:quiz:"); got != "" {
		t.Fatalf("rt:quiz: must yield empty; got %q", got)
	}
	// Non-realtime channel.
	if got := SessionIDFromChannel("nort:quiz:s1"); got != "" {
		t.Fatalf("non-realtime channel must yield empty; got %q", got)
	}
	// Multiple colons — segment after the LAST colon wins.
	if got := SessionIDFromChannel("rt:quiz:s1:sub"); got != "sub" {
		t.Fatalf("last-colon segment expected 'sub'; got %q", got)
	}
}

func TestTallyAndLeaderboardKeyBuilders(t *testing.T) {
	if got := tallyKey("s1", "q1"); got != "rt:tally:s1:q1" {
		t.Fatalf("tallyKey: %q", got)
	}
	if got := lbKey("s1"); got != "rt:lb:s1" {
		t.Fatalf("lbKey: %q", got)
	}
}

// -----------------------------------------------------------------------------
// The go-redis wrappers against a DEAD loopback port. No server, no client
// mock — the real go-redis dial fails instantly on the loopback interface
// (ECONNREFUSED), which is enough to drive the error guard code that does NOT
// belong to the documented scale-smoke plateau.
// -----------------------------------------------------------------------------

// deadRedisAddr is a closed loopback port — dial-refused, no timeout wait.
const deadRedisAddr = "127.0.0.1:1"

func TestRedisMethods_DeadEndpoint_ErrorPaths(t *testing.T) {
	r, err := NewRedis(RedisConfig{Addr: deadRedisAddr})
	if err != nil {
		t.Fatalf("NewRedis must not dial; got %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := r.Ping(ctx); err == nil {
		t.Error("Ping against a dead endpoint must error")
	}
	if err := r.Publish(ctx, "rt:quiz:s1", []byte("x")); err == nil {
		t.Error("Publish against a dead endpoint must error")
	}
	if _, err := r.Incr(ctx, "s1", "q1", "A"); err == nil {
		t.Error("Incr against a dead endpoint must error")
	}
	if _, err := r.Snapshot(ctx, "s1", "q1"); err == nil {
		t.Error("Snapshot against a dead endpoint must error")
	}
	if _, err := r.Credit(ctx, "s1", "g", 1); err == nil {
		t.Error("Credit against a dead endpoint must error")
	}
	if _, err := r.Top(ctx, "s1", 5); err == nil {
		t.Error("Top against a dead endpoint must error")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close on an unconnected client must be nil; got %v", err)
	}
}

func TestRedisPSubscribe_DeadEndpoint_ReturnsSubscriptionHandle(t *testing.T) {
	r, err := NewRedis(RedisConfig{Addr: deadRedisAddr})
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, unsub, err := r.PSubscribe(ctx, "rt:quiz:*")
	if err != nil {
		t.Fatalf("PSubscribe must return a handle without dialing; got %v", err)
	}
	if ch == nil {
		t.Fatal("PSubscribe returned a nil channel")
	}
	if unsub == nil {
		t.Fatal("PSubscribe returned a nil cancel func")
	}
	if err := unsub(); err != nil {
		t.Fatalf("unsub: %v", err)
	}
}

func TestRedisPSubscribe_ContextCancellation(t *testing.T) {
	r, err := NewRedis(RedisConfig{Addr: deadRedisAddr})
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	ch, unsub, err := r.PSubscribe(ctx, "rt:quiz:*")
	if err != nil {
		t.Fatalf("PSubscribe: %v", err)
	}
	// Cancelling the context must eventually close the message channel
	// (the pump observes ctx.Done / the underlying subscription dies).
	cancel()
	_ = unsub()
	select {
	case _, ok := <-ch:
		if ok {
			t.Log("channel still open immediately after cancel — acceptable (async close)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after cancel + unsub")
	}
}
