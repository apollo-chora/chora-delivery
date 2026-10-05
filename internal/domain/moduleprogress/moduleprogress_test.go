// moduleprogress_test.go — unit tests for the StudentModuleProgress aggregate:
// a per-learner (GCID), per-module completion projection in the Content Delivery
// domain. As a learner completes the content items grouped into a Module,
// RecordItemCompletion advances the completed set and recomputes completion
// against the Module's CURRENT items + Requirement (reusing
// module.ModuleRequirement.IsSatisfiedBy).
//
// Black-box (package moduleprogress_test) so the tests exercise the real public
// surface the pg adapter + subscriber depend on.
package moduleprogress_test

import (
	"errors"
	"testing"

	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

const (
	tTenant = "01970000-0000-7000-8000-000000000001"
	tCourse = "01970000-0000-7000-8000-000000000099"
	tGCID   = "01970000-0000-7000-8000-000000000abc"
	cidA    = "01970000-0000-7000-9000-0000000000a1"
	cidB    = "01970000-0000-7000-9000-0000000000a2"
	cidC    = "01970000-0000-7000-9000-0000000000a3"
)

// buildModule constructs a module with the given content-item ids appended in
// order, returning the module and the minted ModuleItem content-item ids (which
// equal the ids passed, since AddItem stores ContentItemID verbatim).
func buildModule(t *testing.T, ids ...string) *module.Module {
	t.Helper()
	m, err := module.New(module.NewParams{TenantID: tTenant, CourseID: tCourse, Title: "M1"})
	if err != nil {
		t.Fatalf("module.New: %v", err)
	}
	for _, id := range ids {
		if _, err := m.AddItem(id); err != nil {
			t.Fatalf("AddItem(%s): %v", id, err)
		}
	}
	return m
}

func newProgress(t *testing.T, m *module.Module) *moduleprogress.StudentModuleProgress {
	t.Helper()
	p, err := moduleprogress.New(moduleprogress.NewParams{
		TenantID: tTenant, GCID: tGCID, ModuleID: m.ID, CourseID: tCourse,
	})
	if err != nil {
		t.Fatalf("moduleprogress.New: %v", err)
	}
	return p
}

// -----------------------------------------------------------------------------
// New
// -----------------------------------------------------------------------------

func TestNew_Valid_SeedsDefaults(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA)
	p := newProgress(t, m)
	if p.ID == "" {
		t.Fatalf("New: expected a minted UUIDv7 ID")
	}
	if p.TenantID != tTenant || p.GCID != tGCID || p.ModuleID != m.ID || p.CourseID != tCourse {
		t.Fatalf("New: field mismatch: %+v", p)
	}
	if p.IsComplete {
		t.Fatalf("New: fresh progress must be incomplete")
	}
	if len(p.CompletedContentItemIDs) != 0 {
		t.Fatalf("New: expected empty completed set; got %d", len(p.CompletedContentItemIDs))
	}
	if p.CompletedAt != nil {
		t.Fatalf("New: completed_at must be nil until complete")
	}
	if !p.IsActive() {
		t.Fatalf("New: fresh progress must be active")
	}
}

func TestNew_Invalid(t *testing.T) {
	t.Parallel()
	base := moduleprogress.NewParams{TenantID: tTenant, GCID: tGCID, ModuleID: "01970000-0000-7000-8000-000000000042", CourseID: tCourse}
	cases := map[string]func(p *moduleprogress.NewParams){
		"empty tenant": func(p *moduleprogress.NewParams) { p.TenantID = "  " },
		"empty gcid":   func(p *moduleprogress.NewParams) { p.GCID = "" },
		"empty module": func(p *moduleprogress.NewParams) { p.ModuleID = "" },
		"empty course": func(p *moduleprogress.NewParams) { p.CourseID = " " },
	}
	for name, mut := range cases {
		p := base
		mut(&p)
		if _, err := moduleprogress.New(p); !errors.Is(err, moduleprogress.ErrInvalidArgument) {
			t.Errorf("%s: expected ErrInvalidArgument; got %v", name, err)
		}
	}
}

