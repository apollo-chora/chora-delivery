// Package attendance owns QR-token-based + manual attendance for live
// classes. QR tokens are 30-second time-limited (per docs/design/
// ux_trainer_training_administration.md edge-case row + ADR-087 attendance
// design), HMAC-signed with a server-side key, and tied to (tenant, class).
//
// Two flows:
//
//  1. Instructor-side: rotating QR token issued every 30 seconds. The HTTP
//     handler calls Signer.Issue(tenant, class, now) and projects the token
//     as a QR.
//  2. Learner-side: the learner's tablet POSTs /attendance:scan with the
//     scanned token. Validate() checks signature + TTL, then Log.Record()
//     idempotently records present.
//
// Idempotency: Log.Record() is keyed on (tenant, class, gcid). A second
// scan returns the same record (created=false). This protects against
// double-scan from ChromeDevTools refresh / user double-tap.
package attendance

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	// ErrInvalidArgument signals a guard-clause failure.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrTokenExpired is returned by Validate if the token's issued_at + TTL
	// is strictly before the validation time.
	ErrTokenExpired = errors.New("token expired")
	// ErrTokenInvalid is returned for tampered / malformed / wrong-key tokens.
	ErrTokenInvalid = errors.New("token invalid")
)

// -----------------------------------------------------------------------------
// Constants
// -----------------------------------------------------------------------------

// TokenTTL is the rotation window per docs/design/
// ux_trainer_training_administration.md ("QR tokens are time-limited
// (30-second refresh)"). Make this a literal so the test pins the contract.
const TokenTTL = 30 * time.Second

// minHMACKeyLen is the minimum HMAC-SHA-256 key length we accept (32 bytes).
// In production the key comes from Secret Manager (no inline config — see
// `feedback_no_inline_config` memory).
const minHMACKeyLen = 32

// -----------------------------------------------------------------------------
// Source + Status enums
// -----------------------------------------------------------------------------

// Source is how an attendance record was captured.
type Source string

const (
	SourceQRScan Source = "qr-scan"
	SourceManual Source = "manual"
)

// ParseSource maps a string to Source or returns an error.
func ParseSource(s string) (Source, error) {
	switch Source(s) {
	case SourceQRScan, SourceManual:
		return Source(s), nil
	default:
		return "", fmt.Errorf("%w: unknown source %q (allowed: qr-scan, manual)", ErrInvalidArgument, s)
	}
}

// Status is the attendance verdict.
type Status string

const (
	StatusPresent Status = "present"
	StatusAbsent  Status = "absent"
	StatusLate    Status = "late"
	StatusExcused Status = "excused"
)

