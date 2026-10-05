// testset_test.go — domain tests for the TestSet aggregate.
//
// TDD RED phase: these tests are written FIRST and MUST fail (compile-fail)
// because testset.go does not yet exist. They drive the domain shape per
// chora-contracts/openapi/delivery-test-sets.yaml (Lane A scope: 6 endpoints).
//
// Invariants exercised:
//   - DRAFT lifecycle: state, total_points, question_count are computed
//   - Append-only-on-PUBLISHED: AddQuestion / UpdateQuestion / RemoveQuestion
//     on PUBLISHED returns ErrTestSetPublishedImmutable
//   - Publish requires at least one question (ErrTestSetNoQuestions)
//   - Publish on ARCHIVED returns ErrTestSetArchived
//   - Idempotent re-publish: publish on PUBLISHED returns nil (no mutation)
//   - Soft-delete-aware: removing a question is a logical operation; the
//     parent aggregate's question_count recomputes.
//   - Per-question display_order defaults to last+1 when unspecified.
package delivery_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	testTenantID = "11111111-1111-7111-8111-111111111111"
	testAuthorID = "00000000-0000-7000-8000-000000001999"
	testQID1     = "01985e7f-1234-7abc-8def-000000000001"
	testQID2     = "01985e7f-1234-7abc-8def-000000000002"
	testQID3     = "01985e7f-1234-7abc-8def-000000000003"
)

// -----------------------------------------------------------------------------
// Construction
// -----------------------------------------------------------------------------

func TestNewTestSet_HappyPath(t *testing.T) {
	ts, err := delivery.NewTestSet(delivery.NewTestSetInput{
		TenantID:   testTenantID,
		AuthorGCID: testAuthorID,
		Title:      "Phyllis Math Test Set 1",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	if ts.ID == "" {
		t.Fatalf("expected non-empty TestSet.ID")
	}
	if !strings.EqualFold(string(ts.State), "DRAFT") {
		t.Fatalf("expected state=DRAFT; got %q", ts.State)
	}
	if ts.QuestionCount() != 0 {
		t.Fatalf("expected QuestionCount=0; got %d", ts.QuestionCount())
	}
	if ts.TotalPoints() != 0 {
		t.Fatalf("expected TotalPoints=0; got %v", ts.TotalPoints())
	}
	if ts.DeletedAt != nil {
		t.Fatalf("expected DeletedAt=nil on construction")
	}
}

func TestNewTestSet_RejectsBlankFields(t *testing.T) {
	cases := []struct {
		name string
		in   delivery.NewTestSetInput
	}{
		{"empty tenant", delivery.NewTestSetInput{AuthorGCID: testAuthorID, Title: "x"}},
		{"empty author", delivery.NewTestSetInput{TenantID: testTenantID, Title: "x"}},
		{"empty title", delivery.NewTestSetInput{TenantID: testTenantID, AuthorGCID: testAuthorID, Title: "   "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := delivery.NewTestSet(tc.in)
			if err == nil {
				t.Fatalf("expected error for %s; got nil", tc.name)
			}
			if !errors.Is(err, delivery.ErrInvalidArgument) {
				t.Fatalf("expected ErrInvalidArgument; got %v", err)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// AddQuestion (DRAFT)
// -----------------------------------------------------------------------------

func TestAddQuestion_HappyPath_DraftIncrementsCountAndPoints(t *testing.T) {
	ts := mustNewTestSet(t)
	q, err := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1,
		QuestionType: "mcq",
		Points:       2.0,
	})
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if q.ID == "" {
		t.Fatalf("expected non-empty TestSetQuestion.ID")
	}
	if q.TestSetID != ts.ID {
		t.Fatalf("expected q.TestSetID==ts.ID; got %s vs %s", q.TestSetID, ts.ID)
	}
	if q.DisplayOrder != 1 {
		t.Fatalf("expected display_order=1 on first add; got %d", q.DisplayOrder)
	}
	if ts.QuestionCount() != 1 {
		t.Fatalf("expected QuestionCount=1; got %d", ts.QuestionCount())
	}
	if ts.TotalPoints() != 2.0 {
		t.Fatalf("expected TotalPoints=2.0; got %v", ts.TotalPoints())
	}
}

func TestAddQuestion_DefaultsDisplayOrderToLastPlusOne(t *testing.T) {
	ts := mustNewTestSet(t)
	if _, err := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 1.0,
	}); err != nil {
		t.Fatalf("AddQuestion #1: %v", err)
	}
	q2, err := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID2, QuestionID: testQID2, QuestionType: "oe", Points: 5.0,
	})
	if err != nil {
		t.Fatalf("AddQuestion #2: %v", err)
	}
	if q2.DisplayOrder != 2 {
		t.Fatalf("expected display_order=2; got %d", q2.DisplayOrder)
	}
}

func TestAddQuestion_AcceptsExplicitDisplayOrder(t *testing.T) {
	ts := mustNewTestSet(t)
	q, err := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 1.0,
		DisplayOrder: 5,
	})
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if q.DisplayOrder != 5 {
		t.Fatalf("expected display_order=5; got %d", q.DisplayOrder)
	}
}

