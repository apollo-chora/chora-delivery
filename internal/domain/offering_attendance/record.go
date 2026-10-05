// Package offering_attendance owns the session-scoped attendance Record for the
// R+ Four-Mode Attendance tab — a learner's presence mark for one OfferingSession.
//
// Distinct from attendance.Record (which is CLASS-scoped + gRPC-coupled): this
// aggregate references a session_id (offering_sessions.id), not a class. It
// REUSES the attendance.Status (present|absent|late|excused) + attendance.Source
// (qr-scan|manual) vocabulary + validation so the two surfaces speak the same
// language, without overloading the class-scoped struct.
//
// The natural key is (tenant_id, session_id, gcid): re-marking a learner in the
// same session UPDATES the mark (an instructor can correct present→late) —
// upsert semantics, enforced at the DB by UNIQUE(tenant_id, session_id, gcid).
//
// Hexagonal: pure domain, no HTTP, no persistence. UUIDv7 ids, cross-aggregate
// refs (session_id, gcid) as opaque strings (no FK, ddd-enforcement #3).
package offering_attendance

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/attendance"
)

// ErrInvalidArgument signals a guard-clause failure in NewRecord.
var ErrInvalidArgument = errors.New("invalid argument")

// NewUUIDv7 returns a freshly generated UUIDv7 string (deps-free local copy,
// mirrors offering_session.NewUUIDv7).
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

// Record is one learner's attendance mark for one OfferingSession. Every field
// is exported → json.Marshal round-trips losslessly for the pg JSONB snapshot.
type Record struct {
	ID         string
	TenantID   string
	SessionID  string
	GCID       string
	Status     attendance.Status
	Source     attendance.Source
	RecordedAt time.Time
}

// NewRecordInput is the value-bag for NewRecord.
type NewRecordInput struct {
	TenantID  string
	SessionID string
	GCID      string
	Status    attendance.Status
	Source    attendance.Source
	Now       time.Time
}

// NewRecord constructs a mark with all guards.
//
// Validation: tenant_id, session_id, gcid all required (non-blank); status must
// be one of the four attendance.Status values. Source defaults to manual when
// blank (the tab's default entry mode).
func NewRecord(in NewRecordInput) (*Record, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return nil, fmt.Errorf("%w: session_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.GCID) == "" {
		return nil, fmt.Errorf("%w: gcid required", ErrInvalidArgument)
	}
	if !in.Status.Valid() {
		return nil, fmt.Errorf("%w: invalid status %q", ErrInvalidArgument, in.Status)
	}
	src := in.Source
	if strings.TrimSpace(string(src)) == "" {
		src = attendance.SourceManual
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	return &Record{
		ID:         NewUUIDv7(),
		TenantID:   in.TenantID,
		SessionID:  in.SessionID,
		GCID:       in.GCID,
		Status:     in.Status,
		Source:     src,
		RecordedAt: now.UTC(),
	}, nil
}
