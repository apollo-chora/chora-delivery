package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// compile-time: the client satisfies the exam admission outbound port.
var _ exam.VerificationClaimReader = (*VerificationClient)(nil)

// newStubIdentity spins an httptest server that asserts the request shape then
// replies with the supplied status + JSON body.
func newStubIdentity(t *testing.T, status int, body string, wantGCID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/internal/v1/identity/verification-status" {
			t.Errorf("path = %s, want /internal/v1/identity/verification-status", r.URL.Path)
		}
		if got := r.URL.Query().Get("gcid"); got != wantGCID {
			t.Errorf("gcid = %q, want %q", got, wantGCID)
		}
		// OTLP-everywhere: outbound call MUST carry a W3C traceparent.
		if r.Header.Get("traceparent") == "" {
			t.Error("missing traceparent header on outbound identity call")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestVerificationClient_IsVerified_True(t *testing.T) {
	srv := newStubIdentity(t, http.StatusOK, `{"gcid":"learner-1","verified":true,"status":"verified"}`, "learner-1")
	defer srv.Close()

	c := NewVerificationClient(srv.URL, srv.Client())
	ok, err := c.IsVerified(context.Background(), "tenant-1", "learner-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("IsVerified = false, want true")
	}
}

func TestVerificationClient_IsVerified_False(t *testing.T) {
	srv := newStubIdentity(t, http.StatusOK, `{"gcid":"learner-2","verified":false,"status":"unverified"}`, "learner-2")
	defer srv.Close()

	c := NewVerificationClient(srv.URL, srv.Client())
	ok, err := c.IsVerified(context.Background(), "tenant-1", "learner-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("IsVerified = true, want false")
	}
}

func TestVerificationClient_IdentityError_FailsLoud(t *testing.T) {
	srv := newStubIdentity(t, http.StatusInternalServerError, `{"error":"boom"}`, "learner-3")
	defer srv.Close()

	c := NewVerificationClient(srv.URL, srv.Client())
	ok, err := c.IsVerified(context.Background(), "tenant-1", "learner-3")
	if err == nil {
		t.Fatal("expected error on identity 500, got nil (silent false is forbidden — must surface 502)")
	}
	if ok {
		t.Fatal("IsVerified = true on error, want false")
	}
}

func TestVerificationClient_RouteMissing_FailsLoud(t *testing.T) {
	// 404 = mesh/route misconfig (the internal endpoint is not reachable), NOT
	// an unverified learner — must fail loud, never degrade to false.
	srv := newStubIdentity(t, http.StatusNotFound, `not found`, "learner-4")
	defer srv.Close()

	c := NewVerificationClient(srv.URL, srv.Client())
	_, err := c.IsVerified(context.Background(), "tenant-1", "learner-4")
	if err == nil {
		t.Fatal("expected error on identity 404, got nil")
	}
}

func TestVerificationClient_TransportError_FailsLoud(t *testing.T) {
	srv := newStubIdentity(t, http.StatusOK, `{"verified":true}`, "learner-5")
	base := srv.URL
	srv.Close() // kill the server → dial failure

	c := NewVerificationClient(base, http.DefaultClient)
	_, err := c.IsVerified(context.Background(), "tenant-1", "learner-5")
	if err == nil {
		t.Fatal("expected transport error after server close, got nil")
	}
}

func TestVerificationClient_EmptyGCID_Guards(t *testing.T) {
	c := NewVerificationClient("http://identity.invalid", http.DefaultClient)
	if _, err := c.IsVerified(context.Background(), "tenant-1", "  "); err == nil {
		t.Fatal("expected error for empty gcid, got nil")
	}
}

func TestVerificationClient_EmptyBaseURL_Guards(t *testing.T) {
	c := NewVerificationClient("", http.DefaultClient)
	if _, err := c.IsVerified(context.Background(), "tenant-1", "learner-6"); err == nil {
		t.Fatal("expected error for empty base URL (SVC_IDENTITY_HTTP_URL unset), got nil")
	}
}

func TestVerificationClient_NilHTTPClient_Defaults(t *testing.T) {
	// A nil injected client must default to a working one (production passes
	// its own; this guards the default branch).
	srv := newStubIdentity(t, http.StatusOK, `{"gcid":"learner-7","verified":true,"status":"verified"}`, "learner-7")
	defer srv.Close()

	c := NewVerificationClient(srv.URL, nil)
	ok, err := c.IsVerified(context.Background(), "tenant-1", "learner-7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("IsVerified = false, want true")
	}
}