func TestAddQuestion_RejectsBlankFields(t *testing.T) {
	cases := []struct {
		name string
		in   delivery.AddQuestionInput
	}{
		{"empty atom", delivery.AddQuestionInput{QuestionType: "mcq", Points: 1.0}},
		{"empty type", delivery.AddQuestionInput{QuestionAtomID: testQID1, QuestionID: testQID1, Points: 1.0}},
		{"zero points", delivery.AddQuestionInput{QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 0}},
		{"negative points", delivery.AddQuestionInput{QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := mustNewTestSet(t)
			_, err := ts.AddQuestion(tc.in)
			if err == nil {
				t.Fatalf("expected error for %s; got nil", tc.name)
			}
		})
	}
}

func TestAddQuestion_RejectsInvalidQuestionType(t *testing.T) {
	ts := mustNewTestSet(t)
	_, err := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1,
		QuestionType: "not-a-real-type",
		Points:       1.0,
	})
	if err == nil {
		t.Fatalf("expected error for invalid question type")
	}
}

// TestAddQuestion_CanonicalizesEssayToOE pins delivery's inbound anti-corruption
// boundary. chora-creation's question search returns question_type "essay" for
// open-ended atoms, but the delivery test-set / grading vocabulary is "oe" (per
// delivery-test-sets.yaml; the submission + grading path keys on
// QuestionTypeOE). AddQuestion MUST accept the creation alias and store the
// delivery canonical so an essay atom routes to the OE grading crew. Regression
// for the live 422 `question_type "essay" not supported` in the OE e2e.
func TestAddQuestion_CanonicalizesEssayToOE(t *testing.T) {
	for _, in := range []string{"essay", "ESSAY", "Essay"} {
		ts := mustNewTestSet(t)
		q, err := ts.AddQuestion(delivery.AddQuestionInput{
			QuestionAtomID: testQID1, QuestionID: testQID1,
			QuestionType: in,
			Points:       10.0,
		})
		if err != nil {
			t.Fatalf("AddQuestion(%q): unexpected error %v", in, err)
		}
		if q.QuestionType != string(delivery.QuestionTypeOE) {
			t.Fatalf("AddQuestion(%q): expected stored type %q; got %q", in, delivery.QuestionTypeOE, q.QuestionType)
		}
	}
}

// -----------------------------------------------------------------------------
// UpdateQuestion (DRAFT)
// -----------------------------------------------------------------------------

func TestUpdateQuestion_AdjustsPointsAndTotalRecomputes(t *testing.T) {
	ts := mustNewTestSet(t)
	q, _ := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})

	newPoints := 7.0
	updated, err := ts.UpdateQuestion(q.ID, delivery.UpdateQuestionInput{Points: &newPoints})
	if err != nil {
		t.Fatalf("UpdateQuestion: %v", err)
	}
	if updated.Points != 7.0 {
		t.Fatalf("expected points=7.0; got %v", updated.Points)
	}
	if ts.TotalPoints() != 7.0 {
		t.Fatalf("expected TotalPoints recompute=7.0; got %v", ts.TotalPoints())
	}
}