// -----------------------------------------------------------------------------
// RecordItemCompletion — all_items
// -----------------------------------------------------------------------------

func TestRecordItemCompletion_AllItems_AdvancesThenCompletes(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA, cidB) // default requirement = all_items
	p := newProgress(t, m)

	changed, err := p.RecordItemCompletion(cidA, m)
	if err != nil {
		t.Fatalf("record cidA: %v", err)
	}
	if !changed {
		t.Fatalf("first completion must report changed")
	}
	if p.IsComplete {
		t.Fatalf("all_items must stay incomplete after 1 of 2")
	}
	if len(p.CompletedContentItemIDs) != 1 {
		t.Fatalf("expected 1 completed; got %d", len(p.CompletedContentItemIDs))
	}
	if p.CompletedAt != nil {
		t.Fatalf("completed_at must be nil while incomplete")
	}

	changed, err = p.RecordItemCompletion(cidB, m)
	if err != nil {
		t.Fatalf("record cidB: %v", err)
	}
	if !changed {
		t.Fatalf("completing the module must report changed")
	}
	if !p.IsComplete {
		t.Fatalf("all_items must be complete after 2 of 2")
	}
	if p.CompletedAt == nil {
		t.Fatalf("completed_at must be stamped on completion")
	}
}

// -----------------------------------------------------------------------------
// RecordItemCompletion — n_of_m
// -----------------------------------------------------------------------------

func TestRecordItemCompletion_NOfM_CompletesAtThreshold(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA, cidB, cidC)
	if err := m.SetRequirement(module.ModuleRequirement{Kind: module.RequirementNOfM, ThresholdN: 2}); err != nil {
		t.Fatalf("SetRequirement: %v", err)
	}
	p := newProgress(t, m)

	if _, err := p.RecordItemCompletion(cidA, m); err != nil {
		t.Fatalf("record cidA: %v", err)
	}
	if p.IsComplete {
		t.Fatalf("n_of_m(2) must be incomplete after 1")
	}
	if _, err := p.RecordItemCompletion(cidB, m); err != nil {
		t.Fatalf("record cidB: %v", err)
	}
	if !p.IsComplete {
		t.Fatalf("n_of_m(2) must be complete after 2")
	}
}

// -----------------------------------------------------------------------------
// RecordItemCompletion — specific_items
// -----------------------------------------------------------------------------

func TestRecordItemCompletion_SpecificItems_RequiresNamedSubset(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA, cidB, cidC)
	if err := m.SetRequirement(module.ModuleRequirement{Kind: module.RequirementSpecificItems, RequiredItemIDs: []string{cidA, cidC}}); err != nil {
		t.Fatalf("SetRequirement: %v", err)
	}
	p := newProgress(t, m)

	// Completing a non-required item does not complete the module.
	if _, err := p.RecordItemCompletion(cidB, m); err != nil {
		t.Fatalf("record cidB: %v", err)
	}
	if p.IsComplete {
		t.Fatalf("specific_items must stay incomplete without all required")
	}
	if _, err := p.RecordItemCompletion(cidA, m); err != nil {
		t.Fatalf("record cidA: %v", err)
	}
	if p.IsComplete {
		t.Fatalf("still missing cidC")
	}
	if _, err := p.RecordItemCompletion(cidC, m); err != nil {
		t.Fatalf("record cidC: %v", err)
	}
	if !p.IsComplete {
		t.Fatalf("specific_items complete once all required done")
	}
}

// -----------------------------------------------------------------------------
// Idempotency + guards
// -----------------------------------------------------------------------------

