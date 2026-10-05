// examform.go — the ExamForm aggregate for the Exam bounded context.
//
// ExamForm is a revision-pinned, exposure-locked assembly of question-bank
// items for a proctored exam (ADR-190 D2). It GROWS from the live
// internal/domain/exam.Exam aggregate — same package, same DB (chora_delivery),
// ADD-ONLY. Cross-domain references (ExamID, ItemBankID, and each pinned item's
// ItemID/AtomRevisionID) travel as opaque UUIDs — no Go-level FK, validated
// over events per .claude/rules/ddd-enforcement.md.
//
// State machine (FSM):
//
//	DRAFT     → ASSEMBLED  via Assemble (requires >= 1 pinned item)
//	ASSEMBLED → EXPOSED    via Expose   (requires items + a CutScore)
//	EXPOSED   → RETIRED    via Retire   (terminal)
//
// Governing invariants (compliance-grade):
//   - cannot Expose an empty form
//   - once EXPOSED the item set is IMMUTABLE (exposure-lock) — every mutation
//     returns ErrExamFormExposureLocked
//   - RETIRED is terminal
//   - every PinnedItem carries a pinned AtomRevisionID (revision-pin) so an
//     exposed item can never silently drift to a newer revision
//
// Pure domain — NO http, NO persistence imports. newUUIDv7 is the package-local
// generator defined in exam.go.
package exam