func TestUpdateQuestion_ReorderDisplayOrder(t *testing.T) {
	ts := mustNewTestSet(t)
	q1, _ := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 1.0,
	})
	newOrder := 99
	updated, err := ts.UpdateQuestion(q1.ID, delivery.UpdateQuestionInput{DisplayOrder: &newOrder})
	if err != nil {
		t.Fatalf("UpdateQuestion: %v", err)
	}
	if updated.DisplayOrder != 99 {
		t.Fatalf("expected DisplayOrder=99; got %d", updated.DisplayOrder)
	}
}

func TestUpdateQuestion_UnknownIDReturnsErrNotFound(t *testing.T) {
	ts := mustNewTestSet(t)
	pts := 1.0
	_, err := ts.UpdateQuestion("does-not-exist", delivery.UpdateQuestionInput{Points: &pts})
	if !errors.Is(err, delivery.ErrTestSetQuestionNotFound) {
		t.Fatalf("expected ErrTestSetQuestionNotFound; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// RemoveQuestion (DRAFT)
// -----------------------------------------------------------------------------

func TestRemoveQuestion_DecrementsCountAndPoints(t *testing.T) {
	ts := mustNewTestSet(t)
	q1, _ := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 3.0,
	})
	_, _ = ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID2, QuestionID: testQID2, QuestionType: "oe", Points: 4.0,
	})
	if err := ts.RemoveQuestion(q1.ID); err != nil {
		t.Fatalf("RemoveQuestion: %v", err)
	}
	if ts.QuestionCount() != 1 {
		t.Fatalf("expected QuestionCount=1 after remove; got %d", ts.QuestionCount())
	}
	if ts.TotalPoints() != 4.0 {
		t.Fatalf("expected TotalPoints=4.0; got %v", ts.TotalPoints())
	}
}

func TestRemoveQuestion_Idempotent(t *testing.T) {
	ts := mustNewTestSet(t)
	q, _ := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 1.0,
	})
	if err := ts.RemoveQuestion(q.ID); err != nil {
		t.Fatalf("RemoveQuestion 1st: %v", err)
	}
	// Second remove of the same id: idempotent (no-op or ErrNotFound).
	err := ts.RemoveQuestion(q.ID)
	if err != nil && !errors.Is(err, delivery.ErrTestSetQuestionNotFound) {
		t.Fatalf("second remove: expected nil or ErrTestSetQuestionNotFound; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Publish lifecycle
// -----------------------------------------------------------------------------

func TestPublish_DraftToPublished_HappyPath(t *testing.T) {
	ts := mustNewTestSet(t)
	_, _ = ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})
	if err := ts.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !strings.EqualFold(string(ts.State), "PUBLISHED") {
		t.Fatalf("expected state=PUBLISHED; got %q", ts.State)
	}
	if ts.PublishedAt == nil || ts.PublishedAt.IsZero() {
		t.Fatalf("expected PublishedAt to be stamped")
	}
}

func TestPublish_OnEmptyDraft_ReturnsErrNoQuestions(t *testing.T) {
	ts := mustNewTestSet(t)
	err := ts.Publish()
	if !errors.Is(err, delivery.ErrTestSetNoQuestions) {
		t.Fatalf("expected ErrTestSetNoQuestions; got %v", err)
	}
	if !strings.EqualFold(string(ts.State), "DRAFT") {
		t.Fatalf("expected state still DRAFT; got %q", ts.State)
	}
}

func TestPublish_Idempotent_ReturnsNil(t *testing.T) {
	ts := mustNewTestSet(t)
	_, _ = ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})
	if err := ts.Publish(); err != nil {
		t.Fatalf("Publish 1st: %v", err)
	}
	firstPublishedAt := *ts.PublishedAt
	// Idempotent re-publish: no error, no PublishedAt mutation.
	if err := ts.Publish(); err != nil {
		t.Fatalf("Publish 2nd should be idempotent; got %v", err)
	}
	if !ts.PublishedAt.Equal(firstPublishedAt) {
		t.Fatalf("expected PublishedAt unchanged; before=%v after=%v",
			firstPublishedAt, *ts.PublishedAt)
	}
}

