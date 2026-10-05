// candidate_test.go — TDD (RED-first) for the Candidate aggregate + the
// ID-verification ADMISSION GATE (ADR-190 D2).
//
// Compliance-grade target (#1): Admit() MUST fail with the sentinel
// ErrCandidateNotVerified unless VerificationStatus == VERIFIED. A candidate
// is NEVER admitted to a proctored sitting without a resolved Identity
// verification claim.
//
// FSM under test:
//
//	ALLOCATED --MarkVerified--> ID_VERIFIED --Admit--> ADMITTED
//	ALLOCATED --Reject-------->  REJECTED   (terminal)
//	ALLOCATED --Withdraw------>  WITHDRAWN  (terminal)
//	ID_VERIFIED --Reject------->  REJECTED
//	ID_VERIFIED --Withdraw----->  WITHDRAWN
//
// Coverage target: 85% domain (per .claude/rules/development-execution.md).
package exam

import (
	"errors"
	"testing"
	"time"
)

const (
	candTenantID = "019e2f93-d586-71b5-8c3d-e2b0d0d50100"
	candExamID   = "019e2f93-d586-71b5-8c3d-e2b0d0d50200"
	candGCID     = "019e2f93-d586-71b5-8c3d-e2b0d0d50300"
)

func mustCandidate(t *testing.T) *Candidate {
	t.Helper()
	c, err := NewCandidate(NewCandidateInput{
		TenantID: candTenantID,
		ExamID:   candExamID,
		GCID:     candGCID,
	})
	if err != nil {
		t.Fatalf("NewCandidate: unexpected err %v", err)
	}
	return c
}

// -----------------------------------------------------------------------------
// Constructor
// -----------------------------------------------------------------------------

func TestNewCandidate_Defaults(t *testing.T) {
	c := mustCandidate(t)
	if c.State != CandidateStateAllocated {
		t.Errorf("state=%q want ALLOCATED", c.State)
	}
	if c.VerificationStatus != VerificationStatusUnverified {
		t.Errorf("verification_status=%q want UNVERIFIED", c.VerificationStatus)
	}
	if c.ID == "" {
		t.Error("ID must be a freshly generated UUIDv7")
	}
	if c.TenantID != candTenantID || c.ExamID != candExamID || c.GCID != candGCID {
		t.Errorf("field carry mismatch: %+v", c)
	}
	if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		t.Error("timestamps must be set")
	}
	if c.CreatedAt.Location() != time.UTC || c.UpdatedAt.Location() != time.UTC {
		t.Error("timestamps must be UTC")
	}
	if c.DeletedAt != nil {
		t.Error("DeletedAt must be nil on a fresh candidate")
	}
}

func TestNewCandidate_TrimsWhitespace(t *testing.T) {
	c, err := NewCandidate(NewCandidateInput{
		TenantID: "  " + candTenantID + "  ",
		ExamID:   " " + candExamID + " ",
		GCID:     "\t" + candGCID + "\n",
	})
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}
	if c.TenantID != candTenantID || c.ExamID != candExamID || c.GCID != candGCID {
		t.Errorf("constructor must trim: %+v", c)
	}
}

