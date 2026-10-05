// identity_profile_subscriber_test.go — unit tests for the Q3 name-projection
// subscriber (delivery side). Covers Upsert-on-handle, idempotent replay (same
// event_id → one upsert), and envelope/payload validation (fail loud).
package events

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
)

// recordingDirectory wraps InMemUserDirectory and counts Upsert calls so a test
// can prove idempotent replay collapses to a single upsert.
type recordingDirectory struct {
	*directory.InMemUserDirectory
	upserts int
}

func newRecordingDirectory() *recordingDirectory {
	return &recordingDirectory{InMemUserDirectory: directory.NewInMemUserDirectory()}
}

func (d *recordingDirectory) Upsert(ctx context.Context, e directory.UserDirectoryEntry) error {
	d.upserts++
	return d.InMemUserDirectory.Upsert(ctx, e)
}

func TestIdentityProfileSubscriber_HandleProfileUpdated_Upserts(t *testing.T) {
	t.Parallel()
	dir := newRecordingDirectory()
	sub := NewIdentityProfileSubscriber(IdentityProfileSubscriberDeps{
		Directory: dir, Inbox: idempotent.NewMemoryStore(),
	})
	ev := ProfileUpdatedEvent{
		EventID: "evt-1", TenantID: "t1", GCID: "g1",
		DisplayName: "Alice", Email: "a@x.com", UpdatedAt: time.Now().UTC(),
	}
	if err := sub.HandleProfileUpdated(context.Background(), ev); err != nil {
		t.Fatalf("handle: %v", err)
	}
	names, _ := dir.LookupNames(context.Background(), []string{"g1"})
	if names["g1"] != "Alice" {
		t.Errorf("g1 display_name = %q, want Alice", names["g1"])
	}
}

func TestIdentityProfileSubscriber_IdempotentReplay(t *testing.T) {
	t.Parallel()
	dir := newRecordingDirectory()
	sub := NewIdentityProfileSubscriber(IdentityProfileSubscriberDeps{
		Directory: dir, Inbox: idempotent.NewMemoryStore(),
	})
	ev := ProfileUpdatedEvent{
		EventID: "evt-replay", TenantID: "t1", GCID: "g1",
		DisplayName: "Alice", UpdatedAt: time.Now().UTC(),
	}
	if err := sub.HandleProfileUpdated(context.Background(), ev); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := sub.HandleProfileUpdated(context.Background(), ev); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if dir.upserts != 1 {
		t.Errorf("idempotent replay: upserts = %d, want 1", dir.upserts)
	}
}

func TestIdentityProfileSubscriber_Validation(t *testing.T) {
	t.Parallel()
	ts := time.Now().UTC()
	cases := map[string]ProfileUpdatedEvent{
		"missing event_id":   {TenantID: "t", GCID: "g", DisplayName: "n", UpdatedAt: ts},
		"missing tenant_id":  {EventID: "e", GCID: "g", DisplayName: "n", UpdatedAt: ts},
		"missing gcid":       {EventID: "e", TenantID: "t", DisplayName: "n", UpdatedAt: ts},
		"missing updated_at": {EventID: "e", TenantID: "t", GCID: "g", DisplayName: "n"},
	}
	for name, ev := range cases {
		dir := newRecordingDirectory()
		sub := NewIdentityProfileSubscriber(IdentityProfileSubscriberDeps{
			Directory: dir, Inbox: idempotent.NewMemoryStore(),
		})
		if err := sub.HandleProfileUpdated(context.Background(), ev); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
		if dir.upserts != 0 {
			t.Errorf("%s: expected no upsert on validation failure; got %d", name, dir.upserts)
		}
	}
}
