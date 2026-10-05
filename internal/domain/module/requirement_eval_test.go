// requirement_eval_test.go — unit tests for ModuleRequirement.IsSatisfiedBy, the
// pure completion-evaluator a StudentModuleProgress projection uses to decide
// whether a learner's set of completed content-item ids satisfies a module's
// completion Requirement. Black-box (package module_test).
//
// Semantics under test (mirror the three RequirementKind rules):
//   - all_items      : NON-EMPTY module AND every active item completed;
//   - n_of_m         : at least ThresholdN of the module's CURRENT items completed
//     (completions for ids no longer a member do not count);
//   - specific_items : every RequiredItemID completed.
package module_test

import (
	"testing"

	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
)

// items builds a slice of *ModuleItem from content-item ids (only ContentItemID
// matters to the evaluator).
func items(ids ...string) []*module.ModuleItem {
	out := make([]*module.ModuleItem, 0, len(ids))
	for _, id := range ids {
		out = append(out, &module.ModuleItem{ContentItemID: id})
	}
	return out
}

// completedSet builds the completed-id membership map.
func completedSet(ids ...string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func TestIsSatisfiedBy_AllItems(t *testing.T) {
	t.Parallel()
	req := module.ModuleRequirement{Kind: module.RequirementAllItems}
	mods := items(cid1, cid2, cid3)

	cases := map[string]struct {
		completed map[string]bool
		want      bool
	}{
		"none complete":    {completedSet(), false},
		"partial complete": {completedSet(cid1, cid2), false},
		"all complete":     {completedSet(cid1, cid2, cid3), true},
		"superset":         {completedSet(cid1, cid2, cid3, "01970000-0000-7000-9000-0000000000ff"), true},
	}
	for name, tc := range cases {
		if got := req.IsSatisfiedBy(mods, tc.completed); got != tc.want {
			t.Errorf("%s: IsSatisfiedBy = %v, want %v", name, got, tc.want)
		}
	}
}

func TestIsSatisfiedBy_AllItems_EmptyModuleIsNeverComplete(t *testing.T) {
	t.Parallel()
	req := module.ModuleRequirement{Kind: module.RequirementAllItems}
	if req.IsSatisfiedBy(nil, completedSet()) {
		t.Fatalf("all_items over an empty module must be incomplete (nothing achieved)")
	}
}

func TestIsSatisfiedBy_NOfM(t *testing.T) {
	t.Parallel()
	req := module.ModuleRequirement{Kind: module.RequirementNOfM, ThresholdN: 2}
	mods := items(cid1, cid2, cid3)

	cases := map[string]struct {
		completed map[string]bool
		want      bool
	}{
		"below threshold":           {completedSet(cid1), false},
		"exactly threshold":         {completedSet(cid1, cid2), true},
		"above threshold":           {completedSet(cid1, cid2, cid3), true},
		"non-member does not count": {completedSet(cid1, "01970000-0000-7000-9000-0000000000ff"), false},
	}
	for name, tc := range cases {
		if got := req.IsSatisfiedBy(mods, tc.completed); got != tc.want {
			t.Errorf("%s: IsSatisfiedBy = %v, want %v", name, got, tc.want)
		}
	}
}

func TestIsSatisfiedBy_SpecificItems(t *testing.T) {
	t.Parallel()
	req := module.ModuleRequirement{Kind: module.RequirementSpecificItems, RequiredItemIDs: []string{cid1, cid3}}
	mods := items(cid1, cid2, cid3)

	cases := map[string]struct {
		completed map[string]bool
		want      bool
	}{
		"missing one required":  {completedSet(cid1, cid2), false},
		"all required complete": {completedSet(cid1, cid3), true},
		"required + extra":      {completedSet(cid1, cid2, cid3), true},
	}
	for name, tc := range cases {
		if got := req.IsSatisfiedBy(mods, tc.completed); got != tc.want {
			t.Errorf("%s: IsSatisfiedBy = %v, want %v", name, got, tc.want)
		}
	}
}

func TestIsSatisfiedBy_UnknownKind_False(t *testing.T) {
	t.Parallel()
	req := module.ModuleRequirement{Kind: module.RequirementKind("bogus")}
	if req.IsSatisfiedBy(items(cid1), completedSet(cid1)) {
		t.Fatalf("unknown requirement kind must never report satisfied")
	}
}
