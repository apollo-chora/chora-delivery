// identity_profile_pubsub_handler.go — Pub/Sub push handler for the Q3 name
// projection (delivery side). Routes chora.identity.user.profile_updated.v1
// deliveries to the events.IdentityProfileSubscriber.
//
// Wire contract (identity's outbox is JSON; the canonical chora-go-common relay
// projects the envelope onto Pub/Sub ATTRIBUTES and puts the payload in the
// message DATA body — see libs/chora-go-common/pubsub envelopeAttributes):
//
//	attributes: { topic, event_id, idempotency_key, tenant_id, gcid,
//	              occurred_at, published_at, traceparent, source_project,
//	              source_service, schema_version }
//	data (base64 JSON): { "gcid", "display_name", "email"?, "updated_at" }
//
// This differs from payments_pubsub_handler.go (proto payload with an embedded
// envelope) because identity emits JSON; the shape mirrors the observability
// familiar-growth JSON handler (envelope-from-attributes). The directory row
// keys on the PAYLOAD gcid (the profile subject), not the attribute gcid (the
// actor).
//
// Per ddd-enforcement: this HTTP handler is a thin transport-only translator;
// the IdentityProfileSubscriber is the inbound business adapter (idempotency +
// directory upsert). OIDC + mesh authz gate the /api/internal/* path.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
)

// IdentityProfilePushDeps wires the handler.
type IdentityProfilePushDeps struct {
	// Subscriber is the typed inbound business adapter. Required.
	Subscriber *events.IdentityProfileSubscriber
	// Verifier is the OIDC token verifier. Use
	// eventpush.NewVerifier(eventpush.VerifierConfig{}) for dev.
	Verifier *eventpush.Verifier
}

// NewIdentityProfilePushHandler returns the http.Handler bound to the
// chora.identity.user.profile_updated.v1 push subscription. Panics on nil
// Subscriber so a mis-wired bootstrap fails loud per feedback_no_stubs_real_wiring.
func NewIdentityProfilePushHandler(deps IdentityProfilePushDeps) http.Handler {
	if deps.Subscriber == nil {
		panic("http: IdentityProfilePushHandler requires Subscriber")
	}
	return eventpush.NewHandler(eventpush.HandlerConfig{
		Verifier: deps.Verifier,
		Dispatch: dispatchIdentityProfilePush(deps.Subscriber),
	})
}

// profileUpdatedPayload is the JSON body identity publishes in msg.Data.
type profileUpdatedPayload struct {
	GCID        string `json:"gcid"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	UpdatedAt   string `json:"updated_at"` // RFC3339 / RFC3339Nano
}

// dispatchIdentityProfilePush decodes one push delivery + invokes the subscriber.
func dispatchIdentityProfilePush(sub *events.IdentityProfileSubscriber) eventpush.DispatchFunc {
	return func(ctx context.Context, msg eventpush.PushMessage) error {
		// Single-topic endpoint: when the relay stamps the `topic` attribute it
		// MUST match; a mismatch is a misroute → fail loud so the broker DLQs.
		if topic := msg.Attributes["topic"]; topic != "" && topic != events.TopicIdentityUserProfileUpdated {
			return fmt.Errorf("identity_profile_push: unexpected topic %q", topic)
		}
		// Envelope trust anchor from attributes (canonical Chora relay projection).
		eventID := strings.TrimSpace(msg.Attributes["event_id"])
		tenantID := strings.TrimSpace(msg.Attributes["tenant_id"])
		if eventID == "" {
			return errors.New("identity_profile_push: missing event_id attribute")
		}
		if tenantID == "" {
			return errors.New("identity_profile_push: missing tenant_id attribute")
		}
		if len(msg.Data) == 0 {
			return errors.New("identity_profile_push: empty payload")
		}
		var p profileUpdatedPayload
		if err := json.Unmarshal(msg.Data, &p); err != nil {
			return fmt.Errorf("identity_profile_push: json.Unmarshal payload: %w", err)
		}
		updatedAt, err := parseProfileUpdatedAt(p.UpdatedAt)
		if err != nil {
			return fmt.Errorf("identity_profile_push: %w", err)
		}
		return sub.HandleProfileUpdated(ctx, events.ProfileUpdatedEvent{
			EventID:     eventID,
			TenantID:    tenantID,
			GCID:        strings.TrimSpace(p.GCID),
			DisplayName: p.DisplayName,
			Email:       p.Email,
			UpdatedAt:   updatedAt,
		})
	}
}

// parseProfileUpdatedAt parses the payload's RFC3339(/Nano) updated_at. Fail
// loud on absence/malformation — updated_at drives the directory's last-writer-
// wins reconciliation, so a bad value is a hard error, not a silent default.
func parseProfileUpdatedAt(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("payload updated_at required")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("payload updated_at %q not RFC3339: %w", s, err)
	}
	return t.UTC(), nil
}
