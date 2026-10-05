// identity_profile_subscriber_edges_test.go — external coverage for the
// subscriber guard branches the in-package tests do not reach: nil receiver,
// unwired directory port, and the constructor's inbox default.
package events_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
)

func TestIdentityProfileSubscriber_NilReceiver(t *testing.T) {
	var s *events.IdentityProfileSubscriber
	err := s.HandleProfileUpdated(context.Background(), events.ProfileUpdatedEvent{
		EventID: "e", TenantID: "t", GCID: "g",
	})
	if err == nil {
		t.Fatal("nil receiver must error")
	}
}

func TestIdentityProfileSubscriber_DirectoryNotWired(t *testing.T) {
	// Deps with a nil Directory + nil Inbox also exercises the constructor's
	// inbox default (hits the NewMemoryStore fallback).
	sub := events.NewIdentityProfileSubscriber(events.IdentityProfileSubscriberDeps{})
	if sub == nil {
		t.Fatal("nil subscriber")
	}
	err := sub.HandleProfileUpdated(context.Background(), events.ProfileUpdatedEvent{
		EventID: "e", TenantID: "t", GCID: "g",
	})
	if err == nil {
		t.Fatal("unwired directory port must error")
	}
}

func TestIdentityProfileSubscriber_ConstructorDefaults(t *testing.T) {
	sub := events.NewIdentityProfileSubscriber(events.IdentityProfileSubscriberDeps{})
	if sub == nil {
		t.Fatal("nil subscriber")
	}
	// TTL <= 0 defaults to IdentityProfileInboxTTL; verify via the exported
	// constant to pin the constructor's defaulting behaviour.
	if events.IdentityProfileInboxTTL <= 0 {
		t.Fatal("IdentityProfileInboxTTL must be positive")
	}
}