func TestPublish_OnArchived_ReturnsErrArchived(t *testing.T) {
	ts := mustNewTestSet(t)
	_, _ = ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 1.0,
	})
	ts.Archive()
	err := ts.Publish()
	if !errors.Is(err, delivery.ErrTestSetArchived) {
		t.Fatalf("expected ErrTestSetArchived; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Append-only-on-PUBLISHED (ADR-155 D5)
// -----------------------------------------------------------------------------

func TestAddQuestion_OnPublished_ReturnsErrImmutable(t *testing.T) {
	ts := mustNewTestSet(t)
	_, _ = ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})
	_ = ts.Publish()

	_, err := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID2, QuestionID: testQID2, QuestionType: "oe", Points: 5.0,
	})
	if !errors.Is(err, delivery.ErrTestSetPublishedImmutable) {
		t.Fatalf("expected ErrTestSetPublishedImmutable; got %v", err)
	}
}

func TestUpdateQuestion_OnPublished_ReturnsErrImmutable(t *testing.T) {
	ts := mustNewTestSet(t)
	q, _ := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})
	_ = ts.Publish()

	newPts := 9.0
	_, err := ts.UpdateQuestion(q.ID, delivery.UpdateQuestionInput{Points: &newPts})
	if !errors.Is(err, delivery.ErrTestSetPublishedImmutable) {
		t.Fatalf("expected ErrTestSetPublishedImmutable; got %v", err)
	}
}

func TestRemoveQuestion_OnPublished_ReturnsErrImmutable(t *testing.T) {
	ts := mustNewTestSet(t)
	q, _ := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})
	_ = ts.Publish()

	err := ts.RemoveQuestion(q.ID)
	if !errors.Is(err, delivery.ErrTestSetPublishedImmutable) {
		t.Fatalf("expected ErrTestSetPublishedImmutable; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Archive
// -----------------------------------------------------------------------------

// -----------------------------------------------------------------------------
// Fix-F — Publish-time snapshot (Lane A snapshot debt close)
// Per `feedback_no_stubs_real_wiring` + ddd-enforcement #3.
// -----------------------------------------------------------------------------

// stubSnapshotter is a test double for the QuestionSnapshotter port. Real
// production wiring is the gRPC client adapter — this stays in the test file
// only, NOT in the domain package.
type stubSnapshotter struct {
	payloads map[string]delivery.QuestionPayloadSnapshot
	err      error
	calls    []string
	// callers captures the ADR-229 publish actor per call (CHO-2133).
	callers []string
}

func (s *stubSnapshotter) SnapshotQuestion(_ context.Context, tenantID, questionID, callerGCID string) (delivery.QuestionPayloadSnapshot, error) {
	s.calls = append(s.calls, tenantID+"|"+questionID)
	s.callers = append(s.callers, callerGCID)
	if s.err != nil {
		return delivery.QuestionPayloadSnapshot{}, s.err
	}
	v, ok := s.payloads[questionID]
	if !ok {
		return delivery.QuestionPayloadSnapshot{}, delivery.ErrQuestionSnapshotNotFound
	}
	return v, nil
}

func TestPublishWithSnapshot_HappyPath(t *testing.T) {
	ts := mustNewTestSet(t)
	q, _ := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})
	snap := &stubSnapshotter{
		payloads: map[string]delivery.QuestionPayloadSnapshot{
			testQID1: {
				QuestionType: "mcq",
				PayloadJSON:  `{"options":[{"option_id":"opt-a","is_correct":true},{"option_id":"opt-b","is_correct":false}],"scoring":{"mode":"single_correct"}}`,
				CapturedAt:   time.Now().UTC(),
			},
		},
	}
	if err := ts.PublishWithSnapshot(context.Background(), snap, testAuthorID); err != nil {
		t.Fatalf("PublishWithSnapshot: %v", err)
	}
	if !strings.EqualFold(string(ts.State), "PUBLISHED") {
		t.Fatalf("state: want PUBLISHED, got %q", ts.State)
	}
	// All question rows have payload_snapshot populated.
	qs := ts.Questions()
	if len(qs) != 1 {
		t.Fatalf("questions: want 1, got %d", len(qs))
	}
	if qs[0].PayloadSnapshot == "" {
		t.Errorf("question %s: payload_snapshot empty after publish", qs[0].ID)
	}
	if qs[0].SnapshotAt == nil {
		t.Errorf("question %s: snapshot_at nil after publish", qs[0].ID)
	}
	// Snapshotter called once per question.
	if got := len(snap.calls); got != 1 {
		t.Errorf("snapshotter calls: want 1, got %d", got)
	}
	_ = q
}