import (
	"errors"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// State
// -----------------------------------------------------------------------------

// ExamFormState models the ExamForm lifecycle FSM.
type ExamFormState string

const (
	// ExamFormStateDraft — admin is assembling the form; items mutable.
	ExamFormStateDraft ExamFormState = "DRAFT"
	// ExamFormStateAssembled — items + cut locked in, ready to expose.
	ExamFormStateAssembled ExamFormState = "ASSEMBLED"
	// ExamFormStateExposed — the form is live to candidates; item set immutable.
	ExamFormStateExposed ExamFormState = "EXPOSED"
	// ExamFormStateRetired — withdrawn; terminal.
	ExamFormStateRetired ExamFormState = "RETIRED"
)

// IsValid reports whether s is one of the canonical states.
func (s ExamFormState) IsValid() bool {
	switch s {
	case ExamFormStateDraft, ExamFormStateAssembled, ExamFormStateExposed, ExamFormStateRetired:
		return true
	}
	return false
}

// IsTerminal reports whether s is a terminal state (RETIRED).
func (s ExamFormState) IsTerminal() bool {
	return s == ExamFormStateRetired
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	// ErrExamFormTenantRequired — tenant_id must be non-empty.
	ErrExamFormTenantRequired = errors.New("exam_form: tenant_id required")
	// ErrExamFormExamRequired — exam_id must be non-empty.
	ErrExamFormExamRequired = errors.New("exam_form: exam_id required")
	// ErrExamFormItemBankRequired — item_bank_id must be non-empty.
	ErrExamFormItemBankRequired = errors.New("exam_form: item_bank_id required")
	// ErrExamFormItemIDRequired — a pinned item's item_id must be non-empty.
	ErrExamFormItemIDRequired = errors.New("exam_form: pinned item_id required")
	// ErrExamFormItemRevisionRequired — a pinned item must carry an
	// atom_revision_id (revision-pin); an unpinned item is refused.
	ErrExamFormItemRevisionRequired = errors.New("exam_form: pinned item atom_revision_id required (revision-pin)")
	// ErrExamFormEmpty — Assemble/Expose require at least one pinned item.
	ErrExamFormEmpty = errors.New("exam_form: form has no pinned items")
	// ErrExamFormExposureLocked — the item set is immutable once EXPOSED/RETIRED.
	ErrExamFormExposureLocked = errors.New("exam_form: exposure-locked (item set immutable)")
	// ErrExamFormNotDraft — operation requires DRAFT state.
	ErrExamFormNotDraft = errors.New("exam_form: not in DRAFT state")
	// ErrExamFormNotAssembled — Expose requires ASSEMBLED state.
	ErrExamFormNotAssembled = errors.New("exam_form: not in ASSEMBLED state")
	// ErrExamFormNotExposed — Retire/Grade require EXPOSED state.
	ErrExamFormNotExposed = errors.New("exam_form: not in EXPOSED state")
	// ErrExamFormAlreadyExposed — Expose was called on an already-EXPOSED form.
	ErrExamFormAlreadyExposed = errors.New("exam_form: already EXPOSED")
	// ErrExamFormRetired — the form is RETIRED (terminal).
	ErrExamFormRetired = errors.New("exam_form: RETIRED (terminal)")
	// ErrExamFormNoCutScore — Expose requires a CutScore to be set.
	ErrExamFormNoCutScore = errors.New("exam_form: no cut_score set")
)

// -----------------------------------------------------------------------------
// Value objects
// -----------------------------------------------------------------------------

// PinnedItem is a revision-pinned reference to a question-bank item. ItemID
// references the questionbank aggregate in chora_creation (no FK);
// AtomRevisionID pins the exact revision exposed in this form so the item can
// never drift to a newer revision after exposure.
type PinnedItem struct {
	ItemID         string `json:"item_id"`
	AtomRevisionID string `json:"atom_revision_id"`
	Position       int    `json:"position"`
}

// -----------------------------------------------------------------------------
// Aggregate
// -----------------------------------------------------------------------------

// ExamForm is the revision-pinned, exposure-locked exam-form aggregate root.
//
// Soft delete via DeletedAt; transitions advance UpdatedAt. Cut is a pointer so
// "not yet set" is distinguishable from a zero-value cut.
type ExamForm struct {
	ID         string
	TenantID   string
	ExamID     string
	ItemBankID string
	Items      []PinnedItem
	Cut        *CutScore
	State      ExamFormState
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ExposedAt  *time.Time
	RetiredAt  *time.Time
	DeletedAt  *time.Time
}

// NewExamFormInput is the constructor input bag.
type NewExamFormInput struct {
	TenantID   string
	ExamID     string
	ItemBankID string
}

// NewExamForm constructs a DRAFT-state ExamForm shell (no items, no cut).
func NewExamForm(in NewExamFormInput) (*ExamForm, error) {
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		return nil, ErrExamFormTenantRequired
	}
	examID := strings.TrimSpace(in.ExamID)
	if examID == "" {
		return nil, ErrExamFormExamRequired
	}
	bankID := strings.TrimSpace(in.ItemBankID)
	if bankID == "" {
		return nil, ErrExamFormItemBankRequired
	}
	now := time.Now().UTC()
	return &ExamForm{
		ID:         newUUIDv7(),
		TenantID:   tenantID,
		ExamID:     examID,
		ItemBankID: bankID,
		Items:      nil,
		State:      ExamFormStateDraft,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// mutable reports whether the form still accepts item/cut mutation. Once
// EXPOSED (or RETIRED), the exposure-lock holds.
func (f *ExamForm) mutable() bool {
	return f.State == ExamFormStateDraft || f.State == ExamFormStateAssembled
}

// AddItem appends a revision-pinned item. Refused once the form is
// exposure-locked; the item must carry both an item_id and an atom_revision_id.
func (f *ExamForm) AddItem(it PinnedItem) error {
	if !f.mutable() {
		return ErrExamFormExposureLocked
	}
	if strings.TrimSpace(it.ItemID) == "" {
		return ErrExamFormItemIDRequired
	}
	if strings.TrimSpace(it.AtomRevisionID) == "" {
		return ErrExamFormItemRevisionRequired
	}
	f.Items = append(f.Items, PinnedItem{
		ItemID:         strings.TrimSpace(it.ItemID),
		AtomRevisionID: strings.TrimSpace(it.AtomRevisionID),
		Position:       it.Position,
	})
	f.UpdatedAt = time.Now().UTC()
	return nil
}

// SetCutScore sets (or replaces) the pass-mark. Refused once exposure-locked;
// the cut is validated before it is stored.
func (f *ExamForm) SetCutScore(c CutScore) error {
	if !f.mutable() {
		return ErrExamFormExposureLocked
	}
	if err := c.Validate(); err != nil {
		return err
	}
	cut := c
	f.Cut = &cut
	f.UpdatedAt = time.Now().UTC()
	return nil
}

// Assemble transitions DRAFT → ASSEMBLED. Requires at least one pinned item.
func (f *ExamForm) Assemble() error {
	if f.State != ExamFormStateDraft {
		return ErrExamFormNotDraft
	}
	if len(f.Items) == 0 {
		return ErrExamFormEmpty
	}
	f.State = ExamFormStateAssembled
	f.UpdatedAt = time.Now().UTC()
	return nil
}

// Expose transitions ASSEMBLED → EXPOSED, locking the item set. Requires a
// non-empty form AND a valid CutScore (defence-in-depth on both, since a form
// cannot be scored without a cut and must never be exposed empty).
func (f *ExamForm) Expose() error {
	switch f.State {
	case ExamFormStateExposed:
		return ErrExamFormAlreadyExposed
	case ExamFormStateRetired:
		return ErrExamFormRetired
	case ExamFormStateAssembled:
		// ok
	default:
		return ErrExamFormNotAssembled
	}
	if len(f.Items) == 0 {
		return ErrExamFormEmpty
	}
	if f.Cut == nil {
		return ErrExamFormNoCutScore
	}
	if err := f.Cut.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	f.State = ExamFormStateExposed
	f.ExposedAt = &now
	f.UpdatedAt = now
	return nil
}

// Retire transitions EXPOSED → RETIRED (terminal).
func (f *ExamForm) Retire() error {
	switch f.State {
	case ExamFormStateRetired:
		return ErrExamFormRetired
	case ExamFormStateExposed:
		// ok
	default:
		return ErrExamFormNotExposed
	}
	now := time.Now().UTC()
	f.State = ExamFormStateRetired
	f.RetiredAt = &now
	f.UpdatedAt = now
	return nil
}

// Grade is the finalize path: it scores a candidate's raw score against this
// form's exposure-locked CutScore and returns the durable ExamResult plus the
// ExamResultReleased event value. The form MUST be EXPOSED (you score against a
// live, locked form) and MUST carry a cut. This method delegates the pure
// PASS/FAIL comparison to NewExamResult (the #1 compliance path).
func (f *ExamForm) Grade(candidateRef string, rawScore int) (*ExamResult, ExamResultReleased, error) {
	if f.State != ExamFormStateExposed {
		return nil, ExamResultReleased{}, ErrExamFormNotExposed
	}
	if f.Cut == nil {
		return nil, ExamResultReleased{}, ErrExamFormNoCutScore
	}
	res, err := NewExamResult(NewExamResultInput{
		TenantID:     f.TenantID,
		ExamID:       f.ExamID,
		ExamFormID:   f.ID,
		CandidateRef: candidateRef,
		Cut:          *f.Cut,
		RawScore:     rawScore,
	})
	if err != nil {
		return nil, ExamResultReleased{}, err
	}
	released := ExamResultReleased{
		ResultID:     res.ID,
		TenantID:     res.TenantID,
		ExamID:       res.ExamID,
		ExamFormID:   res.ExamFormID,
		CandidateRef: res.CandidateRef,
		RawScore:     res.RawScore,
		MaxScore:     res.MaxScore,
		PassMark:     f.Cut.PassMark(),
		Outcome:      res.Outcome,
		OccurredAt:   res.ScoredAt,
	}
	return res, released, nil
}
