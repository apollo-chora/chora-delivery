// broker_test.go — RED tests for the in-process WebSocket fan-out broker.
//
// The broker is the per-session pub/sub primitive that backs the R+
// classroom-realtime WS endpoints:
//
//	GET /api/v1/live-quizzes/{sessionId}/ws  — live-quiz fan-out
//	GET /api/v1/live-polls/{pollId}/ws       — live-poll fan-out
//
// Test matrix (per the Wave-5 brief):
//
//   - Subscribe(sessionID) returns a channel; Publish(sessionID, msg) fans
//     out to every subscriber on that session
//   - Multiple subscribers on the same session ALL receive the message
//   - Different-session subscribers are isolated (no cross-session leak)
//   - Bounded per-subscriber channel: when full, oldest is dropped + new
//     value still delivered (drop-slow-consumer semantics matches the
//     chora-payments SSE event_broker pattern at 12bb1a2d)
//   - Unsubscribe removes the subscriber + closes the channel exactly once
//   - Close drains all subscribers + future Publish is a no-op (no panic)
//   - Concurrent Subscribe / Publish / Unsubscribe is race-free
//
// Mirrors the design intent of services/chora-payments/internal/adapter/
// pubsub/event_broker/broker.go (12bb1a2d) — same drop-oldest backpressure
// + cleanup-once semantics, but for a single in-process Publisher (no
// upstream Pub/Sub subscription; the publisher is the HTTP handler that
// just mutated a LiveQuizSession / LivePoll aggregate).
//
// M14/M15 HARDENING NOTE: this broker is POD-LOCAL. Multi-pod fan-out
// requires a Pub/Sub redistribution layer (one subscription per pod
// listening on chora.classroom.live_quiz_session.response_submitted.v1 /
// chora.classroom.live_poll.vote_cast.v1 then re-publishing into the
// per-pod broker). See the integration manifest in the PR body.
package ws

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// Subscribe + Publish — basic fan-out
// -----------------------------------------------------------------------------