func TestPublishWithSnapshot_SnapshotterFailure_FailsLoud(t *testing.T) {
	// Per feedback_no_stubs_real_wiring: snapshotter error MUST abort
	// Publish — never silently green-path with no snapshot.
	ts := mustNewTestSet(t)
	_, _ = ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})
	snap := &stubSnapshotter{err: errors.New("gRPC unavailable")}
	err := ts.PublishWithSnapshot(context.Background(), snap, testAuthorID)
	if err == nil {
		t.Fatalf("want error when snapshotter fails, got nil")
	}
	// State unchanged.
	if !strings.EqualFold(string(ts.State), "DRAFT") {
		t.Errorf("state on snapshot failure: want DRAFT (unchanged), got %q", ts.State)
	}
}

func TestPublishWithSnapshot_NoSnapshotter_FallsBackToPublish(t *testing.T) {
	// When no snapshotter is wired (e.g., local dev / in-memory tests),
	// PublishWithSnapshot is equivalent to Publish() — but the caller MUST
	// guarantee that downstream grade-time lookups won't crash. This is
	// the dev/test convenience.
	ts := mustNewTestSet(t)
	_, _ = ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})
	if err := ts.PublishWithSnapshot(context.Background(), nil, testAuthorID); err != nil {
		t.Fatalf("PublishWithSnapshot(nil): %v", err)
	}
	if !strings.EqualFold(string(ts.State), "PUBLISHED") {
		t.Fatalf("state: want PUBLISHED, got %q", ts.State)
	}
}

func TestPublishWithSnapshot_Idempotent(t *testing.T) {
	ts := mustNewTestSet(t)
	_, _ = ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})
	snap := &stubSnapshotter{
		payloads: map[string]delivery.QuestionPayloadSnapshot{
			testQID1: {
				QuestionType: "mcq",
				PayloadJSON:  `{"options":[{"option_id":"opt-a","is_correct":true}],"scoring":{"mode":"single_correct"}}`,
				CapturedAt:   time.Now().UTC(),
			},
		},
	}
	if err := ts.PublishWithSnapshot(context.Background(), snap, testAuthorID); err != nil {
		t.Fatalf("PublishWithSnapshot 1: %v", err)
	}
	firstPub := *ts.PublishedAt
	// Second call must be idempotent and NOT re-fetch the snapshot.
	if err := ts.PublishWithSnapshot(context.Background(), snap, testAuthorID); err != nil {
		t.Fatalf("PublishWithSnapshot 2 (idempotent): %v", err)
	}
	if !ts.PublishedAt.Equal(firstPub) {
		t.Errorf("PublishedAt mutated on idempotent re-publish")
	}
	// Snapshotter called ONCE (not twice).
	if got := len(snap.calls); got != 1 {
		t.Errorf("snapshotter calls: want 1 (idempotent skips re-fetch), got %d", got)
	}
}

func TestArchive_SetsArchivedAndSoftDeletes(t *testing.T) {
	ts := mustNewTestSet(t)
	ts.Archive()
	if !strings.EqualFold(string(ts.State), "ARCHIVED") {
		t.Fatalf("expected state=ARCHIVED; got %q", ts.State)
	}
	if ts.DeletedAt == nil {
		t.Fatalf("expected DeletedAt to be stamped after archive")
	}
}

// -----------------------------------------------------------------------------
// Listing / read accessors
// -----------------------------------------------------------------------------

