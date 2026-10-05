// identity_profile_pubsub_handler_test.go — Q3 name-projection push-handler
// tests (delivery side). Verifies the JSON push delivery is decoded (envelope
// from Pub/Sub attributes, payload from the message data body), dispatched to
// the IdentityProfileSubscriber, and upserted into the directory — plus
// idempotent replay, OIDC verify-failure rejection, and fail-loud decode paths.
package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
)

const (
	ipTenant  = "01970000-0000-7000-8000-0000000000d1"
	ipActor   = "01970000-0000-7000-9000-0000000000a1"
	ipSubject = "01970000-0000-7000-9000-0000000000b2"
)

func newIdentityProfilePushTestServer(t *testing.T) (http.Handler, *directory.InMemUserDirectory) {
	t.Helper()
	dir := directory.NewInMemUserDirectory()
	sub := events.NewIdentityProfileSubscriber(events.IdentityProfileSubscriberDeps{
		Directory: dir,
		Inbox:     idempotent.NewMemoryStore(),
	})
	h := httpadapter.NewIdentityProfilePushHandler(httpadapter.IdentityProfilePushDeps{
		Subscriber: sub,
		Verifier:   eventpush.NewVerifier(eventpush.VerifierConfig{}), // no-op (dev)
	})
	return h, dir
}

