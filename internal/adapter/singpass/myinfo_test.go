// Package singpass — Singpass MyInfo prefill adapter tests, S6.1.
//
// Coordinates with A-Singpass (S6.3 sister) at merge time. Until A-Singpass
// lands the real NDI client, this stub provides a deterministic Singpass
// session + prefill response; the HTTP layer consumes the same MyInfoClient
// interface so the swap-in is a wiring change.
package singpass_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/singpass"
)

func TestStubClient_RetrieveMyInfo_HappyPath(t *testing.T) {
	t.Parallel()
	c := singpass.NewStubClient(singpass.Config{RedirectURL: "https://chora.site/callback"})
	res, err := c.RetrieveMyInfo(context.Background(), "valid-prefill-token-1")
	if err != nil {
		t.Fatalf("RetrieveMyInfo: %v", err)
	}
	if res.SessionID == "" {
		t.Fatalf("expected SessionID")
	}
	if res.Profile.LegalName == "" {
		t.Fatalf("expected LegalName from MyInfo prefill")
	}
	if res.Profile.NRICLast4 == "" {
		t.Fatalf("expected NRICLast4")
	}
	if !res.Verified {
		t.Fatalf("MyInfo response should be Verified=true")
	}
}

func TestStubClient_RetrieveMyInfo_RejectsBlankToken(t *testing.T) {
	t.Parallel()
	c := singpass.NewStubClient(singpass.Config{RedirectURL: "https://chora.site/callback"})
	_, err := c.RetrieveMyInfo(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error for blank token")
	}
}

func TestStubClient_RetrieveMyInfo_NoRedirectURL(t *testing.T) {
	t.Parallel()
	c := singpass.NewStubClient(singpass.Config{})
	_, err := c.RetrieveMyInfo(context.Background(), "tok")
	if err == nil {
		t.Fatalf("expected error when SINGPASS_REDIRECT_URL not configured")
	}
}

func TestStubClient_Deterministic_SameTokenSameSession(t *testing.T) {
	t.Parallel()
	c := singpass.NewStubClient(singpass.Config{RedirectURL: "https://chora.site/callback"})
	a, _ := c.RetrieveMyInfo(context.Background(), "tok-determ")
	b, _ := c.RetrieveMyInfo(context.Background(), "tok-determ")
	if a.SessionID != b.SessionID {
		t.Fatalf("stub must be deterministic on token: %q vs %q", a.SessionID, b.SessionID)
	}
}

func TestStubClient_DifferentToken_DifferentSession(t *testing.T) {
	t.Parallel()
	c := singpass.NewStubClient(singpass.Config{RedirectURL: "https://chora.site/callback"})
	a, _ := c.RetrieveMyInfo(context.Background(), "tok-a")
	b, _ := c.RetrieveMyInfo(context.Background(), "tok-b")
	if a.SessionID == b.SessionID {
		t.Fatalf("different tokens must yield different sessions")
	}
}
