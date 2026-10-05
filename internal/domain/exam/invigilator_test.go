// invigilator_test.go — TDD (RED-first) for the ExamInvigilator aggregate: a
// per-sitting proctor assignment carrying a RANK (ADR-191 D1 rank enum +
// docs/design/exam-administration-domain.md §4.6).
//
// Compliance invariant (ADR-191 O1): at most ONE chief_invigilator per sitting
// (the chief may pause/void). Enforced at the domain via EnsureSingleChief and
// at the DB via a partial-unique index (migration 0048).
//
// Coverage target: 85% domain (per .claude/rules/development-execution.md).
package exam

import (
	"errors"
	"testing"
	"time"
)

const (
	invTenantID = "019e2f93-d586-71b5-8c3d-e2b0d0d52100"
	invSitting  = "019e2f93-d586-71b5-8c3d-e2b0d0d52200"
	invGCID     = "019e2f93-d586-71b5-8c3d-e2b0d0d52300"
	invGCID2    = "019e2f93-d586-71b5-8c3d-e2b0d0d52301"
)

func mustInvigilator(t *testing.T, rank InvigilatorRank, gcid string) *ExamInvigilator {
	t.Helper()
	iv, err := NewExamInvigilator(NewExamInvigilatorInput{
		TenantID:        invTenantID,
		SittingID:       invSitting,
		InvigilatorGCID: gcid,
		Rank:            rank,
	})
	if err != nil {
		t.Fatalf("NewExamInvigilator(%q): unexpected err %v", rank, err)
	}
	return iv
}

// -----------------------------------------------------------------------------
// Enum
// -----------------------------------------------------------------------------

func TestInvigilatorRank_IsValid(t *testing.T) {
	for _, r := range []InvigilatorRank{
		InvigilatorRankChief, InvigilatorRankInvigilator,
		InvigilatorRankTechnicalSupport, InvigilatorRankObserver,
	} {
		if !r.IsValid() {
			t.Errorf("%q should be valid", r)
		}
	}
	if InvigilatorRank("supervisor").IsValid() {
		t.Error("supervisor must be invalid")
	}
	// The ADR-191 tokens are lowercase snake — assert the exact chief token.
	if InvigilatorRankChief != "chief_invigilator" {
		t.Errorf("chief token=%q want chief_invigilator", InvigilatorRankChief)
	}
}

// -----------------------------------------------------------------------------
// Constructor
// -----------------------------------------------------------------------------

func TestNewExamInvigilator_Defaults(t *testing.T) {
	iv := mustInvigilator(t, InvigilatorRankChief, invGCID)
	if iv.ID == "" {
		t.Error("ID must be a freshly generated UUIDv7")
	}
	if iv.Rank != InvigilatorRankChief {
		t.Errorf("rank=%q want chief_invigilator", iv.Rank)
	}
	if iv.TenantID != invTenantID || iv.SittingID != invSitting || iv.InvigilatorGCID != invGCID {
		t.Errorf("field carry mismatch: %+v", iv)
	}
	if iv.AssignedAt.IsZero() || iv.AssignedAt.Location() != time.UTC {
		t.Error("AssignedAt must be set + UTC")
	}
	if iv.DeletedAt != nil {
		t.Error("DeletedAt must be nil on a fresh assignment")
	}
}

func TestNewExamInvigilator_DefaultRankIsPlainInvigilator(t *testing.T) {
	iv, err := NewExamInvigilator(NewExamInvigilatorInput{
		TenantID:        invTenantID,
		SittingID:       invSitting,
		InvigilatorGCID: invGCID,
		// Rank omitted → least-privilege default (plain invigilator).
	})
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if iv.Rank != InvigilatorRankInvigilator {
		t.Errorf("default rank=%q want invigilator", iv.Rank)
	}
}

