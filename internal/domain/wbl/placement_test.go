// placement_test.go — RED tests driving the WblPlacement aggregate FSM.
//
// Per .claude/rules/development-execution.md TDD strict order: this file is
// written FIRST, every test must fail at compile (constructor + types do
// not yet exist), then the domain is filled in to drive each to GREEN.
//
// Coverage target: 85% domain (per .claude/rules/development-execution.md).
package wbl_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/wbl"
)

const (
	wblTestTenantID    = "019e2f93-d586-71b5-8c3d-e2b0d0d50100"
	wblTestLearnerGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d50101"
	wblTestCourseID    = "019e2f93-d586-71b5-8c3d-e2b0d0d50200"
)

func validInput() wbl.NewPlacementInput {
	start := time.Now().UTC().Add(24 * time.Hour)
	end := start.Add(60 * 24 * time.Hour)
	return wbl.NewPlacementInput{
		TenantID:        wblTestTenantID,
		GCID:            wblTestLearnerGCID,
		CourseID:        wblTestCourseID,
		HostOrgName:     "Acme Robotics Pte Ltd",
		SupervisorName:  "Ms. Tan",
		SupervisorEmail: "tan@acme.example",
		StartDate:       start,
		EndDate:         end,
		HoursRequired:   160,
	}
}

// -----------------------------------------------------------------------------
// Constructor
// -----------------------------------------------------------------------------

func TestNewPlacement_OK(t *testing.T) {
	p, err := wbl.NewPlacement(validInput())
	if err != nil {
		t.Fatalf("NewPlacement err=%v", err)
	}
	if p.State != wbl.PlacementStateScheduled {
		t.Errorf("state=%q want SCHEDULED", p.State)
	}
	if p.ID == "" {
		t.Errorf("id is empty")
	}
	if p.HoursCompleted != 0 {
		t.Errorf("hours_completed=%d want 0", p.HoursCompleted)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set")
	}
}

func TestNewPlacement_TenantRequired(t *testing.T) {
	in := validInput()
	in.TenantID = " "
	if _, err := wbl.NewPlacement(in); !errors.Is(err, wbl.ErrTenantRequired) {
		t.Errorf("err=%v want ErrTenantRequired", err)
	}
}

func TestNewPlacement_GCIDRequired(t *testing.T) {
	in := validInput()
	in.GCID = ""
	if _, err := wbl.NewPlacement(in); !errors.Is(err, wbl.ErrGCIDRequired) {
		t.Errorf("err=%v want ErrGCIDRequired", err)
	}
}

func TestNewPlacement_CourseIDRequired(t *testing.T) {
	in := validInput()
	in.CourseID = ""
	if _, err := wbl.NewPlacement(in); !errors.Is(err, wbl.ErrCourseIDRequired) {
		t.Errorf("err=%v want ErrCourseIDRequired", err)
	}
}

func TestNewPlacement_HostOrgRequired(t *testing.T) {
	in := validInput()
	in.HostOrgName = " "
	if _, err := wbl.NewPlacement(in); !errors.Is(err, wbl.ErrHostOrgRequired) {
		t.Errorf("err=%v want ErrHostOrgRequired", err)
	}
}

func TestNewPlacement_SupervisorRequired(t *testing.T) {
	in := validInput()
	in.SupervisorName = ""
	if _, err := wbl.NewPlacement(in); !errors.Is(err, wbl.ErrSupervisorRequired) {
		t.Errorf("err=%v want ErrSupervisorRequired", err)
	}
}

func TestNewPlacement_SupervisorEmailRequired(t *testing.T) {
	in := validInput()
	in.SupervisorEmail = "not-an-email"
	if _, err := wbl.NewPlacement(in); !errors.Is(err, wbl.ErrSupervisorEmailInvalid) {
		t.Errorf("err=%v want ErrSupervisorEmailInvalid", err)
	}
}

func TestNewPlacement_HoursRequiredPositive(t *testing.T) {
	in := validInput()
	in.HoursRequired = 0
	if _, err := wbl.NewPlacement(in); !errors.Is(err, wbl.ErrHoursRequiredPositive) {
		t.Errorf("err=%v want ErrHoursRequiredPositive", err)
	}
}

func TestNewPlacement_EndAfterStart(t *testing.T) {
	in := validInput()
	in.EndDate = in.StartDate.Add(-time.Hour)
	if _, err := wbl.NewPlacement(in); !errors.Is(err, wbl.ErrEndBeforeStart) {
		t.Errorf("err=%v want ErrEndBeforeStart", err)
	}
}

func TestNewPlacement_TrimsHostOrg(t *testing.T) {
	in := validInput()
	in.HostOrgName = "  Acme Robotics Pte Ltd  "
	p, err := wbl.NewPlacement(in)
	if err != nil {
		t.Fatalf("NewPlacement err=%v", err)
	}
	if p.HostOrgName != "Acme Robotics Pte Ltd" {
		t.Errorf("host_org=%q want trimmed", p.HostOrgName)
	}
}

