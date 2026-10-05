// examform_test.go — TDD for the ExamForm aggregate (W4 Brick-1).
//
// ExamForm is the revision-pinned, exposure-locked assembly of question-bank
// items for a proctored exam (ADR-190 D2). The invariants pinned here are
// legal/compliance-grade:
//
//   - cannot Expose an empty form (defence-in-depth on top of Assemble)
//   - once EXPOSED the item set is immutable (exposure-lock → sentinel error)
//   - RETIRED is terminal
//   - every pinned item carries a pinned AtomRevisionID (revision-pin)
//
// White-box (package exam) so a few tests can force an otherwise-unreachable
// invalid state (empty ASSEMBLED) to exercise Expose's independent guard.
package exam

import (
	"errors"
	"testing"
)

const (
	formTestTenantID = "019e2f93-d586-71b5-8c3d-e2b0d0d50100"
	formTestExamID   = "019e2f93-d586-71b5-8c3d-e2b0d0d50300"
	formTestBankID   = "019e2f93-d586-71b5-8c3d-e2b0d0d50400"
)

func pin(pos int) PinnedItem {
	return PinnedItem{
		ItemID:         "item-" + string(rune('a'+pos)),
		AtomRevisionID: "rev-" + string(rune('a'+pos)),
		Position:       pos,
	}
}

func mustRawCut(t *testing.T, max, mark int) CutScore {
	t.Helper()
	c, err := NewRawCutScore(max, mark)
	if err != nil {
		t.Fatalf("NewRawCutScore: %v", err)
	}
	return c
}

func newDraftForm(t *testing.T) *ExamForm {
	t.Helper()
	f, err := NewExamForm(NewExamFormInput{
		TenantID:   formTestTenantID,
		ExamID:     formTestExamID,
		ItemBankID: formTestBankID,
	})
	if err != nil {
		t.Fatalf("NewExamForm: %v", err)
	}
	return f
}

// assembledForm returns an ASSEMBLED form with n items + a raw cut (max=100,
// mark=60), the standard fixture for expose/retire/grade tests.
func assembledForm(t *testing.T, n int) *ExamForm {
	t.Helper()
	f := newDraftForm(t)
	for i := 0; i < n; i++ {
		if err := f.AddItem(pin(i)); err != nil {
			t.Fatalf("AddItem: %v", err)
		}
	}
	if err := f.SetCutScore(mustRawCut(t, 100, 60)); err != nil {
		t.Fatalf("SetCutScore: %v", err)
	}
	if err := f.Assemble(); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return f
}

func TestNewExamForm_OK_DraftShell(t *testing.T) {
	t.Parallel()
	f := newDraftForm(t)
	if f.State != ExamFormStateDraft {
		t.Errorf("state=%s want DRAFT", f.State)
	}
	if f.ID == "" {
		t.Error("ID must be generated (UUIDv7)")
	}
	if f.TenantID != formTestTenantID || f.ExamID != formTestExamID || f.ItemBankID != formTestBankID {
		t.Errorf("refs not populated: %+v", f)
	}
	if f.CreatedAt.IsZero() || f.UpdatedAt.IsZero() {
		t.Error("timestamps must be set")
	}
	if len(f.Items) != 0 || f.Cut != nil {
		t.Error("draft shell must be empty")
	}
	if f.DeletedAt != nil {
		t.Error("must not be soft-deleted")
	}
}

func TestNewExamForm_Errors(t *testing.T) {
	t.Parallel()
	if _, err := NewExamForm(NewExamFormInput{ExamID: formTestExamID, ItemBankID: formTestBankID}); !errors.Is(err, ErrExamFormTenantRequired) {
		t.Errorf("tenant want ErrExamFormTenantRequired; got %v", err)
	}
	if _, err := NewExamForm(NewExamFormInput{TenantID: formTestTenantID, ItemBankID: formTestBankID}); !errors.Is(err, ErrExamFormExamRequired) {
		t.Errorf("exam want ErrExamFormExamRequired; got %v", err)
	}
	if _, err := NewExamForm(NewExamFormInput{TenantID: formTestTenantID, ExamID: formTestExamID}); !errors.Is(err, ErrExamFormItemBankRequired) {
		t.Errorf("bank want ErrExamFormItemBankRequired; got %v", err)
	}
}