// Valid reports whether s is a recognised attendance status.
func (s Status) Valid() bool {
	switch s {
	case StatusPresent, StatusAbsent, StatusLate, StatusExcused:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// QR token
// -----------------------------------------------------------------------------

// TokenSigner mints + validates HMAC-SHA-256 signed QR tokens.
type TokenSigner struct {
	key []byte
}

// NewTokenSigner constructs a TokenSigner. The key MUST be at least 32 bytes;
// production keys come from Secret Manager.
func NewTokenSigner(key []byte) (*TokenSigner, error) {
	if len(key) < minHMACKeyLen {
		return nil, fmt.Errorf("%w: hmac key must be >= %d bytes (got %d)", ErrInvalidArgument, minHMACKeyLen, len(key))
	}
	// Defensive copy — caller may reuse / clear their slice.
	cp := make([]byte, len(key))
	copy(cp, key)
	return &TokenSigner{key: cp}, nil
}

// QRToken bundles a signed token string with its expiry stamp. The HTTP
// handler returns this verbatim to the instructor's QR display.
type QRToken struct {
	Token     string
	IssuedAt  time.Time
	ExpiresAt time.Time
	TenantID  string
	ClassID   string
}

// TokenClaims is the parsed inner payload of a validated token.
type TokenClaims struct {
	TenantID string
	ClassID  string
	IssuedAt time.Time
	Nonce    string // 8-byte hex; included so two tokens issued in the same ms differ
}

// Issue returns a signed token valid for TokenTTL from `now`.
//
// Format: base64url(payload).hex(hmac256(payload, key))
//
// Payload: tenant_id|class_id|issued_unix_ms|nonce_hex.
func (s *TokenSigner) Issue(tenantID, classID string, now time.Time) (*QRToken, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(classID) == "" {
		return nil, fmt.Errorf("%w: class_id required", ErrInvalidArgument)
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		// Fall back to deterministic-but-unique value derived from the time bits.
		ns := now.UnixNano()
		for i := range nonce {
			nonce[i] = byte(ns >> uint(8*(i%8)))
		}
	}
	nonceHex := hex.EncodeToString(nonce)
	payload := fmt.Sprintf("%s|%s|%d|%s", tenantID, classID, now.UnixMilli(), nonceHex)
	encPayload := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(encPayload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return &QRToken{
		Token:     encPayload + "." + sig,
		IssuedAt:  now,
		ExpiresAt: now.Add(TokenTTL),
		TenantID:  tenantID,
		ClassID:   classID,
	}, nil
}

// Validate verifies signature + TTL of `token` against `now`.
func (s *TokenSigner) Validate(token string, now time.Time) (*TokenClaims, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: malformed token", ErrTokenInvalid)
	}
	encPayload, sig := parts[0], parts[1]
	wantMAC := hmac.New(sha256.New, s.key)
	wantMAC.Write([]byte(encPayload))
	gotSig, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(wantMAC.Sum(nil), gotSig) {
		return nil, fmt.Errorf("%w: signature mismatch", ErrTokenInvalid)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return nil, fmt.Errorf("%w: bad payload encoding", ErrTokenInvalid)
	}
	fields := strings.Split(string(payloadBytes), "|")
	if len(fields) != 4 {
		return nil, fmt.Errorf("%w: payload field count", ErrTokenInvalid)
	}
	tenant, class, ts, nonce := fields[0], fields[1], fields[2], fields[3]
	issuedMillis, err := parseInt64(ts)
	if err != nil {
		return nil, fmt.Errorf("%w: bad timestamp", ErrTokenInvalid)
	}
	issuedAt := time.UnixMilli(issuedMillis).UTC()
	if now.After(issuedAt.Add(TokenTTL)) {
		return nil, ErrTokenExpired
	}
	return &TokenClaims{
		TenantID: tenant,
		ClassID:  class,
		IssuedAt: issuedAt,
		Nonce:    nonce,
	}, nil
}

// parseInt64 — small helper to keep package dependency-free.
func parseInt64(s string) (int64, error) {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-numeric character")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// -----------------------------------------------------------------------------
// Attendance log + Record (idempotent)
// -----------------------------------------------------------------------------

// Record is a single attendance fact.
type Record struct {
	ID         string
	TenantID   string
	ClassID    string
	GCID       string
	Source     Source
	Status     Status
	RecordedAt time.Time
}

// Log is an in-memory attendance store, keyed by (tenant, class, gcid) for
// idempotency. Production replaces this with a Postgres adapter.
type Log struct {
	mu      sync.RWMutex
	byKey   map[string]*Record   // (tenant|class|gcid) -> Record
	byClass map[string][]*Record // (tenant|class) -> [Record]
}

// NewLog returns an empty Log.
func NewLog() *Log {
	return &Log{
		byKey:   make(map[string]*Record),
		byClass: make(map[string][]*Record),
	}
}

// Record idempotently records attendance for (tenant, class, gcid).
//
// Returns:
//   - record (existing or newly-created)
//   - created=true ONLY on first insert; subsequent calls return created=false
//   - error on input validation only.
//
// Idempotency contract: the second call with the same (tenant, class, gcid)
// is a no-op — RecordedAt is preserved from the first call. This protects
// against double-scan from PWA refreshes.
func (l *Log) Record(tenantID, classID, gcid string, src Source, status Status, now time.Time) (*Record, bool, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, false, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(classID) == "" {
		return nil, false, fmt.Errorf("%w: class_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(gcid) == "" {
		return nil, false, fmt.Errorf("%w: gcid required", ErrInvalidArgument)
	}
	if !status.Valid() {
		return nil, false, fmt.Errorf("%w: invalid status %q", ErrInvalidArgument, status)
	}
	key := tenantID + "|" + classID + "|" + gcid
	classKey := tenantID + "|" + classID
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.byKey[key]; ok {
		return existing, false, nil
	}
	rec := &Record{
		ID:         NewUUIDv7(),
		TenantID:   tenantID,
		ClassID:    classID,
		GCID:       gcid,
		Source:     src,
		Status:     status,
		RecordedAt: now.UTC(),
	}
	l.byKey[key] = rec
	l.byClass[classKey] = append(l.byClass[classKey], rec)
	return rec, true, nil
}

// ListByClass returns the records for (tenant, class). Cross-tenant isolation
// is enforced by including tenantID in the lookup key.
func (l *Log) ListByClass(tenantID, classID string) []*Record {
	l.mu.RLock()
	defer l.mu.RUnlock()
	src := l.byClass[tenantID+"|"+classID]
	out := make([]*Record, len(src))
	copy(out, src)
	return out
}

// -----------------------------------------------------------------------------
// UUIDv7 (local — keeps package dependency-free)
// -----------------------------------------------------------------------------

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