func TestRecordItemCompletion_Idempotent(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA, cidB)
	p := newProgress(t, m)

	if _, err := p.RecordItemCompletion(cidA, m); err != nil {
		t.Fatalf("record cidA: %v", err)
	}
	changed, err := p.RecordItemCompletion(cidA, m)
	if err != nil {
		t.Fatalf("re-record cidA: %v", err)
	}
	if changed {
		t.Fatalf("re-recording the same item must be a no-op (changed=false)")
	}
	if len(p.CompletedContentItemIDs) != 1 {
		t.Fatalf("idempotent record must not double-count; got %d", len(p.CompletedContentItemIDs))
	}
}

func TestRecordItemCompletion_ItemNotInModule_Errors(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA)
	p := newProgress(t, m)
	if _, err := p.RecordItemCompletion(cidB, m); !errors.Is(err, moduleprogress.ErrItemNotInModule) {
		t.Fatalf("expected ErrItemNotInModule; got %v", err)
	}
	if len(p.CompletedContentItemIDs) != 0 {
		t.Fatalf("a rejected completion must not mutate state")
	}
}

func TestRecordItemCompletion_ModuleMismatch_Errors(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA)
	other := buildModule(t, cidA) // different module id
	p := newProgress(t, m)
	if _, err := p.RecordItemCompletion(cidA, other); !errors.Is(err, moduleprogress.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument on module id mismatch; got %v", err)
	}
}

func TestRecordItemCompletion_TenantMismatch_Errors(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA)
	m.TenantID = "01970000-0000-7000-8000-0000000000ff" // module of a foreign tenant
	p := newProgress(t, m)
	p.TenantID = tTenant // progress belongs to tTenant
	if _, err := p.RecordItemCompletion(cidA, m); !errors.Is(err, moduleprogress.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument on tenant mismatch; got %v", err)
	}
}

// When the module gains items after the learner was already complete, the
// completion bar rises above the learner's set: recording another (now
// insufficient) item must flip IsComplete back to false and clear completed_at
// rather than leave a stale "complete".
func TestRecordItemCompletion_BarRises_RecomputesIncomplete(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA) // all_items over {cidA}
	p := newProgress(t, m)
	if _, err := p.RecordItemCompletion(cidA, m); err != nil {
		t.Fatalf("record cidA: %v", err)
	}
	if !p.IsComplete {
		t.Fatalf("precondition: single-item all_items must be complete")
	}

	// Instructor grows the module: now {cidA, cidB, cidC}.
	if _, err := m.AddItem(cidB); err != nil {
		t.Fatalf("AddItem cidB: %v", err)
	}
	if _, err := m.AddItem(cidC); err != nil {
		t.Fatalf("AddItem cidC: %v", err)
	}
	changed, err := p.RecordItemCompletion(cidB, m)
	if err != nil {
		t.Fatalf("record cidB: %v", err)
	}
	if !changed {
		t.Fatalf("recompute crossing the raised bar must report changed")
	}
	if p.IsComplete {
		t.Fatalf("all_items over the grown module must no longer be complete")
	}
	if p.CompletedAt != nil {
		t.Fatalf("completed_at must be cleared when it drops back to incomplete")
	}
}

func TestRecordItemCompletion_OnDeleted_Errors(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA)
	p := newProgress(t, m)
	p.SoftDelete()
	if _, err := p.RecordItemCompletion(cidA, m); !errors.Is(err, moduleprogress.ErrDeleted) {
		t.Fatalf("expected ErrDeleted; got %v", err)
	}
}

func TestSoftDelete_Idempotent(t *testing.T) {
	t.Parallel()
	m := buildModule(t, cidA)
	p := newProgress(t, m)
	p.SoftDelete()
	if p.IsActive() {
		t.Fatalf("SoftDelete must deactivate")
	}
	first := *p.DeletedAt
	p.SoftDelete()
	if *p.DeletedAt != first {
		t.Fatalf("SoftDelete must be idempotent (deleted_at unchanged)")
	}
}