func TestQuestions_ReturnsOrderedByDisplayOrder(t *testing.T) {
	ts := mustNewTestSet(t)
	q1, _ := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 1.0, DisplayOrder: 3,
	})
	q2, _ := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID2, QuestionID: testQID2, QuestionType: "oe", Points: 2.0, DisplayOrder: 1,
	})
	q3, _ := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID3, QuestionID: testQID3, QuestionType: "mcq", Points: 3.0, DisplayOrder: 2,
	})

	got := ts.Questions()
	if len(got) != 3 {
		t.Fatalf("expected 3 questions; got %d", len(got))
	}
	want := []string{q2.ID, q3.ID, q1.ID}
	for i, qid := range want {
		if got[i].ID != qid {
			t.Fatalf("order[%d]: want %s; got %s", i, qid, got[i].ID)
		}
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func mustNewTestSet(t *testing.T) *delivery.TestSet {
	t.Helper()
	ts, err := delivery.NewTestSet(delivery.NewTestSetInput{
		TenantID:   testTenantID,
		AuthorGCID: testAuthorID,
		Title:      "Phyllis Math Test Set 1",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	return ts
}

// -----------------------------------------------------------------------------
// LEG3-D R3 Option B — explicit question_id distinct from question_atom_id.
//
// Per docs/m13/wave3-leg3d-round3-blocker-2026-05-16.md + user "go B"
// decision: the atom payload's embedded question_id is the canonical
// identifier passed to chora-creation.SnapshotQuestionByID. The FE picker
// extracts mcq_payload.question_id from the loaded atom and posts BOTH
// question_atom_id + question_id distinctly; chora-delivery stores both
// and threads question_id through PublishWithSnapshot (NOT atom_id).
// -----------------------------------------------------------------------------

// Distinct UUIDv7 strings that mirror the production split:
//
//   - testQAtomID1 stands in for `mcq_payload.atom_id` (the LearningAtom row)
//   - testEmbeddedQID1 stands in for `mcq_payload.question_id`
//     (the Question UUID embedded inside the atom payload, sourced from
//     chora_creation.questions.question_id).
const (
	testQAtomID1     = "00000000-0000-7000-8000-00000000a0a2"
	testEmbeddedQID1 = "019e2ba6-7353-73f5-b517-dd627e76d450"
	testQAtomID2     = "00000000-0000-7000-8000-00000000a0a5"
	testEmbeddedQID2 = "019e2ba6-9999-73f5-b517-dd627e76d451"
)

func TestAddQuestion_RequiresExplicitQuestionID(t *testing.T) {
	ts := mustNewTestSet(t)
	_, err := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQAtomID1,
		// QuestionID intentionally blank — domain MUST reject so a stray
		// caller (skipping the FE picker resolution) doesn't silently send
		// atom_id as the question_id at SnapshotQuestionByID time.
		QuestionType: "mcq",
		Points:       2.0,
	})
	if err == nil {
		t.Fatalf("expected error when question_id is blank; got nil")
	}
	if !errors.Is(err, delivery.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument; got %v", err)
	}
}

func TestAddQuestion_StoresQuestionIDDistinctFromAtomID(t *testing.T) {
	ts := mustNewTestSet(t)
	q, err := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQAtomID1,
		QuestionID:     testEmbeddedQID1,
		QuestionType:   "mcq",
		Points:         2.0,
	})
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if q.QuestionAtomID != testQAtomID1 {
		t.Errorf("question_atom_id: want %q; got %q", testQAtomID1, q.QuestionAtomID)
	}
	if q.QuestionID != testEmbeddedQID1 {
		t.Errorf("question_id: want %q; got %q", testEmbeddedQID1, q.QuestionID)
	}
	if q.QuestionAtomID == q.QuestionID {
		t.Errorf("question_atom_id and question_id MUST be stored as distinct fields; both = %q",
			q.QuestionAtomID)
	}
}

