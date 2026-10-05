// identity_profile_subscriber.go — Pub/Sub subscriber for the Q3 name
// projection (delivery side).
//
// Receives chora.identity.user.profile_updated.v1 from the identity domain and
// upserts the (gcid → display_name) row into chora_delivery.user_directory via
// the directory.UserDirectoryPort. That projection is later stitched onto the
// RLS-scoped course roster so learners render with a real name instead of a
// raw GCID (see internal/adapter/inmem/roster_repo.go).
//
// Two layers of replay safety compose here:
//   - inbox.Process short-circuits an exact-event_id redelivery (fast path).
//   - the port's Upsert is last-writer-wins on updated_at, so an out-of-order
//     DIFFERENT event (older profile edit arriving after a newer one) cannot
//     clobber a fresher name.
//
// Hexagonal: ADAPTER. Depends on the directory domain port + the shared
// idempotent.Store. No domain code imports this file.
package events

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
)

const (
	// TopicIdentityUserProfileUpdated is the inbound identity event this
	// subscriber binds to. JSON on the wire (identity's outbox is JSON); the
	// envelope rides in Pub/Sub attributes, the payload in the message data.
	TopicIdentityUserProfileUpdated = "chora.identity.user.profile_updated.v1"

	// IdentityProfileInboxTTL — dedupe-key retention window. 24h covers the
	// Pub/Sub redelivery window; the LWW guard on Upsert backstops anything
	// beyond it.
	IdentityProfileInboxTTL = 24 * time.Hour
)

// ProfileUpdatedEvent is the decoded identity profile-update fact: the envelope
// trust-anchor fields (EventID, TenantID) plus the payload subject (GCID) and
// its projected attributes (DisplayName, Email, UpdatedAt). The HTTP push
// handler builds this from Pub/Sub attributes + the JSON payload body.
type ProfileUpdatedEvent struct {
	EventID     string
	TenantID    string
	GCID        string
	DisplayName string
	Email       string
	UpdatedAt   time.Time
}

// IdentityProfileSubscriberDeps wires the subscriber.
type IdentityProfileSubscriberDeps struct {
	// Directory — the user-directory projection port (pg.UserDirectoryRepo in
	// production; directory.InMemUserDirectory in dev/tests). Required.
	Directory directory.UserDirectoryPort
	// Inbox — idempotent.Store for replay dedup. Defaults to MemoryStore when
	// nil so dev/unit tests work without a Postgres pool.
	Inbox idempotent.Store
	// TTL — inbox dedup-key retention. Defaults to IdentityProfileInboxTTL.
	TTL time.Duration
}

// IdentityProfileSubscriber processes chora.identity.user.profile_updated.v1.
type IdentityProfileSubscriber struct {
	directory directory.UserDirectoryPort
	inbox     idempotent.Store
	ttl       time.Duration
}

// NewIdentityProfileSubscriber constructs an IdentityProfileSubscriber.
func NewIdentityProfileSubscriber(deps IdentityProfileSubscriberDeps) *IdentityProfileSubscriber {
	inbox := deps.Inbox
	if inbox == nil {
		inbox = idempotent.NewMemoryStore()
	}
	ttl := deps.TTL
	if ttl <= 0 {
		ttl = IdentityProfileInboxTTL
	}
	return &IdentityProfileSubscriber{directory: deps.Directory, inbox: inbox, ttl: ttl}
}

// HandleProfileUpdated validates the event, dedups on event_id, and upserts the
// directory projection. Fail-loud on missing trust-anchor fields (event_id /
// tenant_id) or a missing payload subject (gcid) / updated_at.
func (s *IdentityProfileSubscriber) HandleProfileUpdated(ctx context.Context, ev ProfileUpdatedEvent) error {
	if s == nil {
		return errors.New("identity profile subscriber: nil receiver")
	}
	if s.directory == nil {
		return errors.New("identity profile subscriber: directory port not wired")
	}
	if strings.TrimSpace(ev.EventID) == "" {
		return errors.New("identity profile subscriber: envelope event_id required")
	}
	if strings.TrimSpace(ev.TenantID) == "" {
		return errors.New("identity profile subscriber: envelope tenant_id required")
	}
	if strings.TrimSpace(ev.GCID) == "" {
		return errors.New("identity profile subscriber: payload gcid required")
	}
	if ev.UpdatedAt.IsZero() {
		return errors.New("identity profile subscriber: payload updated_at required")
	}
	key := TopicIdentityUserProfileUpdated + ":" + ev.EventID
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		return s.directory.Upsert(ctx, directory.UserDirectoryEntry{
			GCID:        ev.GCID,
			DisplayName: ev.DisplayName,
			Email:       ev.Email,
			UpdatedAt:   ev.UpdatedAt,
		})
	})
}