func TestIdentityProfilePushHandler_UpsertsDirectory(t *testing.T) {
	h, dir := newIdentityProfilePushTestServer(t)
	body := buildIdentityProfilePushBody(t,
		identityProfilePushAttrs(events.TopicIdentityUserProfileUpdated, "evt-ip-1", ipTenant, ipSubject),
		identityProfilePayload(ipSubject, "Alice Tan", "alice@x.com", "2026-07-08T10:00:00Z"))
	rec := dispatchIdentityProfilePush(t, h, body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("dispatch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	names, _ := dir.LookupNames(context.Background(), []string{ipSubject})
	if names[ipSubject] != "Alice Tan" {
		t.Errorf("directory[%s] = %q, want Alice Tan", ipSubject, names[ipSubject])
	}
}

// The directory row keys on the PAYLOAD gcid (the profile subject), not the
// envelope/attribute gcid (the actor) — an admin editing another learner's
// profile must project the learner's name, not the admin's.
func TestIdentityProfilePushHandler_KeysOnPayloadSubject(t *testing.T) {
	h, dir := newIdentityProfilePushTestServer(t)
	attrs := identityProfilePushAttrs(events.TopicIdentityUserProfileUpdated, "evt-ip-subj", ipTenant, ipActor)
	body := buildIdentityProfilePushBody(t, attrs,
		identityProfilePayload(ipSubject, "Subject Name", "", "2026-07-08T10:00:00Z"))
	rec := dispatchIdentityProfilePush(t, h, body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("dispatch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	names, _ := dir.LookupNames(context.Background(), []string{ipSubject, ipActor})
	if names[ipSubject] != "Subject Name" {
		t.Errorf("subject not projected: %v", names)
	}
	if _, ok := names[ipActor]; ok {
		t.Errorf("actor gcid should NOT be projected: %v", names)
	}
}

func TestIdentityProfilePushHandler_IdempotentReplay(t *testing.T) {
	h, dir := newIdentityProfilePushTestServer(t)
	first := dispatchIdentityProfilePush(t, h, buildIdentityProfilePushBody(t,
		identityProfilePushAttrs(events.TopicIdentityUserProfileUpdated, "evt-ip-replay", ipTenant, ipSubject),
		identityProfilePayload(ipSubject, "Alice", "", "2026-07-08T10:00:00Z")))
	if first.Code < 200 || first.Code >= 300 {
		t.Fatalf("first: status=%d", first.Code)
	}
	// Replay the SAME event_id with a different name — must be short-circuited
	// by the inbox, leaving the first value intact.
	second := dispatchIdentityProfilePush(t, h, buildIdentityProfilePushBody(t,
		identityProfilePushAttrs(events.TopicIdentityUserProfileUpdated, "evt-ip-replay", ipTenant, ipSubject),
		identityProfilePayload(ipSubject, "Bob", "", "2026-07-08T11:00:00Z")))
	if second.Code < 200 || second.Code >= 300 {
		t.Fatalf("replay: status=%d", second.Code)
	}
	names, _ := dir.LookupNames(context.Background(), []string{ipSubject})
	if names[ipSubject] != "Alice" {
		t.Errorf("idempotent replay changed the projection; got %q, want Alice", names[ipSubject])
	}
}

func TestIdentityProfilePushHandler_VerifyFailure_Rejects(t *testing.T) {
	dir := directory.NewInMemUserDirectory()
	sub := events.NewIdentityProfileSubscriber(events.IdentityProfileSubscriberDeps{Directory: dir})
	// Enabled verifier (audience set) with a stub validator — a request with no
	// Authorization header must be rejected 401 before dispatch.
	verifier := eventpush.NewVerifier(eventpush.VerifierConfig{
		Audience: "https://chora-delivery.example/api/internal/pubsub/identity-profile-inbox",
		ValidateToken: func(_ context.Context, _, _ string) (eventpush.TokenClaims, error) {
			return eventpush.TokenClaims{}, errors.New("stub: never called without a bearer")
		},
	})
	h := httpadapter.NewIdentityProfilePushHandler(httpadapter.IdentityProfilePushDeps{Subscriber: sub, Verifier: verifier})
	body := buildIdentityProfilePushBody(t,
		identityProfilePushAttrs(events.TopicIdentityUserProfileUpdated, "evt-ip-401", ipTenant, ipSubject),
		identityProfilePayload(ipSubject, "Alice", "", "2026-07-08T10:00:00Z"))
	rec := dispatchIdentityProfilePush(t, h, body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing bearer on enabled verifier; got %d", rec.Code)
	}
	if names, _ := dir.LookupNames(context.Background(), []string{ipSubject}); len(names) != 0 {
		t.Errorf("rejected request must not upsert; got %v", names)
	}
}

func TestIdentityProfilePushHandler_MissingEventID_FailsLoud(t *testing.T) {
	h, dir := newIdentityProfilePushTestServer(t)
	attrs := identityProfilePushAttrs(events.TopicIdentityUserProfileUpdated, "", ipTenant, ipSubject)
	delete(attrs, "event_id")
	rec := dispatchIdentityProfilePush(t, h, buildIdentityProfilePushBody(t, attrs,
		identityProfilePayload(ipSubject, "Alice", "", "2026-07-08T10:00:00Z")))
	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("expected non-2xx for missing event_id; got %d", rec.Code)
	}
	if names, _ := dir.LookupNames(context.Background(), []string{ipSubject}); len(names) != 0 {
		t.Errorf("no upsert expected on decode failure; got %v", names)
	}
}

func TestIdentityProfilePushHandler_BadUpdatedAt_FailsLoud(t *testing.T) {
	h, _ := newIdentityProfilePushTestServer(t)
	rec := dispatchIdentityProfilePush(t, h, buildIdentityProfilePushBody(t,
		identityProfilePushAttrs(events.TopicIdentityUserProfileUpdated, "evt-ip-baddate", ipTenant, ipSubject),
		identityProfilePayload(ipSubject, "Alice", "", "not-a-timestamp")))
	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("expected non-2xx for unparseable updated_at; got %d", rec.Code)
	}
}

func TestIdentityProfilePushHandler_UnexpectedTopic_FailsLoud(t *testing.T) {
	h, _ := newIdentityProfilePushTestServer(t)
	rec := dispatchIdentityProfilePush(t, h, buildIdentityProfilePushBody(t,
		identityProfilePushAttrs("chora.identity.user.bogus.v1", "evt-ip-bogus", ipTenant, ipSubject),
		identityProfilePayload(ipSubject, "Alice", "", "2026-07-08T10:00:00Z")))
	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("expected non-2xx for mismatched topic attribute; got %d", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func identityProfilePushAttrs(topic, eventID, tenantID, gcid string) map[string]string {
	return map[string]string{
		"topic":           topic,
		"event_id":        eventID,
		"idempotency_key": eventID,
		"tenant_id":       tenantID,
		"gcid":            gcid,
		"source_project":  "chora-489812",
		"source_service":  "chora-identity",
	}
}

func identityProfilePayload(gcid, displayName, email, updatedAt string) []byte {
	m := map[string]any{"gcid": gcid, "display_name": displayName, "updated_at": updatedAt}
	if email != "" {
		m["email"] = email
	}
	b, _ := json.Marshal(m)
	return b
}

func buildIdentityProfilePushBody(t *testing.T, attrs map[string]string, payload []byte) string {
	t.Helper()
	type msg struct {
		Data        string            `json:"data"`
		MessageID   string            `json:"messageId"`
		PublishTime string            `json:"publishTime"`
		Attributes  map[string]string `json:"attributes,omitempty"`
	}
	type env struct {
		Message      msg    `json:"message"`
		Subscription string `json:"subscription"`
	}
	e := env{
		Message: msg{
			Data:        base64.StdEncoding.EncodeToString(payload),
			MessageID:   fmt.Sprintf("mid-%d", time.Now().UnixNano()),
			PublishTime: "2026-07-08T12:00:00Z",
			Attributes:  attrs,
		},
		Subscription: "projects/chora-489812/subscriptions/chora-delivery-identity-profile-inbox",
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	return string(b)
}

func dispatchIdentityProfilePush(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/internal/pubsub/identity-profile-inbox", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