func TestPublishWithSnapshot_UsesQuestionID_NotAtomID(t *testing.T) {
	// The snapshotter is invoked with (tenant, question_id) — NEVER atom_id.
	// This guards the LEG3-D R3 regression in which atom_id was passed and
	// chora-creation returned NotFound → publish 502.
	ts := mustNewTestSet(t)
	if _, err := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQAtomID1,
		QuestionID:     testEmbeddedQID1,
		QuestionType:   "mcq",
		Points:         2.0,
	}); err != nil {
		t.Fatalf("AddQuestion #1: %v", err)
	}
	if _, err := ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQAtomID2,
		QuestionID:     testEmbeddedQID2,
		QuestionType:   "mcq",
		Points:         3.0,
	}); err != nil {
		t.Fatalf("AddQuestion #2: %v", err)
	}
	snap := &stubSnapshotter{
		payloads: map[string]delivery.QuestionPayloadSnapshot{
			testEmbeddedQID1: {QuestionType: "mcq", PayloadJSON: `{"options":[]}`, CapturedAt: time.Now().UTC()},
			testEmbeddedQID2: {QuestionType: "mcq", PayloadJSON: `{"options":[]}`, CapturedAt: time.Now().UTC()},
		},
	}
	if err := ts.PublishWithSnapshot(context.Background(), snap, testAuthorID); err != nil {
		t.Fatalf("PublishWithSnapshot: %v", err)
	}
	if !strings.EqualFold(string(ts.State), "PUBLISHED") {
		t.Fatalf("state: want PUBLISHED; got %q", ts.State)
	}
	// Snapshotter calls MUST carry question_id, not atom_id. The stub records
	// each call as "tenant|questionID"; assert both embedded UUIDs landed
	// and neither atom UUID did.
	wantCalls := map[string]bool{
		testTenantID + "|" + testEmbeddedQID1: false,
		testTenantID + "|" + testEmbeddedQID2: false,
	}
	for _, call := range snap.calls {
		if _, ok := wantCalls[call]; ok {
			wantCalls[call] = true
		}
		if strings.Contains(call, testQAtomID1) || strings.Contains(call, testQAtomID2) {
			t.Errorf("snapshotter call %q carried atom_id; MUST carry question_id only", call)
		}
	}
	for k, seen := range wantCalls {
		if !seen {
			t.Errorf("expected snapshotter call %q; never seen", k)
		}
	}
}

// -----------------------------------------------------------------------------
// UpdateMetadata (PATCH /api/v1/test-sets/{id}) — TESTSET-PATCH-TITLE-504 close
// -----------------------------------------------------------------------------
//
// Contract: chora-contracts/openapi/delivery-test-sets.yaml#updateTestSet.
// Partial update on title + description; nil pointers = "do not modify".
// DRAFT only — PUBLISHED returns ErrTestSetPublishedImmutable (HTTP 409);
// ARCHIVED returns ErrTestSetArchived. Empty/whitespace title rejected.
// updated_at bumps on any mutation.

