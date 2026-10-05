// Package attendance — manual attendance recording.
//
// Manual is the instructor-side override path: an instructor can mark a
// learner present/absent/late/excused even when the QR scan flow was
// missed (broken camera, late arrival, network issue per the
// docs/design/ux_trainer_training_administration.md edge-case row).
//
// Manual records share the same (tenant, class, gcid) idempotency key as
// QR records, so a manual mark AFTER a QR scan is a no-op (the QR scan
// wins). Operators wanting to overwrite must use ManualOverride below.
package attendance

import (
	"fmt"
	"strings"
	"time"
)

// ManualMark is the instructor-side path: idempotent insert that respects
// any pre-existing record (whether QR or manual). Wraps Log.Record with
// SourceManual.
//
// Returns the (record, created, error) triple from Log.Record.
func (l *Log) ManualMark(tenantID, classID, gcid string, status Status, now time.Time) (*Record, bool, error) {
	return l.Record(tenantID, classID, gcid, SourceManual, status, now)
}

// ManualOverride forcibly replaces an existing record's Status (e.g.,
// instructor corrects an erroneous QR scan). The original RecordedAt is
// preserved; only Status + Source are updated. Returns the updated record
// and a bool indicating whether an override happened (false = no record
// existed and a new one was inserted).
func (l *Log) ManualOverride(tenantID, classID, gcid string, status Status, now time.Time) (*Record, bool, error) {
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
	existing, ok := l.byKey[key]
	if !ok {
		// No record yet → insert manual record.
		rec := &Record{
			ID:         NewUUIDv7(),
			TenantID:   tenantID,
			ClassID:    classID,
			GCID:       gcid,
			Source:     SourceManual,
			Status:     status,
			RecordedAt: now.UTC(),
		}
		l.byKey[key] = rec
		l.byClass[classKey] = append(l.byClass[classKey], rec)
		return rec, false, nil
	}
	// Override status + source while preserving id + recorded_at.
	existing.Status = status
	existing.Source = SourceManual
	return existing, true, nil
}
