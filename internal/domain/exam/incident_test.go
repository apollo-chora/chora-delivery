// incident_test.go — TDD (RED-first) for the IncidentReport aggregate: an
// APPEND-ONLY audit record filed by an invigilator during an exam sitting
// (ADR-191 + docs/design/exam-administration-domain.md §4.7). Mirrors the
// AtomRevision append-only ethos — a record is IMMUTABLE once created (no
// update, no soft-delete: the struct carries neither UpdatedAt nor DeletedAt).
//
// Coverage target: 85% domain (per .claude/rules/development-execution.md).
package exam

import (
	"errors"
	"testing"
	"time"
)

const (
	incTenantID  = "019e2f93-d586-71b5-8c3d-e2b0d0d53100"
	incSitting   = "019e2f93-d586-71b5-8c3d-e2b0d0d53200"
	incReporter  = "019e2f93-d586-71b5-8c3d-e2b0d0d53300"
	incCandidate = "019e2f93-d586-71b5-8c3d-e2b0d0d53400"
)

func mustIncident(t *testing.T) *IncidentReport {
	t.Helper()
	ir, err := NewIncidentReport(NewIncidentReportInput{
		TenantID:       incTenantID,
		SittingID:      incSitting,
		ReportedByGCID: incReporter,
		CandidateRef:   incCandidate,
		Kind:           IncidentKindUnauthorizedMaterial,
		Narrative:      "Phone found in candidate's pocket during scan.",
		OccurredAt:     time.Date(2026, 8, 1, 10, 15, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewIncidentReport: unexpected err %v", err)
	}
	return ir
}

// -----------------------------------------------------------------------------
// Enum
// -----------------------------------------------------------------------------

func TestIncidentKind_IsValid(t *testing.T) {
	for _, k := range []IncidentKind{
		IncidentKindIdentityMismatch, IncidentKindUnauthorizedMaterial,
		IncidentKindCommunicationAttempt, IncidentKindDeviceViolation,
		IncidentKindBehaviorDisruption, IncidentKindTechnicalFailure,
		IncidentKindMedicalEmergency, IncidentKindFireAlarm, IncidentKindOther,
	} {
		if !k.IsValid() {
			t.Errorf("%q should be valid", k)
		}
	}
	if IncidentKind("cheating").IsValid() {
		t.Error("cheating must be invalid")
	}
	if IncidentKind("").IsValid() {
		t.Error("empty must be invalid")
	}
}

// -----------------------------------------------------------------------------
// Constructor
// -----------------------------------------------------------------------------

func TestNewIncidentReport_Defaults(t *testing.T) {
	ir := mustIncident(t)
	if ir.ID == "" {
		t.Error("ID must be a freshly generated UUIDv7")
	}
	if ir.TenantID != incTenantID || ir.SittingID != incSitting || ir.ReportedByGCID != incReporter {
		t.Errorf("field carry mismatch: %+v", ir)
	}
	if ir.CandidateRef != incCandidate {
		t.Errorf("candidate_ref=%q want carried", ir.CandidateRef)
	}
	if ir.Kind != IncidentKindUnauthorizedMaterial {
		t.Errorf("kind=%q want unauthorized_material", ir.Kind)
	}
	if ir.CreatedAt.IsZero() || ir.CreatedAt.Location() != time.UTC {
		t.Error("CreatedAt must be set + UTC")
	}
	if ir.OccurredAt.Location() != time.UTC {
		t.Error("OccurredAt must be UTC")
	}
}

func TestNewIncidentReport_OccurredAtDefaultsToNow(t *testing.T) {
	ir, err := NewIncidentReport(NewIncidentReportInput{
		TenantID:       incTenantID,
		SittingID:      incSitting,
		ReportedByGCID: incReporter,
		Kind:           IncidentKindFireAlarm,
		Narrative:      "Building alarm triggered; evacuation.",
		// OccurredAt omitted → defaults to now (UTC).
	})
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if ir.OccurredAt.IsZero() {
		t.Error("omitted OccurredAt must default to now, not zero")
	}
}

func TestNewIncidentReport_OptionalCandidateRef(t *testing.T) {
	// A room-level incident is not tied to a candidate.
	ir, err := NewIncidentReport(NewIncidentReportInput{
		TenantID:       incTenantID,
		SittingID:      incSitting,
		ReportedByGCID: incReporter,
		Kind:           IncidentKindTechnicalFailure,
		Narrative:      "Projector lost power for 5 minutes.",
	})
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if ir.CandidateRef != "" {
		t.Errorf("omitted candidate_ref must be empty, got %q", ir.CandidateRef)
	}
}

func TestNewIncidentReport_Validation(t *testing.T) {
	cases := []struct {
		name string
		in   NewIncidentReportInput
		want error
	}{
		{"blank tenant", NewIncidentReportInput{TenantID: " ", SittingID: incSitting, ReportedByGCID: incReporter, Kind: IncidentKindOther, Narrative: "x"}, ErrIncidentTenantRequired},
		{"blank sitting", NewIncidentReportInput{TenantID: incTenantID, SittingID: "", ReportedByGCID: incReporter, Kind: IncidentKindOther, Narrative: "x"}, ErrIncidentSittingRequired},
		{"blank reporter", NewIncidentReportInput{TenantID: incTenantID, SittingID: incSitting, ReportedByGCID: "\n", Kind: IncidentKindOther, Narrative: "x"}, ErrIncidentReporterRequired},
		{"bad kind", NewIncidentReportInput{TenantID: incTenantID, SittingID: incSitting, ReportedByGCID: incReporter, Kind: "cheating", Narrative: "x"}, ErrIncidentKindInvalid},
		{"blank narrative", NewIncidentReportInput{TenantID: incTenantID, SittingID: incSitting, ReportedByGCID: incReporter, Kind: IncidentKindOther, Narrative: "   "}, ErrIncidentNarrativeRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewIncidentReport(tc.in); !errors.Is(err, tc.want) {
				t.Errorf("err=%v want %v", err, tc.want)
			}
		})
	}
}

func TestNewIncidentReport_TrimsNarrativeAndRefs(t *testing.T) {
	ir, err := NewIncidentReport(NewIncidentReportInput{
		TenantID:       "  " + incTenantID + " ",
		SittingID:      " " + incSitting + " ",
		ReportedByGCID: " " + incReporter + " ",
		CandidateRef:   " " + incCandidate + " ",
		Kind:           IncidentKindOther,
		Narrative:      "  disturbance at the back  ",
	})
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if ir.TenantID != incTenantID || ir.SittingID != incSitting || ir.ReportedByGCID != incReporter {
		t.Errorf("whitespace not trimmed: %+v", ir)
	}
	if ir.CandidateRef != incCandidate {
		t.Errorf("candidate_ref not trimmed: %q", ir.CandidateRef)
	}
	if ir.Narrative != "disturbance at the back" {
		t.Errorf("narrative not trimmed: %q", ir.Narrative)
	}
}

// Append-only: distinct constructions yield distinct IDs (never an in-place
// mutation of an existing record).
func TestNewIncidentReport_DistinctIDs(t *testing.T) {
	a := mustIncident(t)
	b := mustIncident(t)
	if a.ID == b.ID {
		t.Error("each IncidentReport must have a distinct UUIDv7 id (append-only)")
	}
}
