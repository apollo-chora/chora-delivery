// broker_extra_test.go — tops up the ws.Broker surfaces not exercised by
// broker_test.go: the explicit Unsubscribe(name) idiom, per-session counts,
// the post-Close error/no-op contracts, publish-time timestamp stamping,
// and the deliver() early-return when a subscriber is already cleaned up.
package ws

import (
	"testing"
	"time"
)

func TestBroker_ExplicitUnsubscribe(t *testing.T) {
	b := NewBroker(0)
	defer b.Close()

	ch1, _, err := b.Subscribe("session-A")
	if err != nil {
		t.Fatalf("Subscribe 1: %v", err)
	}
	ch2, _, err := b.Subscribe("session-A")
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}

	if got := b.SubscriberCountForSession("session-A"); got != 2 {
		t.Fatalf("SubscriberCountForSession = %d, want 2", got)
	}

	// Unsubscribe ch2 via the explicit API — ch1 must be untouched.
	b.Unsubscribe("session-A", ch2)

	if got := b.SubscriberCountForSession("session-A"); got != 1 {
		t.Fatalf("after unsubscribe, SubscriberCountForSession = %d, want 1", got)
	}

	select {
	case _, ok := <-ch2:
		if ok {
			t.Fatal("unsubscribed channel: expected closed (ok=false)")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("unsubscribed channel not closed within 200ms")
	}

	// ch1 still receives events.
	b.Publish("session-A", Message{Type: "event"})
	select {
	case got := <-ch1:
		if got.Type != "event" {
			t.Fatalf("ch1 got Type=%q, want event", got.Type)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ch1 did not receive after sibling unsubscribe")
	}

	// Unknown session / unknown channel are no-ops (idempotent).
	b.Unsubscribe("session-unknown", ch1)
	b.Unsubscribe("session-A", ch1) // ch1 still registered — stays registered here
	b.Unsubscribe("session-A", make(chan Message))
}

func TestBroker_SubscriberCountForSession_Isolation(t *testing.T) {
	b := NewBroker(0)
	defer b.Close()

	if got := b.SubscriberCountForSession("nope"); got != 0 {
		t.Fatalf("unknown session count = %d, want 0", got)
	}

	_, c1, _ := b.Subscribe("s1")
	defer c1()
	_, c2, _ := b.Subscribe("s1")
	defer c2()
	_, c3, _ := b.Subscribe("s2")
	defer c3()

	if got := b.SubscriberCountForSession("s1"); got != 2 {
		t.Fatalf("s1 count = %d, want 2", got)
	}
	if got := b.SubscriberCountForSession("s2"); got != 1 {
		t.Fatalf("s2 count = %d, want 1", got)
	}
}

func TestBroker_PublishStampsZeroTimestamp(t *testing.T) {
	b := NewBroker(0)
	defer b.Close()

	ch, cleanup, err := b.Subscribe("s")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cleanup()

	b.Publish("s", Message{Type: "event"}) // Timestamp left zero

	select {
	case got := <-ch:
		if got.Timestamp.IsZero() {
			t.Fatal("publish must stamp a zero timestamp with the wall clock")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("did not receive message")
	}
}

func TestBroker_AfterCloseContracts(t *testing.T) {
	b := NewBroker(0)
	if _, _, err := b.Subscribe("s"); err != nil {
		t.Fatalf("Subscribe before Close: %v", err)
	}

	b.Close()

	// Subscribe returns the closed error.
	ch, cleanup, err := b.Subscribe("s")
	if err == nil {
		t.Fatal("Subscribe after Close: want error")
	}
	cleanup() // returned cleanup must be safe to call
	_ = ch

	// Publish after Close is a silent no-op (no panic).
	b.Publish("s", Message{Type: "event"})
	// Unsubscribe after Close is a no-op (no panic, no stale channel close).
	b.Unsubscribe("s", ch)

	// Close is idempotent.
	b.Close()
}

func TestBroker_DeliverSkipsCleanedUpSubscriber(t *testing.T) {
	b := NewBroker(0)

	_, cleanup, err := b.Subscribe("s")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	cleanup() // unregister + close channel

	// Publish to a session whose only subscriber is gone — deliver's
	// cleanedUp early-return must skip it without panicking or sending.
	// (A send on the closed channel would panic; a send that raced would
	// be flagged by the race detector.)
	b.Publish("s", Message{Type: "event"})

	b.Close()
}
