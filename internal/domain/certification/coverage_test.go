// coverage_test.go — closes the remaining statement gaps in certificate.go:
// Verify's nil-certificate guard and strEq's length-mismatch early return.
package certification_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/certification"
)

func TestVerify_NilCertificateRejected(t *testing.T) {
	signer, _ := certification.NewSigner([]byte(hmacKey1))
	if err := signer.Verify(nil, "deadbeef"); !errors.Is(err, certification.ErrInvalidArgument) {
		t.Errorf("nil certificate: got %v want ErrInvalidArgument", err)
	}
}

func TestVerify_ValidCertificate_RoundTrip(t *testing.T) {
	signer, _ := certification.NewSigner([]byte(hmacKey1))
	issued := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cert, err := signer.Issue(tenantA, gcid1, courseA, 88, issued)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Short-signature check: a hex signature shorter than the computed one hits
	// strEq's length-mismatch early return inside Verify's hash comparison is
	// NOT reachable (hash is compared via strEq with equal lengths); instead
	// exercise strEq directly through Issue→Verify by tampering the hash length.
	tampered := *cert
	tampered.Hash = strings.Repeat("0", 32) // half-length hash
	if err := signer.Verify(&tampered, cert.Signature); !errors.Is(err, certification.ErrSignatureMismatch) {
		t.Errorf("short hash: got %v want ErrSignatureMismatch", err)
	}
	if err := signer.Verify(cert, cert.Signature); err != nil {
		t.Errorf("verify: got %v want nil", err)
	}
}
