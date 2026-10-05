// result_ordering_test.go — the learner result projection (myResultHandler)
// and the OE-grading event emitter must present per-question rows in the
// test-set's display_order, NOT the submission-storage order of sub.Answers
// (which reflects answer/merge order). Regression for the 2026-06-20 finding:
// with shuffle_questions=false, Q1 surfaced at result position 7 because perQ
// was built by `range sub.Answers`.
package httpapi

import (
	"testing"
)

func TestOrderRowsByDisplayOrder_SortsByTestSetDisplayOrder(t *testing.T) {
	// Rows arrive in submission-storage (scrambled) order.
	rows := []map[string]interface{}{
		{"test_set_question_id": "tsq-2"},
		{"test_set_question_id": "tsq-3"},
		{"test_set_question_id": "tsq-1"}, // display_order 1 stored last
	}
	displayOrder := map[string]int{"tsq-1": 1, "tsq-2": 2, "tsq-3": 3}

	orderRowsByDisplayOrder(rows, displayOrder)

	got := []string{
		rows[0]["test_set_question_id"].(string),
		rows[1]["test_set_question_id"].(string),
		rows[2]["test_set_question_id"].(string),
	}
	want := []string{"tsq-1", "tsq-2", "tsq-3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: want %s, got %s (full=%v)", i, want[i], got[i], got)
		}
	}
}

func TestOrderRowsByDisplayOrder_UnknownIDsSortLastStable(t *testing.T) {
	// tsqids absent from the display-order map (e.g. snapshot lookup gap)
	// must sort AFTER all known ones, preserving their relative input order.
	rows := []map[string]interface{}{
		{"test_set_question_id": "unknown-a"},
		{"test_set_question_id": "tsq-2"},
		{"test_set_question_id": "unknown-b"},
		{"test_set_question_id": "tsq-1"},
	}
	displayOrder := map[string]int{"tsq-1": 1, "tsq-2": 2}

	orderRowsByDisplayOrder(rows, displayOrder)

	got := []string{
		rows[0]["test_set_question_id"].(string),
		rows[1]["test_set_question_id"].(string),
		rows[2]["test_set_question_id"].(string),
		rows[3]["test_set_question_id"].(string),
	}
	want := []string{"tsq-1", "tsq-2", "unknown-a", "unknown-b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: want %s, got %s (full=%v)", i, want[i], got[i], got)
		}
	}
}

func TestOrderRowsByDisplayOrder_NoopGuards(t *testing.T) {
	orderRowsByDisplayOrder(nil, map[string]int{"a": 1}) // must not panic
	rows := []map[string]interface{}{{"test_set_question_id": "x"}}
	orderRowsByDisplayOrder(rows, nil) // nil map → no reorder, no panic
	if rows[0]["test_set_question_id"] != "x" {
		t.Fatalf("nil map should leave rows untouched")
	}
}
