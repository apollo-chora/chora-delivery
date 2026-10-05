// redis_test.go — unit coverage for the TLS+AUTH config assembly of the
// Memorystore Redis adapter (ADR-168). The live dial is exercised in the
// scale-smoke (miniredis is unavailable offline), but the TLS-config builder is
// pure + must be correct: Memorystore SERVER_AUTHENTICATION presents a cert
// signed by the instance CA that does NOT match the private IP we dial, so we
// verify the chain against the CA + skip the hostname check.
package realtime

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// selfSignedCAPEM returns a throwaway CA cert in PEM form for the test.
func selfSignedCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-redis-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("createcert: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestBuildRedisTLS_NoCA_ReturnsNil(t *testing.T) {
	cfg, err := buildRedisTLS("")
	if err != nil {
		t.Fatalf("empty CA should not error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("empty CA should yield nil *tls.Config, got %#v", cfg)
	}
}

func TestBuildRedisTLS_GarbageCA_FailsLoud(t *testing.T) {
	if _, err := buildRedisTLS("not a pem"); err == nil {
		t.Fatal("garbage CA PEM must fail loud")
	}
}

func TestBuildRedisTLS_ValidCA_VerifiesChainSkipsHostname(t *testing.T) {
	cfg, err := buildRedisTLS(selfSignedCAPEM(t))
	if err != nil {
		t.Fatalf("valid CA: %v", err)
	}
	if cfg == nil {
		t.Fatal("valid CA should yield a *tls.Config")
	}
	if cfg.RootCAs == nil {
		t.Fatal("RootCAs must be populated from the CA PEM")
	}
	// Hostname check is skipped (private IP != cert CN) but the chain is
	// verified by VerifyPeerCertificate — so InsecureSkipVerify must be paired
	// with a non-nil VerifyPeerCertificate.
	if !cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify must be true (hostname skipped for private-IP dial)")
	}
	if cfg.VerifyPeerCertificate == nil {
		t.Error("VerifyPeerCertificate must be set to verify the chain against the CA")
	}
	if cfg.MinVersion < 0x0303 { // tls.VersionTLS12
		t.Error("MinVersion must be >= TLS 1.2")
	}
}

func TestNewRedisTLS_BadCA_Errors(t *testing.T) {
	if _, err := NewRedis(RedisConfig{Addr: "10.0.0.1:6378", CACertPEM: "bad"}); err == nil {
		t.Fatal("NewRedis with bad CA PEM must error")
	}
}

func TestNewRedisTLS_PlaintextNoTLS(t *testing.T) {
	r, err := NewRedis(RedisConfig{Addr: "10.0.0.1:6379"})
	if err != nil {
		t.Fatalf("plaintext NewRedis: %v", err)
	}
	if r == nil {
		t.Fatal("nil client")
	}
	_ = r.Close()
}
