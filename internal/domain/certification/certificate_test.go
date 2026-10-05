// Package certification owns issuance + public verification of credentials.
// Distinct from internal/domain/delivery (in-flight agent's catalogue +
// enrollment scope) — this package adds course-completion certificates with
// HMAC-signed verification URLs.
package certification_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/certification"
)

const (
	tenantA  = "01970000-0000-7000-8000-000000000001"
	courseA  = "01970000-0000-7000-7000-000000000001"
	gcid1    = "01970000-0000-7000-9000-000000000001"
	gcid2    = "01970000-0000-7000-9000-000000000002"
	hmacKey1 = "cert-signing-key-32-bytes-or-more-aaa"
	hmacKey2 = "cert-different-signing-key-32-bytes-zz"
)

// -----------------------------------------------------------------------------
// Issue + IssuedAt + score
// -----------------------------------------------------------------------------

func TestIssue_TableDriven(t *testing.T) {
	signer, err := certification.NewSigner([]byte(hmacKey1))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	issued := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		tenant  string
		gcid    string
		course  string
		score   float64
		wantErr error
	}{
		{"happy 100", tenantA, gcid1, courseA, 100, nil},
		{"happy 80.5", tenantA, gcid1, courseA, 80.5, nil},
		{"happy 0", tenantA, gcid1, courseA, 0, nil},
		{"missing tenant", "", gcid1, courseA, 90, certification.ErrInvalidArgument},
		{"missing gcid", tenantA, "", courseA, 90, certification.ErrInvalidArgument},
		{"missing course", tenantA, gcid1, "", 90, certification.ErrInvalidArgument},
		{"score below 0", tenantA, gcid1, courseA, -1, certification.ErrInvalidArgument},
		{"score above 100", tenantA, gcid1, courseA, 100.1, certification.ErrInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cert, err := signer.Issue(c.tenant, c.gcid, c.course, c.score, issued)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("got %v want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if cert == nil || cert.ID == "" {
				t.Fatalf("nil cert or empty id")
			}
			if cert.Score != c.score {
				t.Errorf("score: got %v want %v", cert.Score, c.score)
			}
			if !cert.IssuedAt.Equal(issued) {
				t.Errorf("issued_at: got %v want %v", cert.IssuedAt, issued)
			}
			if cert.Hash == "" {
				t.Errorf("missing hash")
			}
			if cert.Signature == "" {
				t.Errorf("missing signature")
			}
			// Hash and signature should be hex-encoded; sanity-check length.
			if len(cert.Hash) != 64 { // SHA-256 hex
				t.Errorf("hash length: got %d want 64", len(cert.Hash))
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Verify — happy + tamper detection + cross-key rejection
// -----------------------------------------------------------------------------

func TestVerify_Happy(t *testing.T) {
	signer, _ := certification.NewSigner([]byte(hmacKey1))
	cert, err := signer.Issue(tenantA, gcid1, courseA, 92.5, time.Now().UTC())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := signer.Verify(cert, cert.Signature); err != nil {
		t.Errorf("verify: got %v want nil", err)
	}
}

func TestVerify_TamperedScoreRejected(t *testing.T) {
	signer, _ := certification.NewSigner([]byte(hmacKey1))
	cert, _ := signer.Issue(tenantA, gcid1, courseA, 92.5, time.Now().UTC())
	tampered := *cert
	tampered.Score = 100.0 // attacker bumps score
	if err := signer.Verify(&tampered, cert.Signature); !errors.Is(err, certification.ErrSignatureMismatch) {
		t.Errorf("score-tamper: got %v want ErrSignatureMismatch", err)
	}
}

func TestVerify_TamperedHashRejected(t *testing.T) {
	signer, _ := certification.NewSigner([]byte(hmacKey1))
	cert, _ := signer.Issue(tenantA, gcid1, courseA, 92.5, time.Now().UTC())
	tampered := *cert
	tampered.Hash = strings.Repeat("0", 64)
	if err := signer.Verify(&tampered, cert.Signature); !errors.Is(err, certification.ErrSignatureMismatch) {
		t.Errorf("hash-tamper: got %v want ErrSignatureMismatch", err)
	}
}

func TestVerify_DifferentKeyRejected(t *testing.T) {
	signer1, _ := certification.NewSigner([]byte(hmacKey1))
	signer2, _ := certification.NewSigner([]byte(hmacKey2))
	cert, _ := signer1.Issue(tenantA, gcid1, courseA, 92.5, time.Now().UTC())
	if err := signer2.Verify(cert, cert.Signature); !errors.Is(err, certification.ErrSignatureMismatch) {
		t.Errorf("cross-key: got %v want ErrSignatureMismatch", err)
	}
}

func TestVerify_BadSignatureFormatRejected(t *testing.T) {
	signer, _ := certification.NewSigner([]byte(hmacKey1))
	cert, _ := signer.Issue(tenantA, gcid1, courseA, 92.5, time.Now().UTC())
	if err := signer.Verify(cert, "not-hex"); !errors.Is(err, certification.ErrSignatureMismatch) {
		t.Errorf("non-hex signature: got %v want ErrSignatureMismatch", err)
	}
}

func TestNewSigner_KeyValidation(t *testing.T) {
	if _, err := certification.NewSigner(nil); !errors.Is(err, certification.ErrInvalidArgument) {
		t.Errorf("nil key: got %v want ErrInvalidArgument", err)
	}
	if _, err := certification.NewSigner([]byte("short")); !errors.Is(err, certification.ErrInvalidArgument) {
		t.Errorf("short key: got %v want ErrInvalidArgument", err)
	}
}

// -----------------------------------------------------------------------------
// Hash determinism — same inputs always produce the same hash.
// -----------------------------------------------------------------------------

func TestHash_Deterministic(t *testing.T) {
	signer, _ := certification.NewSigner([]byte(hmacKey1))
	issued := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	c1, _ := signer.Issue(tenantA, gcid1, courseA, 90, issued)
	c2, _ := signer.Issue(tenantA, gcid1, courseA, 90, issued)
	// IDs differ (UUIDv7), but hash is deterministic over (gcid, course, score, issued_at).
	if c1.Hash != c2.Hash {
		t.Errorf("hash not deterministic: %s vs %s", c1.Hash, c2.Hash)
	}
	c3, _ := signer.Issue(tenantA, gcid2, courseA, 90, issued)
	if c1.Hash == c3.Hash {
		t.Errorf("hash collision across gcids")
	}
}
