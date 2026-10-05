// Package attendance owns QR-token + manual attendance for live classes.
// QR tokens are time-limited (30-second TTL per docs/design/
// ux_trainer_training_administration.md edge-case row), single-use, and
// HMAC-signed.
package attendance_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/attendance"
)

const (
	tenantA  = "01970000-0000-7000-8000-000000000001"
	classA   = "01970000-0000-7000-7000-000000000001"
	classB   = "01970000-0000-7000-7000-000000000002"
	gcid1    = "01970000-0000-7000-9000-000000000001"
	gcid2    = "01970000-0000-7000-9000-000000000002"
	hmacKey1 = "test-hmac-key-32-bytes-min-len-aaaaa"
	hmacKey2 = "different-hmac-key-32-bytes-min-zzzz"
)

// -----------------------------------------------------------------------------
// QR token issuance + validation
// -----------------------------------------------------------------------------

func TestQRToken_IssueAndValidate(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	signer, err := attendance.NewTokenSigner([]byte(hmacKey1))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	tok, err := signer.Issue(tenantA, classA, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if tok.Token == "" {
		t.Fatalf("empty token")
	}
	if !tok.ExpiresAt.Equal(now.Add(attendance.TokenTTL)) {
		t.Errorf("expires_at: got %v want %v", tok.ExpiresAt, now.Add(attendance.TokenTTL))
	}

	claims, err := signer.Validate(tok.Token, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("validate within TTL: %v", err)
	}
	if claims.TenantID != tenantA {
		t.Errorf("tenant: got %s want %s", claims.TenantID, tenantA)
	}
	if claims.ClassID != classA {
		t.Errorf("class: got %s want %s", claims.ClassID, classA)
	}
}

func TestQRToken_TTL30Seconds(t *testing.T) {
	if attendance.TokenTTL != 30*time.Second {
		t.Errorf("TokenTTL: got %v want 30s (per ADR-087 / ux_trainer_training_administration.md)", attendance.TokenTTL)
	}
}

func TestQRToken_ExpiredRejected(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	signer, _ := attendance.NewTokenSigner([]byte(hmacKey1))
	tok, _ := signer.Issue(tenantA, classA, now)
	// Validate strictly after TTL → ErrTokenExpired.
	_, err := signer.Validate(tok.Token, now.Add(attendance.TokenTTL+time.Millisecond))
	if !errors.Is(err, attendance.ErrTokenExpired) {
		t.Errorf("expired: got %v want ErrTokenExpired", err)
	}
}

func TestQRToken_TamperedRejected(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	signer, _ := attendance.NewTokenSigner([]byte(hmacKey1))
	tok, _ := signer.Issue(tenantA, classA, now)
	// Flip a single character in the payload portion.
	parts := strings.Split(tok.Token, ".")
	if len(parts) != 2 {
		t.Fatalf("token format: %s", tok.Token)
	}
	tampered := flipFirstChar(parts[0]) + "." + parts[1]
	if _, err := signer.Validate(tampered, now); !errors.Is(err, attendance.ErrTokenInvalid) {
		t.Errorf("tampered: got %v want ErrTokenInvalid", err)
	}
}

func TestQRToken_DifferentKeyRejected(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	signer1, _ := attendance.NewTokenSigner([]byte(hmacKey1))
	tok, _ := signer1.Issue(tenantA, classA, now)
	signer2, _ := attendance.NewTokenSigner([]byte(hmacKey2))
	if _, err := signer2.Validate(tok.Token, now); !errors.Is(err, attendance.ErrTokenInvalid) {
		t.Errorf("cross-key: got %v want ErrTokenInvalid", err)
	}
}

func TestNewTokenSigner_KeyValidation(t *testing.T) {
	if _, err := attendance.NewTokenSigner(nil); !errors.Is(err, attendance.ErrInvalidArgument) {
		t.Errorf("nil key: got %v want ErrInvalidArgument", err)
	}
	if _, err := attendance.NewTokenSigner([]byte("short")); !errors.Is(err, attendance.ErrInvalidArgument) {
		t.Errorf("short key: got %v want ErrInvalidArgument", err)
	}
}

// flipFirstChar replaces s[0] with a different character.
func flipFirstChar(s string) string {
	if s == "" {
		return s
	}
	c := byte('a')
	if s[0] == 'a' {
		c = 'b'
	}
	return string(c) + s[1:]
}

// -----------------------------------------------------------------------------
// Attendance record (single-use idempotency on (class_id, gcid))
// -----------------------------------------------------------------------------

func TestAttendanceLog_RecordIdempotent(t *testing.T) {
	log := attendance.NewLog()
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

	rec1, created, err := log.Record(tenantA, classA, gcid1, attendance.SourceQRScan, attendance.StatusPresent, now)
	if err != nil {
		t.Fatalf("record1: %v", err)
	}
	if !created {
		t.Errorf("first record: created=false want true")
	}
	if rec1.GCID != gcid1 || rec1.ClassID != classA {
		t.Errorf("rec1 ids mismatch")
	}
	// Idempotent — second scan returns the SAME record, no new id, created=false.
	rec2, created, err := log.Record(tenantA, classA, gcid1, attendance.SourceQRScan, attendance.StatusPresent, now.Add(time.Second))
	if err != nil {
		t.Fatalf("record2: %v", err)
	}
	if created {
		t.Errorf("second record: created=true want false")
	}
	if rec2.ID != rec1.ID {
		t.Errorf("idempotency broken: rec2.ID %s != rec1.ID %s", rec2.ID, rec1.ID)
	}
	if !rec2.RecordedAt.Equal(rec1.RecordedAt) {
		t.Errorf("RecordedAt overwritten on idempotent re-scan")
	}
}

func TestAttendanceLog_RecordRejectsInvalid(t *testing.T) {
	log := attendance.NewLog()
	now := time.Now()
	cases := []struct {
		name    string
		tenant  string
		class   string
		gcid    string
		status  attendance.Status
		wantErr error
	}{
		{"missing tenant", "", classA, gcid1, attendance.StatusPresent, attendance.ErrInvalidArgument},
		{"missing class", tenantA, "", gcid1, attendance.StatusPresent, attendance.ErrInvalidArgument},
		{"missing gcid", tenantA, classA, "", attendance.StatusPresent, attendance.ErrInvalidArgument},
		{"invalid status", tenantA, classA, gcid1, attendance.Status("nonsense"), attendance.ErrInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := log.Record(c.tenant, c.class, c.gcid, attendance.SourceQRScan, c.status, now)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("got %v want %v", err, c.wantErr)
			}
		})
	}
}

