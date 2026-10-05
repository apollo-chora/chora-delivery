// Package singpass is the Singpass MyInfo prefill adapter for chora-delivery.
//
// Per docs/design/ux_course_application.md A-CRS-3 §Section 0:
//
//	Singpass MyInfo retrieve auto-fills Sections 1, 2, 3 of the application
//	form. The adapter makes the call to Singpass's NDI sandbox / production
//	endpoint and returns the verified profile. The application stores the
//	session_id for audit trail.
//
// Coordinates with A-Singpass (S6.3 sister): the real NDI client lives in
// chora-identity (per gap-action-list-2026-05-09 §CHO-31 closed). This
// adapter consumes A-Singpass's eventual library at merge — for the
// interim, the stub provides a deterministic prefill response.
//
// Per .claude/skills/secrets-and-env: SINGPASS_CLIENT_ID / SINGPASS_PRIVATE_KEY /
// SINGPASS_REDIRECT_URL come from Secret Manager.
//
// Hexagonal: ADAPTER. Implements MyInfoClient.
package singpass

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Config holds the wiring values supplied by cmd/server.
type Config struct {
	// RedirectURL is SINGPASS_REDIRECT_URL — required.
	RedirectURL string

	// ClientID is SINGPASS_CLIENT_ID — required for the real client; ignored
	// by the stub.
	ClientID string

	// PrivateKey is SINGPASS_PRIVATE_KEY (PEM-encoded) — required for the
	// real client; ignored by the stub.
	PrivateKey string

	// SandboxMode toggles the NDI sandbox endpoint vs production.
	SandboxMode bool
}

// Profile is the MyInfo-derived profile fields that prefill the application
// form (per A-CRS-3 §Section 1 form fields). Stub returns canned values;
// real adapter pulls from Singpass.
type Profile struct {
	NRICLast4         string
	LegalName         string
	PreferredName     string
	DateOfBirth       time.Time
	Gender            string
	Nationality       string
	ResidentialStreet string
	PostalCode        string
	EmploymentStatus  string
}

// RetrieveResult is the return from RetrieveMyInfo.
type RetrieveResult struct {
	SessionID string  // audit trail (stored on application aggregate)
	Profile   Profile // form-prefill payload
	Verified  bool    // true when Singpass attests the profile
}

// MyInfoClient is the port consumed by the HTTP layer.
//
// Production swap-in is the A-Singpass library (S6.3); stub is for tests
// + the interim until A-Singpass lands.
type MyInfoClient interface {
	RetrieveMyInfo(ctx context.Context, prefillToken string) (*RetrieveResult, error)
}

// -----------------------------------------------------------------------------
// StubClient — deterministic mock
// -----------------------------------------------------------------------------

// StubClient returns a canned MyInfo response keyed off prefillToken.
type StubClient struct {
	cfg Config
}

// NewStubClient constructs a StubClient.
func NewStubClient(cfg Config) *StubClient { return &StubClient{cfg: cfg} }

// RetrieveMyInfo returns a deterministic MyInfo response. Same token →
// same SessionID + Profile.
func (c *StubClient) RetrieveMyInfo(_ context.Context, prefillToken string) (*RetrieveResult, error) {
	if strings.TrimSpace(c.cfg.RedirectURL) == "" {
		return nil, errors.New("singpass: SINGPASS_REDIRECT_URL not configured")
	}
	if strings.TrimSpace(prefillToken) == "" {
		return nil, errors.New("singpass: prefill_token required")
	}

	sum := sha256.Sum256([]byte(prefillToken))
	sid := "singpass-stub-session-" + hex.EncodeToString(sum[:6])

	return &RetrieveResult{
		SessionID: sid,
		Profile: Profile{
			NRICLast4:         "789A",
			LegalName:         "Phyllis Tan Wei Ling",
			PreferredName:     "Phyllis",
			DateOfBirth:       time.Date(1990, 8, 15, 0, 0, 0, 0, time.UTC),
			Gender:            "F",
			Nationality:       "SG",
			ResidentialStreet: "123 Bras Basah Rd",
			PostalCode:        "188958",
			EmploymentStatus:  "EMPLOYED",
		},
		Verified: true,
	}, nil
}

// Compile-time check.
var _ MyInfoClient = (*StubClient)(nil)
