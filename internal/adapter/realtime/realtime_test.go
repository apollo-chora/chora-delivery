// realtime_test.go — TDD for the classroom-realtime adapter layer (ADR-168).
// Tests run against the in-memory impls (MemBackplane / MemTally /
// MemLeaderboard) which are the real no-Redis dev fallback AND the unit-test
// fake — the thin go-redis wrappers are exercised in the scale-smoke
// (miniredis is not available offline). Per feedback_strict_tdd.
package realtime

import (
	"context"
	"testing"
	"time"
)

func TestChannelRoundTrip(t *testing.T) {
	if got := QuizChannel("s1"); got != "rt:quiz:s1" {
		t.Fatalf("QuizChannel=%q", got)
	}
	if got := PollChannel("p1"); got != "rt:poll:p1" {
		t.Fatalf("PollChannel=%q", got)
	}
	if got := SessionIDFromChannel("rt:quiz:s1"); got != "s1" {
		t.Fatalf("SessionIDFromChannel(quiz)=%q", got)
	}
	if got := SessionIDFromChannel("rt:poll:p1"); got != "p1" {
		t.Fatalf("SessionIDFromChannel(poll)=%q", got)
	}
	if got := SessionIDFromChannel("garbage"); got != "" {
		t.Fatalf("SessionIDFromChannel(garbage) should be empty; got %q", got)
	}
}

func TestMemBackplane_PubSubPatternFanOut(t *testing.T) {
	ctx := context.Background()
	bp := NewMemBackplane()
	ch, cancel, err := bp.PSubscribe(ctx, "rt:quiz:*")
	if err != nil {
		t.Fatalf("PSubscribe err: %v", err)
	}
	defer cancel()

	if err := bp.Publish(ctx, "rt:quiz:s1", []byte("hello")); err != nil {
		t.Fatalf("Publish err: %v", err)
	}
	// non-matching pattern must NOT be delivered to this subscriber.
	_ = bp.Publish(ctx, "rt:poll:p1", []byte("ignored"))

	select {
	case msg := <-ch:
		if msg.Channel != "rt:quiz:s1" || string(msg.Payload) != "hello" {
			t.Fatalf("unexpected msg: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for matching message")
	}
	// the poll publish must not arrive
	select {
	case msg := <-ch:
		t.Fatalf("got unexpected cross-pattern message: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMemTally_IncrAndSnapshot(t *testing.T) {
	ctx := context.Background()
	t1 := NewMemTally()
	if _, err := t1.Incr(ctx, "s1", "q1", "A"); err != nil {
		t.Fatalf("Incr err: %v", err)
	}
	n, _ := t1.Incr(ctx, "s1", "q1", "A")
	if n != 2 {
		t.Fatalf("want 2 after two incrs; got %d", n)
	}
	_, _ = t1.Incr(ctx, "s1", "q1", "B")
	snap, err := t1.Snapshot(ctx, "s1", "q1")
	if err != nil {
		t.Fatalf("Snapshot err: %v", err)
	}
	if snap["A"] != 2 || snap["B"] != 1 {
		t.Fatalf("snapshot wrong: %#v", snap)
	}
	// a different question is isolated
	if other, _ := t1.Snapshot(ctx, "s1", "q2"); len(other) != 0 {
		t.Fatalf("q2 snapshot should be empty; got %#v", other)
	}
}

func TestMemLeaderboard_CreditAndTop(t *testing.T) {
	ctx := context.Background()
	lb := NewMemLeaderboard()
	_, _ = lb.Credit(ctx, "s1", "gcid-a", 1000)
	_, _ = lb.Credit(ctx, "s1", "gcid-b", 500)
	total, err := lb.Credit(ctx, "s1", "gcid-a", 200)
	if err != nil {
		t.Fatalf("Credit err: %v", err)
	}
	if total != 1200 {
		t.Fatalf("cumulative total want 1200; got %d", total)
	}
	top, err := lb.Top(ctx, "s1", 10)
	if err != nil {
		t.Fatalf("Top err: %v", err)
	}
	if len(top) != 2 {
		t.Fatalf("want 2 entries; got %d", len(top))
	}
	if top[0].GCID != "gcid-a" || top[0].Score != 1200 || top[0].Rank != 1 {
		t.Fatalf("rank-1 wrong: %+v", top[0])
	}
	if top[1].GCID != "gcid-b" || top[1].Rank != 2 {
		t.Fatalf("rank-2 wrong: %+v", top[1])
	}
}

func TestMemLeaderboard_TopRespectsLimit(t *testing.T) {
	ctx := context.Background()
	lb := NewMemLeaderboard()
	for i, g := range []string{"a", "b", "c"} {
		_, _ = lb.Credit(ctx, "s1", g, int64(100*(i+1)))
	}
	top, _ := lb.Top(ctx, "s1", 2)
	if len(top) != 2 {
		t.Fatalf("limit not honoured; got %d", len(top))
	}
	if top[0].GCID != "c" || top[1].GCID != "b" {
		t.Fatalf("top order wrong: %+v", top)
	}
}

func TestFanIn_RoutesByChannelToBroker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bp := NewMemBackplane()

	type delivered struct {
		id      string
		payload []byte
	}
	got := make(chan delivered, 4)
	fan := NewFanIn(bp, "rt:quiz:*", func(sessionID string, payload []byte) {
		got <- delivered{id: sessionID, payload: payload}
	})
	go func() { _ = fan.Run(ctx) }()

	// give the subscriber a moment to register
	time.Sleep(20 * time.Millisecond)
	_ = bp.Publish(ctx, "rt:quiz:sess-7", []byte("event"))

	select {
	case d := <-got:
		if d.id != "sess-7" || string(d.payload) != "event" {
			t.Fatalf("fan-in delivered wrong: %+v", d)
		}
	case <-time.After(time.Second):
		t.Fatalf("fan-in did not deliver")
	}
}