func TestNewCandidate_Validation(t *testing.T) {
	cases := []struct {
		name string
		in   NewCandidateInput
		want error
	}{
		{"empty tenant", NewCandidateInput{TenantID: "  ", ExamID: candExamID, GCID: candGCID}, ErrCandidateTenantRequired},
		{"empty exam", NewCandidateInput{TenantID: candTenantID, ExamID: "", GCID: candGCID}, ErrCandidateExamRequired},
		{"empty gcid", NewCandidateInput{TenantID: candTenantID, ExamID: candExamID, GCID: " "}, ErrCandidateGCIDRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewCandidate(tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Enum validity
// -----------------------------------------------------------------------------

func TestCandidateState_IsValid(t *testing.T) {
	for _, s := range []CandidateState{
		CandidateStateAllocated, CandidateStateIDVerified, CandidateStateAdmitted,
		CandidateStateRejected, CandidateStateWithdrawn,
	} {
		if !s.IsValid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if CandidateState("BOGUS").IsValid() {
		t.Error("BOGUS must be invalid")
	}
}

func TestVerificationStatus_IsValid(t *testing.T) {
	if !VerificationStatusUnverified.IsValid() || !VerificationStatusVerified.IsValid() {
		t.Error("UNVERIFIED / VERIFIED must be valid")
	}
	if VerificationStatus("MAYBE").IsValid() {
		t.Error("MAYBE must be invalid")
	}
}

// -----------------------------------------------------------------------------
// THE ADMISSION GATE (#1 target)
// -----------------------------------------------------------------------------

func TestAdmit_RefusesUnverifiedCandidate(t *testing.T) {
	c := mustCandidate(t) // ALLOCATED / UNVERIFIED
	err := c.Admit()
	if !errors.Is(err, ErrCandidateNotVerified) {
		t.Fatalf("Admit on UNVERIFIED must return ErrCandidateNotVerified; got %v", err)
	}
	if c.State == CandidateStateAdmitted {
		t.Fatal("candidate must NOT be admitted without a verified claim")
	}
}

func TestAdmit_SucceedsAfterVerification(t *testing.T) {
	c := mustCandidate(t)
	if err := c.MarkVerified(); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	if c.State != CandidateStateIDVerified || c.VerificationStatus != VerificationStatusVerified {
		t.Fatalf("post-verify state=%q status=%q", c.State, c.VerificationStatus)
	}
	if err := c.Admit(); err != nil {
		t.Fatalf("Admit after verify: %v", err)
	}
	if c.State != CandidateStateAdmitted {
		t.Fatalf("state=%q want ADMITTED", c.State)
	}
}

func TestAdmit_IdempotencyGuard_AlreadyAdmitted(t *testing.T) {
	c := mustCandidate(t)
	_ = c.MarkVerified()
	_ = c.Admit()
	// Second Admit: still VERIFIED but no longer ID_VERIFIED → not admissible.
	if err := c.Admit(); !errors.Is(err, ErrCandidateNotAdmissible) {
		t.Fatalf("re-Admit must return ErrCandidateNotAdmissible; got %v", err)
	}
}

func TestAdmit_RejectedCandidate_StaysGated(t *testing.T) {
	c := mustCandidate(t)
	_ = c.Reject() // REJECTED, still UNVERIFIED
	if err := c.Admit(); !errors.Is(err, ErrCandidateNotVerified) {
		t.Fatalf("Admit on rejected/unverified must be gated; got %v", err)
	}
}

func TestAdmit_VerifiedThenWithdrawn_NotAdmissible(t *testing.T) {
	c := mustCandidate(t)
	_ = c.MarkVerified() // VERIFIED + ID_VERIFIED
	_ = c.Withdraw()     // WITHDRAWN (VerificationStatus stays VERIFIED)
	// Gate passes (VERIFIED) but state is terminal → not admissible.
	if err := c.Admit(); !errors.Is(err, ErrCandidateNotAdmissible) {
		t.Fatalf("Admit on withdrawn-after-verify must return ErrCandidateNotAdmissible; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// MarkVerified transitions
// -----------------------------------------------------------------------------

func TestMarkVerified_RequiresAllocated(t *testing.T) {
	c := mustCandidate(t)
	if err := c.MarkVerified(); err != nil {
		t.Fatalf("first MarkVerified: %v", err)
	}
	// Already ID_VERIFIED — not ALLOCATED.
	if err := c.MarkVerified(); !errors.Is(err, ErrCandidateNotAllocated) {
		t.Fatalf("second MarkVerified must return ErrCandidateNotAllocated; got %v", err)
	}
}

func TestMarkVerified_FromTerminal(t *testing.T) {
	c := mustCandidate(t)
	_ = c.Reject()
	if err := c.MarkVerified(); !errors.Is(err, ErrCandidateTerminal) {
		t.Fatalf("MarkVerified from REJECTED must return ErrCandidateTerminal; got %v", err)
	}
}

func TestMarkVerified_AdvancesUpdatedAt(t *testing.T) {
	c := mustCandidate(t)
	before := c.UpdatedAt
	time.Sleep(2 * time.Millisecond)
	if err := c.MarkVerified(); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	if !c.UpdatedAt.After(before) {
		t.Errorf("UpdatedAt must advance on transition (before=%v after=%v)", before, c.UpdatedAt)
	}
}

// -----------------------------------------------------------------------------
// Reject / Withdraw branches
// -----------------------------------------------------------------------------

func TestReject_FromAllocatedAndVerified(t *testing.T) {
	c1 := mustCandidate(t)
	if err := c1.Reject(); err != nil || c1.State != CandidateStateRejected {
		t.Fatalf("Reject from ALLOCATED: err=%v state=%q", err, c1.State)
	}
	c2 := mustCandidate(t)
	_ = c2.MarkVerified()
	if err := c2.Reject(); err != nil || c2.State != CandidateStateRejected {
		t.Fatalf("Reject from ID_VERIFIED: err=%v state=%q", err, c2.State)
	}
}

func TestWithdraw_FromAllocatedAndVerified(t *testing.T) {
	c1 := mustCandidate(t)
	if err := c1.Withdraw(); err != nil || c1.State != CandidateStateWithdrawn {
		t.Fatalf("Withdraw from ALLOCATED: err=%v state=%q", err, c1.State)
	}
	c2 := mustCandidate(t)
	_ = c2.MarkVerified()
	if err := c2.Withdraw(); err != nil || c2.State != CandidateStateWithdrawn {
		t.Fatalf("Withdraw from ID_VERIFIED: err=%v state=%q", err, c2.State)
	}
}

func TestReject_FromTerminal(t *testing.T) {
	c := mustCandidate(t)
	_ = c.Withdraw()
	if err := c.Reject(); !errors.Is(err, ErrCandidateTerminal) {
		t.Fatalf("Reject from WITHDRAWN must return ErrCandidateTerminal; got %v", err)
	}
}

func TestWithdraw_FromTerminal(t *testing.T) {
	c := mustCandidate(t)
	_ = c.MarkVerified()
	_ = c.Admit() // ADMITTED (terminal)
	if err := c.Withdraw(); !errors.Is(err, ErrCandidateTerminal) {
		t.Fatalf("Withdraw from ADMITTED must return ErrCandidateTerminal; got %v", err)
	}
}
