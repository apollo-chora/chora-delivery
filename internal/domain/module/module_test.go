// module_test.go — unit tests for the Module curriculum-structure aggregate.
//
// Mirrors the course_content aggregate test style: table-driven invariant
// coverage for ordering denseness, (content-item) uniqueness, cascade
// soft-delete stopping at the aggregate boundary, and completion-requirement
// validity. Black-box (package module_test) so the tests exercise the real
// public surface the pg adapter + InMem store depend on.
package module_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
)

// uuidN formats a valid, distinct UUID string from an int — used to fill a
// module to its item cap without hand-writing hundreds of literals.
func uuidN(i int) string {
	return fmt.Sprintf("01970000-0000-7000-8000-%012x", i)
}

const (
	tTenant = "01970000-0000-7000-8000-000000000001"
	tCourse = "01970000-0000-7000-8000-000000000099"
	cid1    = "01970000-0000-7000-9000-0000000000a1"
	cid2    = "01970000-0000-7000-9000-0000000000a2"
	cid3    = "01970000-0000-7000-9000-0000000000a3"
)

func newModule(t *testing.T) *module.Module {
	t.Helper()
	m, err := module.New(module.NewParams{TenantID: tTenant, CourseID: tCourse, Title: "Fractions"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

// -----------------------------------------------------------------------------
// New
// -----------------------------------------------------------------------------

func TestNew_Valid_SeedsDefaults(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	if m.ID == "" {
		t.Fatalf("New: expected a minted UUIDv7 ID")
	}
	if m.TenantID != tTenant || m.CourseID != tCourse || m.Title != "Fractions" {
		t.Fatalf("New: field mismatch: %+v", m)
	}
	if m.Requirement.Kind != module.RequirementAllItems {
		t.Fatalf("New: default requirement must be all_items; got %q", m.Requirement.Kind)
	}
	if len(m.Items) != 0 {
		t.Fatalf("New: expected empty items; got %d", len(m.Items))
	}
	if !m.IsActive() {
		t.Fatalf("New: fresh module must be active")
	}
}

func TestNew_Invalid(t *testing.T) {
	t.Parallel()
	cases := map[string]module.NewParams{
		"empty tenant": {TenantID: "  ", CourseID: tCourse, Title: "x"},
		"empty course": {TenantID: tTenant, CourseID: "", Title: "x"},
		"empty title":  {TenantID: tTenant, CourseID: tCourse, Title: "   "},
		"long title":   {TenantID: tTenant, CourseID: tCourse, Title: strings.Repeat("a", module.MaxTitleLength+1)},
	}
	for name, p := range cases {
		p := p
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := module.New(p); !errors.Is(err, module.ErrInvalidArgument) {
				t.Fatalf("expected ErrInvalidArgument; got %v", err)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// AddItem — append, dense positions, uniqueness, cap, ref shape
// -----------------------------------------------------------------------------

func TestAddItem_AppendsDenselyAndReturnsItem(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	it1, err := m.AddItem(cid1)
	if err != nil {
		t.Fatalf("AddItem 1: %v", err)
	}
	it2, err := m.AddItem(cid2)
	if err != nil {
		t.Fatalf("AddItem 2: %v", err)
	}
	if it1.Position != 0 || it2.Position != 1 {
		t.Fatalf("positions must be dense 0,1; got %d,%d", it1.Position, it2.Position)
	}
	if it1.ModuleID != m.ID || it2.ModuleID != m.ID {
		t.Fatalf("items must carry the module id")
	}
	if it1.ContentItemID != cid1 || it2.ContentItemID != cid2 {
		t.Fatalf("content_item_id mismatch")
	}
	if it1.ID == it2.ID || it1.ID == "" {
		t.Fatalf("each item needs a distinct minted id")
	}
	if len(m.Items) != 2 {
		t.Fatalf("expected 2 items; got %d", len(m.Items))
	}
}

func TestAddItem_RejectsDuplicateContentItem(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	if _, err := m.AddItem(cid1); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if _, err := m.AddItem(cid1); !errors.Is(err, module.ErrDuplicateItem) {
		t.Fatalf("expected ErrDuplicateItem; got %v", err)
	}
}

func TestAddItem_RejectsBadRef(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	for name, ref := range map[string]string{"empty": "  ", "not-uuid": "not-a-uuid"} {
		ref := ref
		t.Run(name, func(t *testing.T) {
			if _, err := m.AddItem(ref); !errors.Is(err, module.ErrInvalidArgument) {
				t.Fatalf("expected ErrInvalidArgument; got %v", err)
			}
		})
	}
}

func TestAddItem_CapExceeded(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	// Fill to the cap with synthetic-but-valid UUIDs.
	for i := 0; i < module.MaxItemsPerModule; i++ {
		if _, err := m.AddItem(uuidN(i)); err != nil {
			t.Fatalf("AddItem %d: %v", i, err)
		}
	}
	if _, err := m.AddItem(cid1); !errors.Is(err, module.ErrCapExceeded) {
		t.Fatalf("expected ErrCapExceeded; got %v", err)
	}
}

func TestAddItem_OnDeletedModule(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	m.SoftDelete()
	if _, err := m.AddItem(cid1); !errors.Is(err, module.ErrDeleted) {
		t.Fatalf("expected ErrDeleted; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// RemoveItem — soft-delete + dense re-compaction; requirement consistency
// -----------------------------------------------------------------------------

func TestRemoveItem_RemovesAndRedensifies(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	it0, _ := m.AddItem(cid1)
	it1, _ := m.AddItem(cid2)
	it2, _ := m.AddItem(cid3)
	_ = it0
	_ = it2

	removed, err := m.RemoveItem(it1.ID)
	if err != nil {
		t.Fatalf("RemoveItem: %v", err)
	}
	if removed.ID != it1.ID || removed.DeletedAt == nil {
		t.Fatalf("removed item must be soft-deleted; got %+v", removed)
	}
	if removed.Position != 1 {
		t.Fatalf("removed item must retain its pre-removal position (1); got %d", removed.Position)
	}
	if len(m.Items) != 2 {
		t.Fatalf("expected 2 remaining; got %d", len(m.Items))
	}
	if m.Items[0].Position != 0 || m.Items[1].Position != 1 {
		t.Fatalf("remaining positions must be dense 0,1; got %d,%d", m.Items[0].Position, m.Items[1].Position)
	}
	if m.Items[0].ContentItemID != cid1 || m.Items[1].ContentItemID != cid3 {
		t.Fatalf("remaining order must be cid1,cid3; got %s,%s", m.Items[0].ContentItemID, m.Items[1].ContentItemID)
	}
}

func TestRemoveItem_NotFound(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	if _, err := m.RemoveItem("nope"); !errors.Is(err, module.ErrItemNotFound) {
		t.Fatalf("expected ErrItemNotFound; got %v", err)
	}
}

func TestRemoveItem_RefusesWhenItWouldBreakNOfM(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	m.AddItem(cid1)
	m.AddItem(cid2)
	if err := m.SetRequirement(module.ModuleRequirement{Kind: module.RequirementNOfM, ThresholdN: 2}); err != nil {
		t.Fatalf("SetRequirement: %v", err)
	}
	// Removing an item would drop the count to 1 < threshold 2 → refuse.
	if _, err := m.RemoveItem(m.Items[0].ID); !errors.Is(err, module.ErrRequirementViolation) {
		t.Fatalf("expected ErrRequirementViolation; got %v", err)
	}
	if len(m.Items) != 2 {
		t.Fatalf("refused remove must not mutate items; got %d", len(m.Items))
	}
}

func TestRemoveItem_RefusesWhenItWouldOrphanSpecificRequirement(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	m.AddItem(cid1)
	m.AddItem(cid2)
	if err := m.SetRequirement(module.ModuleRequirement{Kind: module.RequirementSpecificItems, RequiredItemIDs: []string{cid1}}); err != nil {
		t.Fatalf("SetRequirement: %v", err)
	}
	// cid1 is required → removing it must be refused.
	if _, err := m.RemoveItem(m.Items[0].ID); !errors.Is(err, module.ErrRequirementViolation) {
		t.Fatalf("expected ErrRequirementViolation; got %v", err)
	}
	// A non-required item (cid2) may still be removed.
	if _, err := m.RemoveItem(m.Items[1].ID); err != nil {
		t.Fatalf("removing a non-required item should succeed; got %v", err)
	}
}

func TestRemoveItem_OnDeletedModule(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	it, _ := m.AddItem(cid1)
	m.SoftDelete()
	if _, err := m.RemoveItem(it.ID); !errors.Is(err, module.ErrDeleted) {
		t.Fatalf("expected ErrDeleted; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ReorderItems — explicit permutation
// -----------------------------------------------------------------------------

func TestReorderItems_AppliesPermutation(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	a, _ := m.AddItem(cid1)
	b, _ := m.AddItem(cid2)
	c, _ := m.AddItem(cid3)

	if err := m.ReorderItems([]string{c.ID, a.ID, b.ID}); err != nil {
		t.Fatalf("ReorderItems: %v", err)
	}
	want := []string{cid3, cid1, cid2}
	for i, it := range m.Items {
		if it.ContentItemID != want[i] || it.Position != i {
			t.Fatalf("pos %d: want %s@%d; got %s@%d", i, want[i], i, it.ContentItemID, it.Position)
		}
	}
}

func TestReorderItems_Invalid(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	a, _ := m.AddItem(cid1)
	b, _ := m.AddItem(cid2)
	cases := map[string][]string{
		"wrong length": {a.ID},
		"duplicate id": {a.ID, a.ID},
		"unknown id":   {a.ID, "ghost"},
	}
	for name, order := range cases {
		order := order
		t.Run(name, func(t *testing.T) {
			if err := m.ReorderItems(order); !errors.Is(err, module.ErrInvalidReorder) {
				t.Fatalf("expected ErrInvalidReorder; got %v", err)
			}
		})
	}
	_ = b
}

func TestReorderItems_OnDeletedModule(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	a, _ := m.AddItem(cid1)
	m.SoftDelete()
	if err := m.ReorderItems([]string{a.ID}); !errors.Is(err, module.ErrDeleted) {
		t.Fatalf("expected ErrDeleted; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// SetRequirement + ModuleRequirement.Validate
// -----------------------------------------------------------------------------

func TestSetRequirement_Valid(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	m.AddItem(cid1)
	m.AddItem(cid2)

	valid := []module.ModuleRequirement{
		{Kind: module.RequirementAllItems},
		{Kind: module.RequirementNOfM, ThresholdN: 1},
		{Kind: module.RequirementNOfM, ThresholdN: 2},
		{Kind: module.RequirementSpecificItems, RequiredItemIDs: []string{cid1, cid2}},
	}
	for _, req := range valid {
		if err := m.SetRequirement(req); err != nil {
			t.Fatalf("SetRequirement(%+v): %v", req, err)
		}
		if m.Requirement.Kind != req.Kind {
			t.Fatalf("requirement not persisted: %+v", m.Requirement)
		}
	}
}

func TestSetRequirement_Invalid(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	m.AddItem(cid1)
	m.AddItem(cid2)

	invalid := []module.ModuleRequirement{
		{Kind: "bogus"},
		{Kind: module.RequirementAllItems, ThresholdN: 1},                                       // all_items must not set threshold
		{Kind: module.RequirementAllItems, RequiredItemIDs: []string{cid1}},                     // nor required ids
		{Kind: module.RequirementNOfM, ThresholdN: 0},                                           // 0 not allowed
		{Kind: module.RequirementNOfM, ThresholdN: 3},                                           // > item count(2)
		{Kind: module.RequirementNOfM, ThresholdN: 1, RequiredItemIDs: []string{cid1}},          // n_of_m must not set ids
		{Kind: module.RequirementSpecificItems},                                                 // needs >=1 id
		{Kind: module.RequirementSpecificItems, ThresholdN: 1, RequiredItemIDs: []string{cid1}}, // must not set threshold
		{Kind: module.RequirementSpecificItems, RequiredItemIDs: []string{cid3}},                // cid3 not in module
		{Kind: module.RequirementSpecificItems, RequiredItemIDs: []string{cid1, cid1}},          // dup
	}
	for i, req := range invalid {
		req := req
		if err := m.SetRequirement(req); !errors.Is(err, module.ErrInvalidRequirement) {
			t.Fatalf("case %d %+v: expected ErrInvalidRequirement; got %v", i, req, err)
		}
	}
	// A rejected requirement must not overwrite the prior valid one.
	if m.Requirement.Kind != module.RequirementAllItems {
		t.Fatalf("rejected SetRequirement must not mutate; got %+v", m.Requirement)
	}
}

func TestSetRequirement_OnDeletedModule(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	m.SoftDelete()
	if err := m.SetRequirement(module.DefaultRequirement()); !errors.Is(err, module.ErrDeleted) {
		t.Fatalf("expected ErrDeleted; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// SoftDelete — cascade to items, never beyond the aggregate; idempotent
// -----------------------------------------------------------------------------

func TestSoftDelete_CascadesToItemsOnly(t *testing.T) {
	t.Parallel()
	m := newModule(t)
	it0, _ := m.AddItem(cid1)
	it1, _ := m.AddItem(cid2)

	m.SoftDelete()
	if m.IsActive() || m.DeletedAt == nil {
		t.Fatalf("module must be soft-deleted")
	}
	for _, it := range []*module.ModuleItem{it0, it1} {
		if it.DeletedAt == nil {
			t.Fatalf("item %s must be cascade-soft-deleted", it.ID)
		}
		// The cascade never rewrites the cross-aggregate content reference.
		if it.ContentItemID == "" {
			t.Fatalf("cascade must not clear the ContentItemID (cross-aggregate ref)")
		}
	}
	// Idempotent — a second call is a no-op and preserves the first stamp.
	firstStamp := *m.DeletedAt
	m.SoftDelete()
	if !m.DeletedAt.Equal(firstStamp) {
		t.Fatalf("SoftDelete must be idempotent")
	}
}
