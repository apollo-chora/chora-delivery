// coverage_test.go — closes the remaining statement gaps in placement.go:
// Withdraw's empty-reason / notes-append branches and looksLikeEmail's
// whitespace + missing-dot rejections.
package wbl_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/wbl"
)

func TestWithdraw_EmptyReason_NoMarker(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	if err := p.Withdraw(""); err != nil {
		t.Fatalf("Withdraw(''): %v", err)
	}
	if p.State != wbl.PlacementStateWithdrawn {
		t.Errorf("state=%q want WITHDRAWN", p.State)
	}
	if p.EvaluatorNotes != "" {
		t.Errorf("empty reason must not write a marker; got %q", p.EvaluatorNotes)
	}
}

func TestWithdraw_AppendsToExistingNotes(t *testing.T) {
	p, _ := wbl.NewPlacement(validInput())
	if err := p.SetEvaluatorNotes("evaluator notes"); err != nil {
		t.Fatalf("SetEvaluatorNotes: %v", err)
	}
	if err := p.Withdraw("learner withdrew"); err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	want := "evaluator notes\n[WITHDRAWN] learner withdrew"
	if p.EvaluatorNotes != want {
		t.Errorf("notes=%q want %q (append, never overwrite)", p.EvaluatorNotes, want)
	}
}

func TestLooksLikeEmail_RejectsWhitespaceAndMissingDot(t *testing.T) {
	in := validInput()
	in.SupervisorEmail = "tan @acme.example"
	if _, err := wbl.NewPlacement(in); !errors.Is(err, wbl.ErrSupervisorEmailInvalid) {
		t.Errorf("space in email: want ErrSupervisorEmailInvalid, got %v", err)
	}
	in = validInput()
	in.SupervisorEmail = "tan@localhost" // '@' present but no dot after it
	if _, err := wbl.NewPlacement(in); !errors.Is(err, wbl.ErrSupervisorEmailInvalid) {
		t.Errorf("missing dot: want ErrSupervisorEmailInvalid, got %v", err)
	}
}