func TestExamForm_AddItem_ValidatesRevisionPin(t *testing.T) {
	t.Parallel()
	f := newDraftForm(t)
	if err := f.AddItem(PinnedItem{ItemID: "", AtomRevisionID: "rev"}); !errors.Is(err, ErrExamFormItemIDRequired) {
		t.Errorf("empty item_id want ErrExamFormItemIDRequired; got %v", err)
	}
	if err := f.AddItem(PinnedItem{ItemID: "it", AtomRevisionID: ""}); !errors.Is(err, ErrExamFormItemRevisionRequired) {
		t.Errorf("empty atom_revision_id (must be revision-pinned) want ErrExamFormItemRevisionRequired; got %v", err)
	}
	if err := f.AddItem(pin(0)); err != nil {
		t.Fatalf("valid AddItem: %v", err)
	}
	if len(f.Items) != 1 {
		t.Fatalf("items=%d want 1", len(f.Items))
	}
}

func TestExamForm_Assemble_RequiresNonEmptyDraft(t *testing.T) {
	t.Parallel()
	f := newDraftForm(t)
	if err := f.Assemble(); !errors.Is(err, ErrExamFormEmpty) {
		t.Errorf("empty assemble want ErrExamFormEmpty; got %v", err)
	}
	if err := f.AddItem(pin(0)); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if err := f.Assemble(); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if f.State != ExamFormStateAssembled {
		t.Errorf("state=%s want ASSEMBLED", f.State)
	}
	// re-assemble rejected (no longer DRAFT).
	if err := f.Assemble(); !errors.Is(err, ErrExamFormNotDraft) {
		t.Errorf("re-assemble want ErrExamFormNotDraft; got %v", err)
	}
}

func TestExamForm_Expose_RejectsFromDraft(t *testing.T) {
	t.Parallel()
	f := newDraftForm(t)
	if err := f.Expose(); !errors.Is(err, ErrExamFormNotAssembled) {
		t.Errorf("expose from DRAFT want ErrExamFormNotAssembled; got %v", err)
	}
}

// Defence-in-depth: even if a form were somehow ASSEMBLED with no items,
// Expose must independently refuse to expose an empty form.
func TestExamForm_Expose_RejectsEmptyForm(t *testing.T) {
	t.Parallel()
	f := newDraftForm(t)
	f.State = ExamFormStateAssembled // white-box: force the unreachable empty ASSEMBLED
	if err := f.Expose(); !errors.Is(err, ErrExamFormEmpty) {
		t.Errorf("expose empty want ErrExamFormEmpty; got %v", err)
	}
}

func TestExamForm_Expose_RequiresCutScore(t *testing.T) {
	t.Parallel()
	f := newDraftForm(t)
	if err := f.AddItem(pin(0)); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if err := f.Assemble(); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if err := f.Expose(); !errors.Is(err, ErrExamFormNoCutScore) {
		t.Errorf("expose without cut want ErrExamFormNoCutScore; got %v", err)
	}
}

func TestExamForm_Expose_HappyPath_LocksItems(t *testing.T) {
	t.Parallel()
	f := assembledForm(t, 3)
	if err := f.Expose(); err != nil {
		t.Fatalf("expose: %v", err)
	}
	if f.State != ExamFormStateExposed {
		t.Errorf("state=%s want EXPOSED", f.State)
	}
	if f.ExposedAt == nil {
		t.Error("ExposedAt must be stamped on expose")
	}
	// exposure-lock: every mutation returns the sentinel.
	if err := f.AddItem(pin(9)); !errors.Is(err, ErrExamFormExposureLocked) {
		t.Errorf("AddItem after expose want ErrExamFormExposureLocked; got %v", err)
	}
	if err := f.SetCutScore(mustRawCut(t, 100, 70)); !errors.Is(err, ErrExamFormExposureLocked) {
		t.Errorf("SetCutScore after expose want ErrExamFormExposureLocked; got %v", err)
	}
}

