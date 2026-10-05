// Package certification owns course-completion certificates with HMAC-SHA-256
// signed verification. Distinct from internal/domain/delivery
// (in-flight agent's catalogue + enrollment scope).
//
// Pattern:
//
//  1. Issue() builds a Certificate (UUIDv7 id, hash, signature) from
//     (tenant, gcid, course, score, issued_at).
//  2. Hash = SHA-256 over a canonical join of the inputs (deterministic).
//  3. Signature = HMAC-SHA-256 over hash, hex-encoded.
//  4. Verify() rebuilds hash + signature from (mutable) fields and rejects
//     mismatch — proves the cert hasn't been tampered with.
//
// Append-only: there is no Update() / Delete() — certificates are
// immutable per .claude/rules/ddd-enforcement.md AtomRevision pattern
// applied to credentials.
//
// Public verification: docs/design briefing calls for /certifications/{id}
// /verify?signature=X (no auth). The Verify method backs that endpoint.
package certification

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	// ErrInvalidArgument signals a guard-clause failure.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrSignatureMismatch is returned by Verify when hash/signature does
	// not match the recomputed value.
	ErrSignatureMismatch = errors.New("signature mismatch")
)

// minHMACKeyLen — same baseline as the attendance package; keeps key
// material consistent across the service. Production keys come from
// Secret Manager (no inline config — see `feedback_no_inline_config`).
const minHMACKeyLen = 32

// -----------------------------------------------------------------------------
// Certificate
// -----------------------------------------------------------------------------

// Certificate is an immutable credential record.
type Certificate struct {
	ID        string
	TenantID  string
	GCID      string
	CourseID  string
	Score     float64
	Hash      string // SHA-256 hex over canonical input
	Signature string // HMAC-SHA-256 hex over Hash
	IssuedAt  time.Time
}

// Signer mints + verifies certificates with a server-side HMAC key.
type Signer struct {
	key []byte
}

// NewSigner constructs a Signer with key validation.
func NewSigner(key []byte) (*Signer, error) {
	if len(key) < minHMACKeyLen {
		return nil, fmt.Errorf("%w: hmac key must be >= %d bytes (got %d)", ErrInvalidArgument, minHMACKeyLen, len(key))
	}
	cp := make([]byte, len(key))
	copy(cp, key)
	return &Signer{key: cp}, nil
}

// Issue builds a Certificate.
//
// Validation:
//   - tenant, gcid, course required
//   - 0 <= score <= 100 (percentile or grade scale).
//
// IssuedAt is taken verbatim from the caller (UTC truncated). The hash is
// deterministic over (gcid, course, score, issued_at_unix_ms); two
// certificates with the same inputs will produce the same hash but
// different IDs (UUIDv7 carries the timestamp uniqueness).
func (s *Signer) Issue(tenantID, gcid, courseID string, score float64, issuedAt time.Time) (*Certificate, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(gcid) == "" {
		return nil, fmt.Errorf("%w: gcid required", ErrInvalidArgument)
	}
	if strings.TrimSpace(courseID) == "" {
		return nil, fmt.Errorf("%w: course_id required", ErrInvalidArgument)
	}
	if score < 0 || score > 100 {
		return nil, fmt.Errorf("%w: score must be 0..100 (got %v)", ErrInvalidArgument, score)
	}
	hash := computeHash(gcid, courseID, score, issuedAt)
	sig := s.sign(hash)
	return &Certificate{
		ID:        NewUUIDv7(),
		TenantID:  tenantID,
		GCID:      gcid,
		CourseID:  courseID,
		Score:     score,
		Hash:      hash,
		Signature: sig,
		IssuedAt:  issuedAt.UTC(),
	}, nil
}

// Verify checks that:
//
//  1. The hash field matches the recomputed canonical hash over (gcid,
//     course, score, issued_at).
//  2. The provided signature matches HMAC-SHA-256(hash, key).
//
// This catches both score-tampering (different recomputed hash) and
// signature-tampering (different HMAC).
func (s *Signer) Verify(c *Certificate, providedSignature string) error {
	if c == nil {
		return fmt.Errorf("%w: nil certificate", ErrInvalidArgument)
	}
	wantHash := computeHash(c.GCID, c.CourseID, c.Score, c.IssuedAt)
	if !strEq(wantHash, c.Hash) {
		return fmt.Errorf("%w: hash mismatch", ErrSignatureMismatch)
	}
	wantSig := s.sign(c.Hash)
	wantBytes, err := hex.DecodeString(wantSig)
	if err != nil {
		return fmt.Errorf("%w: internal sign error", ErrSignatureMismatch)
	}
	gotBytes, err := hex.DecodeString(providedSignature)
	if err != nil || !hmac.Equal(wantBytes, gotBytes) {
		return fmt.Errorf("%w: hmac mismatch", ErrSignatureMismatch)
	}
	return nil
}

// computeHash returns SHA-256 hex over the canonical join of the inputs.
//
// We use a literal '|' separator + null byte sentinels to defeat trivial
// boundary-confusion attacks (e.g. moving "|" across fields).
func computeHash(gcid, courseID string, score float64, issuedAt time.Time) string {
	h := sha256.New()
	h.Write([]byte(gcid))
	h.Write([]byte{0})
	h.Write([]byte(courseID))
	h.Write([]byte{0})
	h.Write([]byte(fmt.Sprintf("%v", score)))
	h.Write([]byte{0})
	h.Write([]byte(fmt.Sprintf("%d", issuedAt.UTC().UnixMilli())))
	return hex.EncodeToString(h.Sum(nil))
}

// sign produces HMAC-SHA-256(hash, key), hex-encoded.
func (s *Signer) sign(hash string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(hash))
	return hex.EncodeToString(mac.Sum(nil))
}

// strEq is a constant-time equality check for hex strings (defense in
// depth — Verify already uses hmac.Equal on the bytes).
func strEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// NewUUIDv7 returns a freshly generated UUIDv7 string per RFC 9562 §5.7.
func NewUUIDv7() string {
	const buflen = 16
	var b [buflen]byte
	now := uint64(time.Now().UnixMilli())
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	if _, err := rand.Read(b[6:]); err != nil {
		for i := 6; i < buflen; i++ {
			b[i] = byte(now >> uint(8*(i-6)))
		}
	}
	b[6] = (b[6] & 0x0F) | 0x70
	b[8] = (b[8] & 0x3F) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