func TestUpdateMetadata_UpdatesTitleOnly_LeavesDescription(t *testing.T) {
	ts, err := delivery.NewTestSet(delivery.NewTestSetInput{
		TenantID:    testTenantID,
		AuthorGCID:  testAuthorID,
		Title:       "Untitled Test Set",
		Description: "kept",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	prevUpdatedAt := ts.UpdatedAt
	time.Sleep(2 * time.Millisecond)

	newTitle := "Phyllis CJ#1 Smoke"
	if err := ts.UpdateMetadata(delivery.UpdateTestSetMetadataInput{Title: &newTitle}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if ts.Title != "Phyllis CJ#1 Smoke" {
		t.Errorf("Title = %q; want %q", ts.Title, "Phyllis CJ#1 Smoke")
	}
	if ts.Description != "kept" {
		t.Errorf("Description mutated unexpectedly: got %q; want %q", ts.Description, "kept")
	}
	if !ts.UpdatedAt.After(prevUpdatedAt) {
		t.Errorf("UpdatedAt must bump on mutation; before=%v after=%v", prevUpdatedAt, ts.UpdatedAt)
	}
}

func TestUpdateMetadata_UpdatesDescriptionOnly(t *testing.T) {
	ts, _ := delivery.NewTestSet(delivery.NewTestSetInput{
		TenantID:    testTenantID,
		AuthorGCID:  testAuthorID,
		Title:       "kept",
		Description: "old",
	})
	newDesc := "new"
	if err := ts.UpdateMetadata(delivery.UpdateTestSetMetadataInput{Description: &newDesc}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if ts.Title != "kept" {
		t.Errorf("Title mutated unexpectedly: got %q; want %q", ts.Title, "kept")
	}
	if ts.Description != "new" {
		t.Errorf("Description = %q; want %q", ts.Description, "new")
	}
}

func TestUpdateMetadata_RejectsBlankTitle(t *testing.T) {
	ts := mustNewTestSet(t)
	for _, blank := range []string{"", "   ", "\t"} {
		bad := blank
		if err := ts.UpdateMetadata(delivery.UpdateTestSetMetadataInput{Title: &bad}); err == nil {
			t.Errorf("UpdateMetadata(title=%q) expected non-nil error", blank)
		}
	}
}

func TestUpdateMetadata_OnPublished_ReturnsErrImmutable(t *testing.T) {
	ts := mustNewTestSet(t)
	_, _ = ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})
	if err := ts.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	newTitle := "should fail"
	err := ts.UpdateMetadata(delivery.UpdateTestSetMetadataInput{Title: &newTitle})
	if !errors.Is(err, delivery.ErrTestSetPublishedImmutable) {
		t.Errorf("expected ErrTestSetPublishedImmutable; got %v", err)
	}
}

func TestUpdateMetadata_OnArchived_ReturnsErrArchived(t *testing.T) {
	ts := mustNewTestSet(t)
	ts.Archive()
	newTitle := "should fail"
	err := ts.UpdateMetadata(delivery.UpdateTestSetMetadataInput{Title: &newTitle})
	if !errors.Is(err, delivery.ErrTestSetArchived) {
		t.Errorf("expected ErrTestSetArchived; got %v", err)
	}
}

func TestUpdateMetadata_NoFields_NoOp(t *testing.T) {
	ts, _ := delivery.NewTestSet(delivery.NewTestSetInput{
		TenantID:    testTenantID,
		AuthorGCID:  testAuthorID,
		Title:       "kept",
		Description: "kept",
	})
	prevUpdatedAt := ts.UpdatedAt
	if err := ts.UpdateMetadata(delivery.UpdateTestSetMetadataInput{}); err != nil {
		t.Fatalf("UpdateMetadata empty: %v", err)
	}
	if ts.Title != "kept" || ts.Description != "kept" {
		t.Errorf("no-op should preserve fields")
	}
	if !ts.UpdatedAt.Equal(prevUpdatedAt) {
		t.Errorf("UpdatedAt should NOT bump on no-op; before=%v after=%v", prevUpdatedAt, ts.UpdatedAt)
	}
}

// -----------------------------------------------------------------------------
// ADR-229 WS-2 (CHO-2133) — the publish actor threads through to the
// snapshotter so chora-creation's SnapshotQuestionByID gate (chokepoint 2)
// can evaluate owner ∨ tenant-visible ∨ granted for the REAL caller.
// -----------------------------------------------------------------------------

func TestPublishWithSnapshot_ThreadsPublishActorToSnapshotter(t *testing.T) {
	ts := mustNewTestSet(t)
	_, _ = ts.AddQuestion(delivery.AddQuestionInput{
		QuestionAtomID: testQID1, QuestionID: testQID1, QuestionType: "mcq", Points: 2.0,
	})
	snap := &stubSnapshotter{
		payloads: map[string]delivery.QuestionPayloadSnapshot{
			testQID1: {QuestionType: "mcq", PayloadJSON: `{"k":"v"}`, CapturedAt: time.Now().UTC()},
		},
	}
	const actor = "01970000-0000-7000-9000-00000000ac70"
	if err := ts.PublishWithSnapshot(context.Background(), snap, actor); err != nil {
		t.Fatalf("PublishWithSnapshot: %v", err)
	}
	if len(snap.callers) != 1 || snap.callers[0] != actor {
		t.Fatalf("snapshotter callers = %v; want [%s] (the publish actor)", snap.callers, actor)
	}
}
