// coverage_test.go — closes the remaining statement gaps in the module
// aggregate + InMemModuleStore: the ContentItemIDs convenience getter, the
// IsSatisfiedBy fail-closed branches (n_of_m with a non-positive threshold and
// an unknown kind), the store-level SetRequirement delegation (happy, deleted,
// cross-tenant), and the ListByCourse position tie-break.
package module_test

import (
	"context"
	"errors"
	"testing"

	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
)

func TestContentItemIDs_EmptyAndPopulated(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	if got := m.ContentItemIDs(); len(got) != 0 {
		t.Fatalf("empty module: got %v want []", got)
	}
	it0, _ := m.AddItem(cid1)
	it1, _ := m.AddItem(cid2)
	got := m.ContentItemIDs()
	if len(got) != 2 || got[0] != it0.ContentItemID || got[1] != it1.ContentItemID {
		t.Fatalf("ContentItemIDs: got %v want [%s %s]", got, it0.ContentItemID, it1.ContentItemID)
	}
}

func TestIsSatisfiedBy_FailClosed(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	m.AddItem(cid1)
	m.AddItem(cid2)

	// Sanity: completing all items satisfies the default all_items rule.
	if !m.Requirement.IsSatisfiedBy(m.Items, map[string]bool{cid1: true, cid2: true}) {
		t.Fatalf("default all_items must be satisfied by completing all")
	}
	req := module.ModuleRequirement{Kind: module.RequirementNOfM, ThresholdN: 0}
	if req.IsSatisfiedBy(m.Items, map[string]bool{cid1: true, cid2: true}) {
		t.Fatalf("n_of_m threshold 0 must be fail-closed")
	}
	// specific_items with an empty id list is never satisfied.
	if (module.ModuleRequirement{Kind: module.RequirementSpecificItems}).IsSatisfiedBy(m.Items, map[string]bool{}) {
		t.Fatalf("specific_items with no required ids must be fail-closed")
	}
	// An unknown/malformed kind is never satisfied.
	if (module.ModuleRequirement{Kind: "bogus"}).IsSatisfiedBy(m.Items, map[string]bool{cid1: true, cid2: true}) {
		t.Fatalf("unknown kind must be fail-closed")
	}
}

func TestInMem_SetRequirement_Delegates(t *testing.T) {
	t.Parallel()
	s := module.NewInMemModuleStore()
	m := mustCreate(t, s, "Intro")
	if _, err := s.AddItem(context.Background(), tTenant, m.ID, cid1); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if err := s.SetRequirement(context.Background(), tTenant, m.ID,
		module.ModuleRequirement{Kind: module.RequirementNOfM, ThresholdN: 1}); err != nil {
		t.Fatalf("SetRequirement happy: %v", err)
	}
	got, _, _ := s.Get(context.Background(), m.ID)
	if got.Requirement.Kind != module.RequirementNOfM {
		t.Fatalf("requirement not persisted: %+v", got.Requirement)
	}

	const otherTenant = "01970000-0000-7000-8000-000000000002"
	if err := s.SetRequirement(context.Background(), otherTenant, m.ID, module.DefaultRequirement()); !errors.Is(err, module.ErrNotFound) {
		t.Fatalf("cross-tenant SetRequirement must miss; got %v", err)
	}
	if err := s.SoftDelete(context.Background(), tTenant, m.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if err := s.SetRequirement(context.Background(), tTenant, m.ID, module.DefaultRequirement()); !errors.Is(err, module.ErrNotFound) {
		t.Fatalf("SetRequirement on deleted module must miss; got %v", err)
	}

	// An invalid requirement fails loudly and leaves the module unchanged.
	m2 := mustCreate(t, s, "Core")
	if err := s.SetRequirement(context.Background(), tTenant, m2.ID,
		module.ModuleRequirement{Kind: module.RequirementNOfM, ThresholdN: 3}); !errors.Is(err, module.ErrInvalidRequirement) {
		t.Fatalf("invalid requirement: expected ErrInvalidRequirement; got %v", err)
	}
}

func TestInMem_ListByCourse_TieBreaksEqualPositionsByID(t *testing.T) {
	t.Parallel()
	s := module.NewInMemModuleStore()
	m0 := mustCreate(t, s, "Intro")
	m1 := mustCreate(t, s, "Core")
	// Force a position collision (dense assignment would never produce one) so
	// the sort comparator falls through to the ID tie-break.
	m1.Position = m0.Position
	got, err := s.ListByCourse(context.Background(), tTenant, tCourse)
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 modules; got %d", len(got))
	}
	if !(got[0].ID < got[1].ID) {
		t.Fatalf("equal positions must tie-break by ID ascending; got %s, %s", got[0].ID, got[1].ID)
	}
}
