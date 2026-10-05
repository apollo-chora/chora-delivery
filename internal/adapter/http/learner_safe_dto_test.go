// LEG3-D R5 — Snapshot content corruption (P0 demo blocker)
//
// White-box tests for `learnerSafeTestSetQuestionDTO` (question_id
// distinctness) and `projectLearnerSafeSnapshot` (stem passthrough +
// text fallback). Internal-package test on purpose — these helpers
// must not be re-exported for an external test.
//
// Source:
//   - docs/m13/cj1-manual-smoke-findings-2026-05-16.md §LEG3-D R5
//   - docs/m13/be-cj1-specifics-2026-05-16.md §LEG3-D R5
package httpapi

import (
	"encoding/json"
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// LEG3-D R3 Option B introduced distinct QuestionAtomID + QuestionID on
// TestSetQuestion. The DTO emitted to the FE picker + the /me/assessments
// runner MUST surface BOTH UUIDs distinctly. Pre-fix the handler was
// echoing QuestionAtomID for both fields — the conformance gate the FE
// runner uses to look up snapshots silently failed.
func TestLearnerSafeTestSetQuestionDTO_QuestionIDDistinctFromAtomID(t *testing.T) {
	q := &domain.TestSetQuestion{
		ID:             "tsq-1",
		TestSetID:      "ts-1",
		QuestionAtomID: "atom-aaaa-aaaa",
		QuestionID:     "qst-bbbb-bbbb",
		QuestionType:   "mcq",
		DisplayOrder:   1,
		Points:         10,
	}
	out := learnerSafeTestSetQuestionDTO(q)
	if got := out["question_atom_id"]; got != "atom-aaaa-aaaa" {
		t.Errorf("question_atom_id: want atom-aaaa-aaaa, got %v", got)
	}
	if got := out["question_id"]; got != "qst-bbbb-bbbb" {
		t.Errorf("question_id: want qst-bbbb-bbbb (distinct), got %v — DTO is still conflating atom_id and question_id", got)
	}
}

// When the payload_snapshot carries `stem` (LEG3-D R5 fix at
// QuestionClient.SnapshotQuestion merges proto Prompt → JSON.stem),
// projectLearnerSafeSnapshot MUST surface it verbatim to the FE.
func TestProjectLearnerSafeSnapshot_MCQ_PassesStemThrough(t *testing.T) {
	snap := `{"stem":"What gas do plants release as byproduct of photosynthesis?","options":[{"option_id":"opt-A","label":"Oxygen","is_correct":true,"explainer":"yes"}]}`
	out, ok := projectLearnerSafeSnapshot("mcq", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	stem, _ := out["stem"].(string)
	if stem != "What gas do plants release as byproduct of photosynthesis?" {
		t.Errorf("stem: want photosynthesis prompt, got %q", stem)
	}
}

// Per chora-contracts/openapi/creation-questions.yaml §LearnerSafeMCQPayload
// the wire contract requires both `label` and `text` on each option. The
// current chora-creation domain stores `label` only (existing production
// data has label = answer text, e.g. "Oxygen"). The projection MUST
// fall back to label-as-text when `text` is absent so the FE template
// renders option copy. New atoms that author a separate `text` field
// (future-work) take priority.
func TestProjectLearnerSafeSnapshot_MCQ_FallsBackLabelAsTextWhenTextAbsent(t *testing.T) {
	snap := `{"stem":"Q?","options":[{"option_id":"opt-A","label":"Oxygen"},{"option_id":"opt-B","label":"Carbon dioxide"}]}`
	out, ok := projectLearnerSafeSnapshot("mcq", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	optsRaw, _ := out["options"].([]map[string]interface{})
	if len(optsRaw) != 2 {
		t.Fatalf("options: want 2, got %d (out=%v)", len(optsRaw), out)
	}
	if got := optsRaw[0]["text"]; got != "Oxygen" {
		t.Errorf("options[0].text: want fallback to label 'Oxygen', got %v (full=%v)", got, optsRaw[0])
	}
	if got := optsRaw[1]["text"]; got != "Carbon dioxide" {
		t.Errorf("options[1].text: want fallback to label 'Carbon dioxide', got %v (full=%v)", got, optsRaw[1])
	}
	// Sanity — label survives untouched.
	if got := optsRaw[0]["label"]; got != "Oxygen" {
		t.Errorf("options[0].label: want passthrough 'Oxygen', got %v", got)
	}
}

func TestProjectLearnerSafeSnapshot_MCQ_TextPrefersExplicitFieldOverLabelFallback(t *testing.T) {
	snap := `{"stem":"Q?","options":[{"option_id":"opt-A","label":"A","text":"Oxygen"}]}`
	out, ok := projectLearnerSafeSnapshot("mcq", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	optsRaw, _ := out["options"].([]map[string]interface{})
	if len(optsRaw) != 1 {
		t.Fatalf("options: want 1, got %d", len(optsRaw))
	}
	if got := optsRaw[0]["label"]; got != "A" {
		t.Errorf("options[0].label: want 'A', got %v", got)
	}
	if got := optsRaw[0]["text"]; got != "Oxygen" {
		t.Errorf("options[0].text: want explicit 'Oxygen' (NOT fallback to label 'A'), got %v", got)
	}
}

// Guard: when label is empty AND text absent, the projection must NOT
// fabricate an empty `text` (would break "field absent" semantics for
// downstream consumers that distinguish missing vs empty).
func TestProjectLearnerSafeSnapshot_MCQ_EmptyLabelDoesNotFabricateText(t *testing.T) {
	snap := `{"options":[{"option_id":"opt-A","label":""}]}`
	out, ok := projectLearnerSafeSnapshot("mcq", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	optsRaw, _ := out["options"].([]map[string]interface{})
	if len(optsRaw) != 1 {
		t.Fatalf("options: want 1, got %d", len(optsRaw))
	}
	if _, hasText := optsRaw[0]["text"]; hasText {
		t.Errorf("options[0]: text should be absent when label is empty, got %v", optsRaw[0])
	}
}

// Smoke: ensure the projection still ignores non-JSON snapshots gracefully.
func TestProjectLearnerSafeSnapshot_InvalidJSON_ReturnsFalse(t *testing.T) {
	_, ok := projectLearnerSafeSnapshot("mcq", "{not valid")
	if ok {
		t.Errorf("want ok=false on invalid JSON")
	}
}

// Smoke: empty input — must return false (not panic).
func TestProjectLearnerSafeSnapshot_Empty_ReturnsFalse(t *testing.T) {
	_, ok := projectLearnerSafeSnapshot("mcq", "")
	if ok {
		t.Errorf("want ok=false on empty string")
	}
}

// Bench-style: ensure full wire-shape round-trip works.
// This is the canonical e2e shape FE expects to receive at /me/assessments.
func TestProjectLearnerSafeSnapshot_MCQ_FullPhotosynthesisShape(t *testing.T) {
	snap := `{"stem":"What gas do plants release as byproduct of photosynthesis?","options":[
		{"option_id":"ab6e63c2","label":"Oxygen","is_correct":true,"explainer":"yes"},
		{"option_id":"22e35bf0","label":"Carbon dioxide","is_correct":false,"explainer":"no"},
		{"option_id":"387cf345","label":"Nitrogen","is_correct":false,"explainer":"no"},
		{"option_id":"f8d517c1","label":"Methane","is_correct":false,"explainer":"no"}
	]}`
	out, ok := projectLearnerSafeSnapshot("mcq", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	// stem
	if stem, _ := out["stem"].(string); stem != "What gas do plants release as byproduct of photosynthesis?" {
		t.Errorf("stem: %q", stem)
	}
	// is_correct + explainer MUST be stripped (learner-safe projection)
	rendered, _ := json.Marshal(out)
	for _, leak := range []string{"is_correct", "explainer"} {
		if containsString(string(rendered), leak) {
			t.Errorf("learner-safe projection leaks %q: %s", leak, rendered)
		}
	}
	// Four options, each with label + fallback text from label.
	optsRaw, _ := out["options"].([]map[string]interface{})
	if len(optsRaw) != 4 {
		t.Fatalf("options: want 4, got %d", len(optsRaw))
	}
	expected := []string{"Oxygen", "Carbon dioxide", "Nitrogen", "Methane"}
	for i, want := range expected {
		if got := optsRaw[i]["label"]; got != want {
			t.Errorf("options[%d].label: want %q, got %v", i, want, got)
		}
		if got := optsRaw[i]["text"]; got != want {
			t.Errorf("options[%d].text: want fallback %q, got %v", i, want, got)
		}
	}
}

func containsString(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// -----------------------------------------------------------------------------
// ATOM-2 — projection split: pre-RELEASE LEARNER-SAFE vs post-RELEASE
// AUTHOR-SAFE.
//
// After B6 ships AUTHOR-SAFE SnapshotQuestionByID from chora-creation, the
// payload_snapshot column stores the FULL author shape — including
// `is_correct` + `explainer` (MCQ) and `model_answer` + `rubric` (OE).
// chora-delivery is the boundary that splits the projection at READ time:
//
//   - Pre-RELEASE (working canvas at GET /me/assessments/{id} +
//     GET /me/assessments/{id}/submissions/{id}): LEARNER-SAFE projection
//     — strip every grader-only field so a malicious learner cannot scrape
//     correct answers via the working-canvas API.
//
//   - Post-RELEASE (GET .../submissions/{id}/result on RELEASED state):
//     AUTHOR-SAFE projection — surface `is_correct` + `explainer` on each
//     MCQ option (so the learner can read WHY each option was right/wrong)
//     and `model_answer` + `rubric` on OE so the learner can self-assess
//     against the canonical answer key.
//
// Per chora-contracts/openapi/delivery-assessments.yaml §LearnerQuestionGrade
// (mcq_post_grade + oe_post_grade).
// -----------------------------------------------------------------------------

// LEARNER-SAFE MCQ MUST strip `is_correct` even when the snapshot contains
// it (which it always does post B6 AUTHOR-SAFE landing).
func TestProjectLearnerSafeSnapshot_MCQ_StripsIsCorrect(t *testing.T) {
	snap := `{"stem":"Q?","options":[{"option_id":"opt-A","label":"Yes","is_correct":true}]}`
	out, ok := projectLearnerSafeSnapshot("mcq", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	optsRaw, _ := out["options"].([]map[string]interface{})
	if len(optsRaw) != 1 {
		t.Fatalf("options: want 1, got %d", len(optsRaw))
	}
	if _, leaked := optsRaw[0]["is_correct"]; leaked {
		t.Errorf("LEARNER-SAFE: is_correct MUST be stripped from MCQ options (would leak the answer key pre-RELEASE)")
	}
}

// LEARNER-SAFE MCQ MUST strip `explainer` (per-option grading-feedback text)
// even when the snapshot contains it.
func TestProjectLearnerSafeSnapshot_MCQ_StripsExplainer(t *testing.T) {
	snap := `{"stem":"Q?","options":[{"option_id":"opt-A","label":"Yes","explainer":"because photosynthesis"}]}`
	out, ok := projectLearnerSafeSnapshot("mcq", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	optsRaw, _ := out["options"].([]map[string]interface{})
	if _, leaked := optsRaw[0]["explainer"]; leaked {
		t.Errorf("LEARNER-SAFE: explainer MUST be stripped from MCQ options (it's a post-grade reveal payload)")
	}
}

// LEARNER-SAFE MCQ MUST strip `correct_choice` (top-level form some snapshot
// producers may emit).
func TestProjectLearnerSafeSnapshot_MCQ_StripsCorrectChoice(t *testing.T) {
	snap := `{"stem":"Q?","correct_choice":"opt-A","options":[{"option_id":"opt-A","label":"Yes"}]}`
	out, ok := projectLearnerSafeSnapshot("mcq", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	if _, leaked := out["correct_choice"]; leaked {
		t.Errorf("LEARNER-SAFE: top-level correct_choice MUST be stripped")
	}
}

// LEARNER-SAFE OE MUST strip `model_answer` even when the snapshot contains
// it (which it always does post B6 AUTHOR-SAFE landing).
func TestProjectLearnerSafeSnapshot_OE_StripsModelAnswer(t *testing.T) {
	snap := `{"stem":"Explain X?","prompt":"Explain X?","model_answer":"X is Y because Z.","max_words":150}`
	out, ok := projectLearnerSafeSnapshot("oe", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	if _, leaked := out["model_answer"]; leaked {
		t.Errorf("LEARNER-SAFE: model_answer MUST be stripped pre-RELEASE (it's the canonical answer key)")
	}
}

// LEARNER-SAFE OE MUST strip `rubric` (grader-only criterion array).
func TestProjectLearnerSafeSnapshot_OE_StripsRubric(t *testing.T) {
	snap := `{"stem":"Q?","rubric":[{"criterion_id":"c1","title":"Clarity","weight":0.5}]}`
	out, ok := projectLearnerSafeSnapshot("oe", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	if _, leaked := out["rubric"]; leaked {
		t.Errorf("LEARNER-SAFE: rubric MUST be stripped pre-RELEASE (it's the grader's criterion weights)")
	}
}

// LEARNER-SAFE OE MUST keep prompt-side hints (max_words, etc.) — these are
// learner-facing affordances rendered on the working canvas.
func TestProjectLearnerSafeSnapshot_OE_KeepsPromptSideHints(t *testing.T) {
	snap := `{"stem":"Q?","max_words":150,"min_words":50,"placeholder_text":"Write here..."}`
	out, ok := projectLearnerSafeSnapshot("oe", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	if got := out["max_words"]; got == nil {
		t.Errorf("LEARNER-SAFE OE must keep max_words (learner-facing word-limit affordance)")
	}
	if got := out["min_words"]; got == nil {
		t.Errorf("LEARNER-SAFE OE must keep min_words")
	}
	if got := out["placeholder_text"]; got == nil {
		t.Errorf("LEARNER-SAFE OE must keep placeholder_text")
	}
}

// -----------------------------------------------------------------------------
// projectAuthorSafeSnapshot — POST-RELEASE projection (the reveal payload)
// -----------------------------------------------------------------------------

// AUTHOR-SAFE MCQ MUST surface `is_correct` + `explainer` so the learner can
// see WHY each option was right/wrong on the released result.
func TestProjectAuthorSafeSnapshot_MCQ_KeepsIsCorrectAndExplainer(t *testing.T) {
	snap := `{"stem":"Q?","options":[{"option_id":"opt-A","label":"Yes","text":"Yes","is_correct":true,"explainer":"correct because Y"}]}`
	out, ok := projectAuthorSafeSnapshot("mcq", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	optsRaw, _ := out["options"].([]map[string]interface{})
	if len(optsRaw) != 1 {
		t.Fatalf("options: want 1, got %d", len(optsRaw))
	}
	if got := optsRaw[0]["is_correct"]; got != true {
		t.Errorf("AUTHOR-SAFE: is_correct MUST be present on MCQ option; got %v", got)
	}
	if got := optsRaw[0]["explainer"]; got != "correct because Y" {
		t.Errorf("AUTHOR-SAFE: explainer MUST be present on MCQ option; got %v", got)
	}
}

// AUTHOR-SAFE OE MUST surface `model_answer` + `rubric` so the learner can
// self-assess against the canonical answer key on the released result.
func TestProjectAuthorSafeSnapshot_OE_KeepsModelAnswerAndRubric(t *testing.T) {
	snap := `{"stem":"Q?","model_answer":"X is Y because Z.","rubric":[{"criterion_id":"c1","title":"Clarity","description":"Argument is clear","weight":0.5}],"max_words":150}`
	out, ok := projectAuthorSafeSnapshot("oe", snap)
	if !ok {
		t.Fatalf("projection returned ok=false")
	}
	if got, _ := out["model_answer"].(string); got != "X is Y because Z." {
		t.Errorf("AUTHOR-SAFE OE: model_answer MUST be present (post-RELEASE reveal); got %v", out["model_answer"])
	}
	rub, _ := out["rubric"].([]map[string]interface{})
	if len(rub) != 1 {
		t.Fatalf("AUTHOR-SAFE OE: rubric MUST be present (post-RELEASE reveal); got len=%d (out=%v)", len(rub), out)
	}
	if got := rub[0]["criterion_id"]; got != "c1" {
		t.Errorf("AUTHOR-SAFE OE rubric[0].criterion_id: want c1, got %v", got)
	}
	if got := rub[0]["weight"]; got != 0.5 {
		t.Errorf("AUTHOR-SAFE OE rubric[0].weight: want 0.5, got %v", got)
	}
	// Prompt-side hints survive too.
	if got := out["max_words"]; got == nil {
		t.Errorf("AUTHOR-SAFE OE must also surface max_words")
	}
}

// AUTHOR-SAFE projection MUST fail-soft on invalid JSON (returns ok=false).
func TestProjectAuthorSafeSnapshot_InvalidJSON_ReturnsFalse(t *testing.T) {
	_, ok := projectAuthorSafeSnapshot("mcq", "{not valid")
	if ok {
		t.Errorf("AUTHOR-SAFE: want ok=false on invalid JSON")
	}
}

// AUTHOR-SAFE projection MUST fail-soft on empty input (returns ok=false).
func TestProjectAuthorSafeSnapshot_Empty_ReturnsFalse(t *testing.T) {
	_, ok := projectAuthorSafeSnapshot("mcq", "")
	if ok {
		t.Errorf("AUTHOR-SAFE: want ok=false on empty string")
	}
}