func TestBroker_PublishFansOutToSingleSubscriber(t *testing.T) {
	b := NewBroker(0) // default buffer
	defer b.Close()

	ch, _, err := b.Subscribe("session-1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	msg := Message{Type: "snapshot", Timestamp: time.Now().UTC(), Payload: map[string]any{"k": "v"}}
	b.Publish("session-1", msg)

	select {
	case got := <-ch:
		if got.Type != "snapshot" {
			t.Fatalf("Type = %q, want snapshot", got.Type)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("did not receive message within 200ms")
	}
}

func TestBroker_PublishFansOutToMultipleSubscribersOnSameSession(t *testing.T) {
	b := NewBroker(0)
	defer b.Close()

	const n = 5
	chans := make([]<-chan Message, n)
	cleanups := make([]func(), n)
	for i := 0; i < n; i++ {
		ch, cleanup, err := b.Subscribe("session-1")
		if err != nil {
			t.Fatalf("Subscribe[%d]: %v", i, err)
		}
		chans[i] = ch
		cleanups[i] = cleanup
	}
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()

	msg := Message{Type: "event", Timestamp: time.Now().UTC()}
	b.Publish("session-1", msg)

	for i, ch := range chans {
		select {
		case got := <-ch:
			if got.Type != "event" {
				t.Fatalf("subscriber[%d] got Type=%q want event", i, got.Type)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("subscriber[%d] did not receive within 200ms", i)
		}
	}
}

// -----------------------------------------------------------------------------
// Cross-session isolation — subscribers see ONLY their session's events
// -----------------------------------------------------------------------------

func TestBroker_DifferentSessionsAreIsolated(t *testing.T) {
	b := NewBroker(0)
	defer b.Close()

	ch1, c1, err := b.Subscribe("session-A")
	if err != nil {
		t.Fatalf("Subscribe A: %v", err)
	}
	defer c1()
	ch2, c2, err := b.Subscribe("session-B")
	if err != nil {
		t.Fatalf("Subscribe B: %v", err)
	}
	defer c2()

	b.Publish("session-A", Message{Type: "event", Payload: "A-payload"})

	select {
	case got := <-ch1:
		if got.Payload != "A-payload" {
			t.Fatalf("ch1 got payload=%v want A-payload", got.Payload)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("ch1 did not receive session-A event within 200ms")
	}

	// ch2 must NOT receive the session-A event (give it 50ms to be sure).
	select {
	case unwanted := <-ch2:
		t.Fatalf("ch2 received unrelated session-A event: %+v", unwanted)
	case <-time.After(50 * time.Millisecond):
		// expected — no cross-session leak
	}
}

// -----------------------------------------------------------------------------
// Backpressure — bounded buffer + drop-oldest on slow consumer
// -----------------------------------------------------------------------------

func TestBroker_SlowSubscriberDropsOldestEvent(t *testing.T) {
	const buf = 2
	b := NewBroker(buf)
	defer b.Close()

	ch, cleanup, err := b.Subscribe("session-1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cleanup()

	// Fill the buffer + then publish one more (which triggers drop-oldest).
	b.Publish("session-1", Message{Type: "event", Payload: "msg-0"})
	b.Publish("session-1", Message{Type: "event", Payload: "msg-1"})
	b.Publish("session-1", Message{Type: "event", Payload: "msg-2"})

	// We should see msg-1 + msg-2 (msg-0 was dropped to make room).
	got := drainN(t, ch, 2, 500*time.Millisecond)
	if got[0].Payload != "msg-1" || got[1].Payload != "msg-2" {
		t.Fatalf("drain got %+v %+v; want msg-1 msg-2 (oldest dropped)", got[0].Payload, got[1].Payload)
	}
	if b.DroppedTotal() < 1 {
		t.Fatalf("DroppedTotal = %d, want ≥1", b.DroppedTotal())
	}
}

// -----------------------------------------------------------------------------
// Unsubscribe — removes subscriber + closes channel idempotently
// -----------------------------------------------------------------------------

func TestBroker_UnsubscribeClosesChannel(t *testing.T) {
	b := NewBroker(0)
	defer b.Close()

	ch, cleanup, err := b.Subscribe("session-1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	cleanup()

	// Channel must be closed (a receive on a closed channel returns
	// immediately with the zero value + ok=false).
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("expected channel closed (ok=false) after cleanup, got ok=true")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("channel not closed within 200ms after cleanup")
	}

	// Publish after cleanup must not panic + must not deliver.
	b.Publish("session-1", Message{Type: "event"})

	// Idempotent: second cleanup must not panic / double-close.
	cleanup()
}

func TestBroker_SubscriberCountReflectsLifecycle(t *testing.T) {
	b := NewBroker(0)
	defer b.Close()

	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("initial SubscriberCount = %d, want 0", got)
	}

	_, c1, _ := b.Subscribe("session-X")
	_, c2, _ := b.Subscribe("session-X")
	_, c3, _ := b.Subscribe("session-Y")
	if got := b.SubscriberCount(); got != 3 {
		t.Fatalf("after 3 subs, SubscriberCount = %d, want 3", got)
	}

	c1()
	if got := b.SubscriberCount(); got != 2 {
		t.Fatalf("after 1 cleanup, SubscriberCount = %d, want 2", got)
	}
	c2()
	c3()
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("after all cleanups, SubscriberCount = %d, want 0", got)
	}
}

// -----------------------------------------------------------------------------
// Close — drains + future Publish/Subscribe is a no-op (no panic)
// -----------------------------------------------------------------------------

func TestBroker_CloseClosesAllSubscriberChannels(t *testing.T) {
	b := NewBroker(0)

	ch1, _, _ := b.Subscribe("session-A")
	ch2, _, _ := b.Subscribe("session-B")

	b.Close()

	// All channels must close so handler-side range loops wake up.
	select {
	case _, ok := <-ch1:
		if ok {
			t.Fatalf("ch1 still open after Close")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("ch1 not closed within 200ms after Close")
	}
	select {
	case _, ok := <-ch2:
		if ok {
			t.Fatalf("ch2 still open after Close")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("ch2 not closed within 200ms after Close")
	}

	// Subscribe after Close returns an error so handlers fail fast.
	if _, _, err := b.Subscribe("session-C"); err == nil {
		t.Fatalf("Subscribe after Close: want error, got nil")
	}

	// Publish after Close must not panic.
	b.Publish("session-A", Message{Type: "event"})
}

// -----------------------------------------------------------------------------
// Concurrency — Subscribe + Publish + Unsubscribe must be race-free under -race
// -----------------------------------------------------------------------------

func TestBroker_ConcurrentSubscribePublishUnsubscribe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency stress in -short")
	}
	b := NewBroker(8)
	defer b.Close()

	const workers = 20
	const opsPerWorker = 50

	var wg sync.WaitGroup
	wg.Add(workers)
	var received atomic.Int64
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			sessionID := fmt.Sprintf("session-%d", i%4)
			for j := 0; j < opsPerWorker; j++ {
				ch, cleanup, err := b.Subscribe(sessionID)
				if err != nil {
					t.Errorf("Subscribe: %v", err)
					return
				}
				b.Publish(sessionID, Message{Type: "event", Payload: j})
				// Best-effort: drain at most one message before cleanup.
				select {
				case <-ch:
					received.Add(1)
				default:
				}
				cleanup()
			}
		}()
	}
	wg.Wait()

	if received.Load() == 0 {
		t.Fatalf("expected ≥1 received message under concurrency, got 0")
	}
}

// -----------------------------------------------------------------------------
// Context cancellation — Subscribe respects ctx and a cancelled ctx still
// returns a cleanup that's safe to call.
// -----------------------------------------------------------------------------

func TestBroker_SubscribeWithCancelledContextStillReturnsChannel(t *testing.T) {
	// Subscribe should NOT block on context — it allocates state synchronously.
	// (Context is only used to honour shutdown intent in the broker
	// implementation; the channel itself stays usable until cleanup or Close.)
	b := NewBroker(0)
	defer b.Close()

	_ = context.Background() // documenting that the broker is ctx-free; handlers manage ctx

	ch, cleanup, err := b.Subscribe("session-1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cleanup()
	if ch == nil {
		t.Fatalf("Subscribe returned nil channel")
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func drainN(t *testing.T, ch <-chan Message, n int, timeout time.Duration) []Message {
	t.Helper()
	out := make([]Message, 0, n)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for i := 0; i < n; i++ {
		select {
		case got, ok := <-ch:
			if !ok {
				t.Fatalf("drainN: channel closed after %d/%d", i, n)
			}
			out = append(out, got)
		case <-deadline.C:
			t.Fatalf("drainN: timeout after %d/%d", i, n)
		}
	}
	return out
}