func TestNewExamInvigilator_Validation(t *testing.T) {
	cases := []struct {
		name string
		in   NewExamInvigilatorInput
		want error
	}{
		{"blank tenant", NewExamInvigilatorInput{TenantID: " ", SittingID: invSitting, InvigilatorGCID: invGCID}, ErrInvigilatorTenantRequired},
		{"blank sitting", NewExamInvigilatorInput{TenantID: invTenantID, SittingID: "", InvigilatorGCID: invGCID}, ErrInvigilatorSittingRequired},
		{"blank gcid", NewExamInvigilatorInput{TenantID: invTenantID, SittingID: invSitting, InvigilatorGCID: "\t"}, ErrInvigilatorGCIDRequired},
		{"bad rank", NewExamInvigilatorInput{TenantID: invTenantID, SittingID: invSitting, InvigilatorGCID: invGCID, Rank: "boss"}, ErrInvigilatorRankInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewExamInvigilator(tc.in); !errors.Is(err, tc.want) {
				t.Errorf("err=%v want %v", err, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Single-chief invariant (ADR-191 O1)
// -----------------------------------------------------------------------------

func TestExamInvigilator_IsChief(t *testing.T) {
	if !mustInvigilator(t, InvigilatorRankChief, invGCID).IsChief() {
		t.Error("chief_invigilator must report IsChief()==true")
	}
	if mustInvigilator(t, InvigilatorRankObserver, invGCID).IsChief() {
		t.Error("observer must report IsChief()==false")
	}
}

func TestEnsureSingleChief_RejectsSecondChief(t *testing.T) {
	existingChief := mustInvigilator(t, InvigilatorRankChief, invGCID)
	incoming := mustInvigilator(t, InvigilatorRankChief, invGCID2)
	err := EnsureSingleChief([]*ExamInvigilator{existingChief}, incoming)
	if !errors.Is(err, ErrInvigilatorChiefExists) {
		t.Errorf("err=%v want ErrInvigilatorChiefExists", err)
	}
}

func TestEnsureSingleChief_AllowsNonChiefAlongsideChief(t *testing.T) {
	existingChief := mustInvigilator(t, InvigilatorRankChief, invGCID)
	incoming := mustInvigilator(t, InvigilatorRankInvigilator, invGCID2)
	if err := EnsureSingleChief([]*ExamInvigilator{existingChief}, incoming); err != nil {
		t.Errorf("non-chief alongside a chief must be allowed, got %v", err)
	}
}

func TestEnsureSingleChief_AllowsFirstChief(t *testing.T) {
	plain := mustInvigilator(t, InvigilatorRankInvigilator, invGCID)
	incoming := mustInvigilator(t, InvigilatorRankChief, invGCID2)
	if err := EnsureSingleChief([]*ExamInvigilator{plain}, incoming); err != nil {
		t.Errorf("first chief must be allowed, got %v", err)
	}
}

func TestEnsureSingleChief_IgnoresSoftDeletedChief(t *testing.T) {
	old := mustInvigilator(t, InvigilatorRankChief, invGCID)
	_ = old.Unassign() // soft-deleted → no longer occupies the chief slot
	incoming := mustInvigilator(t, InvigilatorRankChief, invGCID2)
	if err := EnsureSingleChief([]*ExamInvigilator{old}, incoming); err != nil {
		t.Errorf("a soft-deleted chief must not block a new chief, got %v", err)
	}
}

func TestEnsureSingleChief_IgnoresSelf(t *testing.T) {
	chief := mustInvigilator(t, InvigilatorRankChief, invGCID)
	// Re-saving the SAME chief (same ID) must not trip the invariant.
	if err := EnsureSingleChief([]*ExamInvigilator{chief}, chief); err != nil {
		t.Errorf("re-saving the same chief must be allowed, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Soft delete (unassign)
// -----------------------------------------------------------------------------

func TestExamInvigilator_Unassign(t *testing.T) {
	iv := mustInvigilator(t, InvigilatorRankObserver, invGCID)
	if err := iv.Unassign(); err != nil {
		t.Fatalf("Unassign: %v", err)
	}
	if iv.DeletedAt == nil {
		t.Fatal("DeletedAt must be set after Unassign")
	}
	first := *iv.DeletedAt
	if err := iv.Unassign(); err != nil {
		t.Fatalf("2nd Unassign: %v", err)
	}
	if !iv.DeletedAt.Equal(first) {
		t.Error("Unassign must be idempotent")
	}
}
