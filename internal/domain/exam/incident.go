// incident.go owns the IncidentReport aggregate — an APPEND-ONLY audit record
// filed by an invigilator during an exam sitting (ADR-191 + design reference
// docs/design/exam-administration-domain.md §4.7, where it is named
// InvigilatorIncidentReport).
//
// APPEND-ONLY ETHOS (mirrors AtomRevision): a record is IMMUTABLE once created.
// The struct deliberately carries NEITHER UpdatedAt NOR DeletedAt — there is no
// mutator and no soft-delete. Corrections are new rows, never edits. The pg
// adapter is granted SELECT + INSERT only (no UPDATE/DELETE) so append-only is
// enforced at the DB, not merely by convention.
//
// sitting_id / reported_by_gcid are opaque cross-aggregate/cross-domain UUID
// references (no FK); candidate_ref is OPTIONAL (empty ⇒ a room-level incident
// not tied to a specific candidate).
//
// This file contains the domain only — NO HTTP, NO persistence imports. The
// UUIDv7 generator (newUUIDv7) is shared with exam.go in this package.
package exam

import (
	"errors"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// Kind enum (design doc §4.7 incident_type)
// -----------------------------------------------------------------------------

// IncidentKind categorises an incident.
type IncidentKind string

const (
	// IncidentKindIdentityMismatch — appearance does not match photo ID.
	IncidentKindIdentityMismatch IncidentKind = "identity_mismatch"
	// IncidentKindUnauthorizedMaterial — prohibited materials found.
	IncidentKindUnauthorizedMaterial IncidentKind = "unauthorized_material"
	// IncidentKindCommunicationAttempt — candidate tried to communicate.
	IncidentKindCommunicationAttempt IncidentKind = "communication_attempt"
	// IncidentKindDeviceViolation — unauthorised device detected.
	IncidentKindDeviceViolation IncidentKind = "device_violation"
	// IncidentKindBehaviorDisruption — disruptive behaviour.
	IncidentKindBehaviorDisruption IncidentKind = "behavior_disruption"
	// IncidentKindTechnicalFailure — equipment/software failure.
	IncidentKindTechnicalFailure IncidentKind = "technical_failure"
	// IncidentKindMedicalEmergency — candidate medical issue.
	IncidentKindMedicalEmergency IncidentKind = "medical_emergency"
	// IncidentKindFireAlarm — building alarm / evacuation.
	IncidentKindFireAlarm IncidentKind = "fire_alarm"
	// IncidentKindOther — any other incident type.
	IncidentKindOther IncidentKind = "other"
)

// IsValid reports whether k is one of the canonical incident kinds.
func (k IncidentKind) IsValid() bool {
	switch k {
	case IncidentKindIdentityMismatch, IncidentKindUnauthorizedMaterial,
		IncidentKindCommunicationAttempt, IncidentKindDeviceViolation,
		IncidentKindBehaviorDisruption, IncidentKindTechnicalFailure,
		IncidentKindMedicalEmergency, IncidentKindFireAlarm, IncidentKindOther:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Errors (sentinels)
// -----------------------------------------------------------------------------

var (
	// ErrIncidentTenantRequired — tenant_id must be non-empty.
	ErrIncidentTenantRequired = errors.New("incident: tenant_id required")
	// ErrIncidentSittingRequired — sitting_id must be non-empty.
	ErrIncidentSittingRequired = errors.New("incident: sitting_id required")
	// ErrIncidentReporterRequired — reported_by_gcid must be non-empty.
	ErrIncidentReporterRequired = errors.New("incident: reported_by_gcid required")
	// ErrIncidentKindInvalid — kind must be one of the canonical incident kinds.
	ErrIncidentKindInvalid = errors.New("incident: kind invalid")
	// ErrIncidentNarrativeRequired — narrative must be trim-non-empty.
	ErrIncidentNarrativeRequired = errors.New("incident: narrative required")
)

// -----------------------------------------------------------------------------
// Aggregate (append-only — no UpdatedAt, no DeletedAt, no mutators)
// -----------------------------------------------------------------------------

// IncidentReport is an append-only audit record filed during an exam sitting.
type IncidentReport struct {
	ID             string
	TenantID       string
	SittingID      string
	ReportedByGCID string
	CandidateRef   string // optional — empty ⇒ room-level incident
	Kind           IncidentKind
	Narrative      string
	OccurredAt     time.Time
	CreatedAt      time.Time
}

// NewIncidentReportInput is the constructor input bag.
type NewIncidentReportInput struct {
	TenantID       string
	SittingID      string
	ReportedByGCID string
	CandidateRef   string
	Kind           IncidentKind
	Narrative      string
	OccurredAt     time.Time
}

// NewIncidentReport constructs an immutable IncidentReport.
//
// Validation guards (rejected with a specific sentinel):
//   - tenant_id         trim-non-empty
//   - sitting_id        trim-non-empty
//   - reported_by_gcid  trim-non-empty
//   - kind              one of the canonical incident kinds
//   - narrative         trim-non-empty
//
// candidate_ref is optional (trimmed; empty allowed). An omitted OccurredAt
// (zero) defaults to now (UTC). The aggregate ID is a freshly generated
// UUIDv7; CreatedAt is current UTC. There are no mutators — corrections are new
// records.
func NewIncidentReport(in NewIncidentReportInput) (*IncidentReport, error) {
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		return nil, ErrIncidentTenantRequired
	}
	sittingID := strings.TrimSpace(in.SittingID)
	if sittingID == "" {
		return nil, ErrIncidentSittingRequired
	}
	reporter := strings.TrimSpace(in.ReportedByGCID)
	if reporter == "" {
		return nil, ErrIncidentReporterRequired
	}
	if !in.Kind.IsValid() {
		return nil, ErrIncidentKindInvalid
	}
	narrative := strings.TrimSpace(in.Narrative)
	if narrative == "" {
		return nil, ErrIncidentNarrativeRequired
	}
	now := time.Now().UTC()
	occurredAt := in.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = now
	}
	return &IncidentReport{
		ID:             newUUIDv7(),
		TenantID:       tenantID,
		SittingID:      sittingID,
		ReportedByGCID: reporter,
		CandidateRef:   strings.TrimSpace(in.CandidateRef),
		Kind:           in.Kind,
		Narrative:      narrative,
		OccurredAt:     occurredAt.UTC(),
		CreatedAt:      now,
	}, nil
}