func TestExamForm_Expose_RejectsAlreadyExposed(t *testing.T) {
	t.Parallel()
	f := assembledForm(t, 2)
	if err := f.Expose(); err != nil {
		t.Fatalf("expose: %v", err)
	}
	if err := f.Expose(); !errors.Is(err, ErrExamFormAlreadyExposed) {
		t.Errorf("double expose want ErrExamFormAlreadyExposed; got %v", err)
	}
}

func TestExamForm_Retire_FromExposed_Terminal(t *testing.T) {
	t.Parallel()
	f := assembledForm(t, 2)
	if err := f.Expose(); err != nil {
		t.Fatalf("expose: %v", err)
	}
	if err := f.Retire(); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if f.State != ExamFormStateRetired {
		t.Errorf("state=%s want RETIRED", f.State)
	}
	if f.RetiredAt == nil {
		t.Error("RetiredAt must be stamped")
	}
	if !f.State.IsTerminal() {
		t.Error("RETIRED must be terminal")
	}
	// terminal: retire again rejected.
	if err := f.Retire(); !errors.Is(err, ErrExamFormRetired) {
		t.Errorf("re-retire want ErrExamFormRetired; got %v", err)
	}
	// exposure-lock still holds on a retired form.
	if err := f.AddItem(pin(9)); !errors.Is(err, ErrExamFormExposureLocked) {
		t.Errorf("mutate retired want ErrExamFormExposureLocked; got %v", err)
	}
}

func TestExamForm_Retire_RejectsNotExposed(t *testing.T) {
	t.Parallel()
	f := assembledForm(t, 2) // ASSEMBLED, never exposed
	if err := f.Retire(); !errors.Is(err, ErrExamFormNotExposed) {
		t.Errorf("retire ASSEMBLED want ErrExamFormNotExposed; got %v", err)
	}
}

func TestExamFormState_IsValid(t *testing.T) {
	t.Parallel()
	for _, s := range []ExamFormState{ExamFormStateDraft, ExamFormStateAssembled, ExamFormStateExposed, ExamFormStateRetired} {
		if !s.IsValid() {
			t.Errorf("%s should be valid", s)
		}
	}
	if ExamFormState("BOGUS").IsValid() {
		t.Error("bogus state must be invalid")
	}
	if ExamFormStateDraft.IsTerminal() {
		t.Error("DRAFT is not terminal")
	}
}

func TestExamForm_Grade_RequiresExposed(t *testing.T) {
	t.Parallel()
	f := assembledForm(t, 3) // not exposed
	if _, _, err := f.Grade("cand", 80); !errors.Is(err, ErrExamFormNotExposed) {
		t.Errorf("grade unexposed want ErrExamFormNotExposed; got %v", err)
	}
}

func TestExamForm_Grade_HappyPath_EmitsEvent(t *testing.T) {
	t.Parallel()
	f := assembledForm(t, 3) // cut max=100 mark=60
	if err := f.Expose(); err != nil {
		t.Fatalf("expose: %v", err)
	}
	const cand = "019e2f93-d586-71b5-8c3d-e2b0d0d50500"
	res, ev, err := f.Grade(cand, 60) // exactly at cut → PASS
	if err != nil {
		t.Fatalf("grade: %v", err)
	}
	if res.Outcome != OutcomePass {
		t.Errorf("raw==cut → PASS; got %s", res.Outcome)
	}
	if res.MaxScore != 100 || res.RawScore != 60 {
		t.Errorf("scores wrong: %+v", res)
	}
	if res.ExamFormID != f.ID || res.ExamID != f.ExamID || res.TenantID != f.TenantID || res.CandidateRef != cand {
		t.Errorf("refs wrong: %+v", res)
	}
	// Event value carries the outcome-spine / cert seam fields.
	if ev.ResultID != res.ID || ev.Outcome != OutcomePass || ev.PassMark != 60 || ev.MaxScore != 100 {
		t.Errorf("event value wrong: %+v", ev)
	}
	if ev.OccurredAt.IsZero() {
		t.Error("event OccurredAt must be set")
	}
}
