// redis_tls_ext_test.go — drives EVERY branch of buildRedisTLS (ADR-168
// Memorystore SERVER_AUTHENTICATION config), including the VerifyPeerCertificate
// closure that redis_test.go stops short of: a leaf signed by the instance CA
// verifies; a leaf signed by a FOREIGN CA fails; a garbage raw cert fails to
// parse; an empty chain is refused; and a two-cert chain (leaf + intermediate)
// exercises the Intermediates pool loop.
package realtime

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

type testCA struct {
	pem  string
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// newTestCA issues a throwaway CA (cert + key held in the struct so tests can
// sign child certificates with the same authority).
func newTestCA(t *testing.T, cn string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	return testCA{
		pem:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		cert: cert,
		key:  key,
	}
}

// newLeafDER issues a leaf certificate signed by the given CA and returns its
// raw DER for feeding back into tls.Config.VerifyPeerCertificate.
func newLeafDER(t *testing.T, ca testCA, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("issue leaf: %v", err)
	}
	return der
}

func TestBuildRedisTLS_VerifyPeerCertificate_ValidLeaf(t *testing.T) {
	ca := newTestCA(t, "redis-instance-ca")
	cfg, err := buildRedisTLS(ca.pem)
	if err != nil {
		t.Fatalf("buildRedisTLS: %v", err)
	}
	if cfg == nil || cfg.VerifyPeerCertificate == nil {
		t.Fatal("expected a VerifyPeerCertificate closure")
	}
	if err := cfg.VerifyPeerCertificate([][]byte{newLeafDER(t, ca, "redis-1")}, nil); err != nil {
		t.Fatalf("a leaf signed by the configured CA must verify: %v", err)
	}
}

func TestBuildRedisTLS_VerifyPeerCertificate_LeafAndIntermediate(t *testing.T) {
	// Chain: leaf <- intermediate <- CA. The intermediate lands in the
	// Intermediates pool (certs[1:] loop) and the whole chain must verify.
	ca := newTestCA(t, "redis-instance-ca")
	interKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("inter genkey: %v", err)
	}
	interTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(42),
		Subject:               pkix.Name{CommonName: "redis-intermediate"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	interDER, err := x509.CreateCertificate(rand.Reader, interTmpl, ca.cert, &interKey.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("issue intermediate: %v", err)
	}
	interCert, err := x509.ParseCertificate(interDER)
	if err != nil {
		t.Fatalf("parse intermediate: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf genkey: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(43),
		Subject:      pkix.Name{CommonName: "redis-1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, interCert, &leafKey.PublicKey, interKey)
	if err != nil {
		t.Fatalf("issue leaf: %v", err)
	}

	cfg, err := buildRedisTLS(ca.pem)
	if err != nil {
		t.Fatalf("buildRedisTLS: %v", err)
	}
	if err := cfg.VerifyPeerCertificate([][]byte{leafDER, interDER}, nil); err != nil {
		t.Fatalf("leaf+intermediate chain against the CA must verify: %v", err)
	}
}

func TestBuildRedisTLS_VerifyPeerCertificate_ForeignCARejected(t *testing.T) {
	ca := newTestCA(t, "redis-instance-ca")
	foreign := newTestCA(t, "other-tenant-ca")
	cfg, err := buildRedisTLS(ca.pem)
	if err != nil {
		t.Fatalf("buildRedisTLS: %v", err)
	}
	if err := cfg.VerifyPeerCertificate([][]byte{newLeafDER(t, foreign, "impostor")}, nil); err == nil {
		t.Fatal("a leaf signed by a foreign CA must be rejected")
	}
}

func TestBuildRedisTLS_VerifyPeerCertificate_UnparsableCert(t *testing.T) {
	ca := newTestCA(t, "redis-instance-ca")
	cfg, err := buildRedisTLS(ca.pem)
	if err != nil {
		t.Fatalf("buildRedisTLS: %v", err)
	}
	if err := cfg.VerifyPeerCertificate([][]byte{[]byte("not a cert at all")}, nil); err == nil {
		t.Fatal("an unparsable raw cert must fail loud")
	}
}

func TestBuildRedisTLS_VerifyPeerCertificate_EmptyChainRefused(t *testing.T) {
	ca := newTestCA(t, "redis-instance-ca")
	cfg, err := buildRedisTLS(ca.pem)
	if err != nil {
		t.Fatalf("buildRedisTLS: %v", err)
	}
	if err := cfg.VerifyPeerCertificate(nil, nil); err == nil {
		t.Fatal("an empty certificate chain must be refused")
	}
}

func TestBuildRedisTLS_BlankCAStringIsNilConfig(t *testing.T) {
	cfg, err := buildRedisTLS("   \n\t ")
	if err != nil {
		t.Fatalf("whitespace-only CA must be treated as unset: %v", err)
	}
	if cfg != nil {
		t.Fatalf("whitespace-only CA must yield nil *tls.Config, got %#v", cfg)
	}
}

func TestBuildRedisTLS_ConfigFields(t *testing.T) {
	ca := newTestCA(t, "redis-instance-ca")
	cfg, err := buildRedisTLS(ca.pem)
	if err != nil {
		t.Fatalf("buildRedisTLS: %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion: want TLS12, got 0x%04x", cfg.MinVersion)
	}
	if cfg.RootCAs == nil {
		t.Error("RootCAs must be populated")
	}
	if !cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify must be true (chain-verified, hostname skipped)")
	}
}