// -----------------------------------------------------------------------------
// State machine
// -----------------------------------------------------------------------------

func TestPlacementState_IsValid(t *testing.T) {
	for _, s := range []wbl.PlacementState{
		wbl.PlacementStateScheduled,
		wbl.PlacementStateInProgress,
		wbl.PlacementStateCompleted,
		wbl.PlacementStateWithdrawn,
	} {
		if !s.IsValid() {
			t.Errorf("state %q reported invalid", s)
		}
	}
	if (wbl.PlacementState("BANANA")).IsValid() {
		t.Errorf("unknown state reported valid")
	}
}

func TestStart_FromScheduled_OK(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	before := p.UpdatedAt
	time.Sleep(time.Millisecond)
	if err := p.Start(); err != nil {
		t.Fatalf("Start err=%v", err)
	}
	if p.State != wbl.PlacementStateInProgress {
		t.Errorf("state=%q want IN_PROGRESS", p.State)
	}
	if !p.UpdatedAt.After(before) {
		t.Errorf("updated_at not bumped")
	}
}

func TestStart_RejectsNonScheduled(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	_ = p.Start()
	if err := p.Start(); !errors.Is(err, wbl.ErrNotScheduled) {
		t.Errorf("err=%v want ErrNotScheduled", err)
	}
}

func TestComplete_FromInProgress_OK(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	_ = p.Start()
	if err := p.Complete(); err != nil {
		t.Fatalf("Complete err=%v", err)
	}
	if p.State != wbl.PlacementStateCompleted {
		t.Errorf("state=%q want COMPLETED", p.State)
	}
}

func TestComplete_RejectsNonInProgress(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	if err := p.Complete(); !errors.Is(err, wbl.ErrNotInProgress) {
		t.Errorf("err=%v want ErrNotInProgress", err)
	}
}

func TestWithdraw_FromScheduled_OK(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	if err := p.Withdraw("learner pulled out"); err != nil {
		t.Fatalf("Withdraw err=%v", err)
	}
	if p.State != wbl.PlacementStateWithdrawn {
		t.Errorf("state=%q want WITHDRAWN", p.State)
	}
}

func TestWithdraw_RejectsAlreadyCompleted(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	_ = p.Start()
	_ = p.Complete()
	if err := p.Withdraw("late withdrawal"); !errors.Is(err, wbl.ErrPlacementClosed) {
		t.Errorf("err=%v want ErrPlacementClosed", err)
	}
}

func TestWithdraw_RejectsAlreadyWithdrawn(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	_ = p.Withdraw("first")
	if err := p.Withdraw("second"); !errors.Is(err, wbl.ErrPlacementClosed) {
		t.Errorf("err=%v want ErrPlacementClosed", err)
	}
}

// -----------------------------------------------------------------------------
// Updates
// -----------------------------------------------------------------------------

func TestRecordHours_OK(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	if err := p.RecordHours(40); err != nil {
		t.Fatalf("RecordHours err=%v", err)
	}
	if p.HoursCompleted != 40 {
		t.Errorf("hours_completed=%d want 40", p.HoursCompleted)
	}
}

func TestRecordHours_RejectsNegative(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	if err := p.RecordHours(-1); !errors.Is(err, wbl.ErrHoursNegative) {
		t.Errorf("err=%v want ErrHoursNegative", err)
	}
}

func TestRecordHours_RejectsOverflow(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	if err := p.RecordHours(p.HoursRequired + 1); !errors.Is(err, wbl.ErrHoursExceedsRequired) {
		t.Errorf("err=%v want ErrHoursExceedsRequired", err)
	}
}

func TestRecordHours_RejectsOnClosed(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	_ = p.Withdraw("done")
	if err := p.RecordHours(10); !errors.Is(err, wbl.ErrPlacementClosed) {
		t.Errorf("err=%v want ErrPlacementClosed", err)
	}
}

func TestSetEvaluatorNotes_OK(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	notes := strings.Repeat("a", 200)
	if err := p.SetEvaluatorNotes(notes); err != nil {
		t.Fatalf("SetEvaluatorNotes err=%v", err)
	}
	if p.EvaluatorNotes != notes {
		t.Errorf("evaluator_notes mismatch")
	}
}

func TestSetEvaluatorNotes_RejectsTooLong(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	notes := strings.Repeat("a", wbl.MaxEvaluatorNotesLen+1)
	if err := p.SetEvaluatorNotes(notes); !errors.Is(err, wbl.ErrEvaluatorNotesTooLong) {
		t.Errorf("err=%v want ErrEvaluatorNotesTooLong", err)
	}
}

func TestSetEvaluatorNotes_TrimsWhitespace(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	_ = p.SetEvaluatorNotes("   trimmed   ")
	if p.EvaluatorNotes != "trimmed" {
		t.Errorf("evaluator_notes=%q want trimmed", p.EvaluatorNotes)
	}
}