func TestAttendanceLog_ListByClass(t *testing.T) {
	log := attendance.NewLog()
	now := time.Now()
	log.Record(tenantA, classA, gcid1, attendance.SourceQRScan, attendance.StatusPresent, now) //nolint:errcheck
	log.Record(tenantA, classA, gcid2, attendance.SourceManual, attendance.StatusAbsent, now)  //nolint:errcheck
	log.Record(tenantA, classB, gcid1, attendance.SourceQRScan, attendance.StatusPresent, now) //nolint:errcheck
	got := log.ListByClass(tenantA, classA)
	if len(got) != 2 {
		t.Fatalf("ListByClass(classA): got %d want 2", len(got))
	}
	// Cross-tenant isolation.
	if other := log.ListByClass("other-tenant", classA); len(other) != 0 {
		t.Errorf("cross-tenant leak: got %d want 0", len(other))
	}
}

// -----------------------------------------------------------------------------
// Manual mark — instructor-only attendance status update
// -----------------------------------------------------------------------------

func TestManualMark_StatusValidation(t *testing.T) {
	for _, s := range []attendance.Status{attendance.StatusPresent, attendance.StatusAbsent, attendance.StatusLate, attendance.StatusExcused} {
		if !s.Valid() {
			t.Errorf("status %s: Valid()=false", s)
		}
	}
	if attendance.Status("garbage").Valid() {
		t.Errorf("garbage status: Valid()=true")
	}
}

func TestParseSource(t *testing.T) {
	cases := []struct {
		in   string
		want attendance.Source
		ok   bool
	}{
		{"qr-scan", attendance.SourceQRScan, true},
		{"manual", attendance.SourceManual, true},
		{"unknown", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := attendance.ParseSource(c.in)
			if !c.ok {
				if err == nil {
					t.Errorf("ParseSource(%q) want err, got %s", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if got != c.want {
				t.Errorf("got %s want %s", got, c.want)
			}
		})
	}
}
