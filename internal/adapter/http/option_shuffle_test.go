package httpapi

import (
	"reflect"
	"sort"
	"testing"
)

func optIDs(rows []map[string]interface{}) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		id, _ := r["option_id"].(string)
		out = append(out, id)
	}
	return out
}

func fourOpts() []map[string]interface{} {
	return []map[string]interface{}{
		{"option_id": "opt_1", "label": "A", "is_correct": true},
		{"option_id": "opt_2", "label": "B", "is_correct": false},
		{"option_id": "opt_3", "label": "C", "is_correct": false},
		{"option_id": "opt_4", "label": "D", "is_correct": false},
	}
}

func TestOptionShuffleSeed_EmptyWhenEitherPartMissing(t *testing.T) {
	if optionShuffleSeed("", "tsq") != "" {
		t.Fatal("empty gcid should yield empty seed")
	}
	if optionShuffleSeed("gcid", "") != "" {
		t.Fatal("empty tsq should yield empty seed")
	}
	if optionShuffleSeed("gcid", "tsq") != "gcid:tsq" {
		t.Fatalf("unexpected seed: %q", optionShuffleSeed("gcid", "tsq"))
	}
}

func TestShuffleOptionRows_DeterministicForSameSeed(t *testing.T) {
	a := fourOpts()
	b := fourOpts()
	seed := optionShuffleSeed("gcid-123", "tsq-1")
	shuffleOptionRows(a, seed)
	shuffleOptionRows(b, seed)
	if !reflect.DeepEqual(optIDs(a), optIDs(b)) {
		t.Fatalf("same seed must produce same order: %v vs %v", optIDs(a), optIDs(b))
	}
}

func TestShuffleOptionRows_PreservesTheOptionSet(t *testing.T) {
	rows := fourOpts()
	shuffleOptionRows(rows, optionShuffleSeed("g", "q"))
	got := optIDs(rows)
	sort.Strings(got)
	want := []string{"opt_1", "opt_2", "opt_3", "opt_4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("option set changed: %v", got)
	}
	// the correct flag still travels with opt_1
	for _, r := range rows {
		if r["option_id"] == "opt_1" {
			if c, _ := r["is_correct"].(bool); !c {
				t.Fatal("is_correct lost from opt_1 after shuffle")
			}
		}
	}
}

func TestShuffleOptionRows_DiffersAcrossLearners(t *testing.T) {
	// Not guaranteed for every pair, but for these two seeds the 4! space makes
	// an identical permutation unlikely; assert at least one differs to prove
	// the seed actually drives the order (anti-cheat property).
	a := fourOpts()
	b := fourOpts()
	shuffleOptionRows(a, optionShuffleSeed("learner-A", "tsq-1"))
	shuffleOptionRows(b, optionShuffleSeed("learner-B", "tsq-1"))
	if reflect.DeepEqual(optIDs(a), optIDs(b)) {
		// extremely unlikely collision — try another question key to confirm
		c := fourOpts()
		d := fourOpts()
		shuffleOptionRows(c, optionShuffleSeed("learner-A", "tsq-2"))
		shuffleOptionRows(d, optionShuffleSeed("learner-B", "tsq-2"))
		if reflect.DeepEqual(optIDs(c), optIDs(d)) {
			t.Fatal("different learner seeds produced identical order for two questions")
		}
	}
}

func TestShuffleOptionRows_NoopGuards(t *testing.T) {
	one := []map[string]interface{}{{"option_id": "opt_1"}}
	shuffleOptionRows(one, "g:q") // <2 rows → no panic, no change
	if optIDs(one)[0] != "opt_1" {
		t.Fatal("single-row slice must be untouched")
	}
	rows := fourOpts()
	before := optIDs(rows)
	shuffleOptionRows(rows, "") // empty seed → no-op
	if !reflect.DeepEqual(before, optIDs(rows)) {
		t.Fatal("empty seed must be a no-op")
	}
}
