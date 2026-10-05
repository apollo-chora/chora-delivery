// coverage_testset_test.go — statement-coverage top-up for the TestSet
// aggregate read/load surfaces (hydration, summary counters, question access)
// and the remaining published/archive guard branches.
package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// covTestSet builds a fresh DRAFT test-set.
func covTestSet(t *testing.T) *TestSet {
	t.Helper()
	ts, err := NewTestSet(NewTestSetInput{
		TenantID:   "11111111-1111-7111-8111-111111111111",
		AuthorGCID: "00000000-0000-7000-8000-000000001999",
		Title:      "Coverage Set",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	return ts
}

// stubSnapshotter is a scriptable QuestionSnapshotter.
type stubSnapshotter struct {
	perQuestion map[string]QuestionPayloadSnapshot
	err         error
}

func (s *stubSnapshotter) SnapshotQuestion(_ context.Context, _ string, questionID string, _ string) (QuestionPayloadSnapshot, error) {
	if s.err != nil {
		return QuestionPayloadSnapshot{}, s.err
	}
	if snap, ok := s.perQuestion[questionID]; ok {
		return snap, nil
	}
	return QuestionPayloadSnapshot{}, ErrQuestionSnapshotNotFound
}

func TestNewTestSet_SourceJobIDAndLimits(t *testing.T) {
	ts, err := NewTestSet(NewTestSetInput{
		TenantID: "t", AuthorGCID: "a", Title: "T", SourceJobID: "  job-1  ",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	if ts.SourceJobID == nil || *ts.SourceJobID != "job-1" {
		t.Fatalf("source_job_id: %v", ts.SourceJobID)
	}
	// Whitespace-only source job id ⇒ nil.
	ts2, err := NewTestSet(NewTestSetInput{TenantID: "t", AuthorGCID: "a", Title: "T", SourceJobID: "   "})
	if err != nil {
		t.Fatalf("NewTestSet blank job: %v", err)
	}
	if ts2.SourceJobID != nil {
		t.Fatalf("blank source_job_id must stay nil, got %q", *ts2.SourceJobID)
	}
	if _, err := NewTestSet(NewTestSetInput{TenantID: "t", AuthorGCID: "a", Title: strings.Repeat("x", 257)}); err == nil {
		t.Fatal("title > 256 must error")
	}
	if _, err := NewTestSet(NewTestSetInput{TenantID: "t", AuthorGCID: "a", Title: "T", Description: strings.Repeat("d", 2001)}); err == nil {
		t.Fatal("description > 2000 must error")
	}
}

func TestTestSet_UpdateMetadata_Guards(t *testing.T) {
	ts := covTestSet(t)
	// No-op input → nil without an UpdatedAt bump.
	before := ts.UpdatedAt
	if err := ts.UpdateMetadata(UpdateTestSetMetadataInput{}); err != nil {
		t.Fatalf("no-op metadata: %v", err)
	}
	if !ts.UpdatedAt.Equal(before) {
		t.Fatal("no-op metadata must not bump updated_at")
	}
	blank := "   "
	if err := ts.UpdateMetadata(UpdateTestSetMetadataInput{Title: &blank}); err == nil {
		t.Fatal("blank title must error")
	}
	longTitle := strings.Repeat("t", 257)
	if err := ts.UpdateMetadata(UpdateTestSetMetadataInput{Title: &longTitle}); err == nil {
		t.Fatal("long title must error")
	}
	longDesc := strings.Repeat("d", 2001)
	if err := ts.UpdateMetadata(UpdateTestSetMetadataInput{Description: &longDesc}); err == nil {
		t.Fatal("long description must error")
	}
	// Happy path: title + description applied, timestamp bumps.
	t2 := "New Title"
	d2 := "New Desc"
	if err := ts.UpdateMetadata(UpdateTestSetMetadataInput{Title: &t2, Description: &d2}); err != nil {
		t.Fatalf("metadata update: %v", err)
	}
	if ts.Title != "New Title" || ts.Description != "New Desc" {
		t.Fatalf("metadata not applied: %q/%q", ts.Title, ts.Description)
	}
}

func TestTestSet_UpdateQuestion_Guards(t *testing.T) {
	ts := covTestSet(t)
	q, err := ts.AddQuestion(AddQuestionInput{
		QuestionAtomID: "atom-1", QuestionID: "q-1", QuestionType: "mcq", Points: 2,
	})
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if _, err := ts.UpdateQuestion("no-such-id", UpdateQuestionInput{}); err == nil {
		t.Fatal("unknown question must error")
	}
	zero := 0.0
	if _, err := ts.UpdateQuestion(q.ID, UpdateQuestionInput{Points: &zero}); err == nil {
		t.Fatal("non-positive points must error")
	}
	neg := -1
	if _, err := ts.UpdateQuestion(q.ID, UpdateQuestionInput{DisplayOrder: &neg}); err == nil {
		t.Fatal("negative display_order must error")
	}
	pts := 5.0
	order := 7
	got, err := ts.UpdateQuestion(q.ID, UpdateQuestionInput{Points: &pts, DisplayOrder: &order})
	if err != nil {
		t.Fatalf("UpdateQuestion: %v", err)
	}
	if got.Points != 5 || got.DisplayOrder != 7 {
		t.Fatalf("question not updated: %+v", got)
	}
}

func TestTestSet_HydrateAndSummaryCounters(t *testing.T) {
	ts := covTestSet(t)

	// Summary counters backing a list-style load.
	ts.HydrateSummaryCounters(3, 12.5)
	if got := ts.QuestionCount(); got != 3 {
		t.Fatalf("QuestionCount shadow: want 3, got %d", got)
	}
	if got := ts.TotalPoints(); got != 12.5 {
		t.Fatalf("TotalPoints shadow: want 12.5, got %f", got)
	}
	// Negative values never SET an override (probed on a fresh aggregate
	// whose overrides are still nil).
	fresh := covTestSet(t)
	fresh.HydrateSummaryCounters(-1, -1)
	if fresh.summaryQuestionCount != nil || fresh.summaryTotalPoints != nil {
		t.Fatal("negative hydrate args must leave overrides unset")
	}

	// HydrateQuestion appends a pre-persisted child; missing TestSetID is filled.
	ts.HydrateQuestion(TestSetQuestion{ID: "child-1", QuestionID: "x", QuestionType: "mcq", Points: 4})
	ts.HydrateQuestion(TestSetQuestion{TestSetID: "explicit", ID: "child-2", QuestionID: "y", QuestionType: "oe", Points: 6})
	if len(ts.questions) != 2 {
		t.Fatalf("hydrated questions: want 2, got %d", len(ts.questions))
	}
	if ts.questions[0].TestSetID != ts.ID {
		t.Fatalf("empty TestSetID must default to the aggregate id: %q", ts.questions[0].TestSetID)
	}
	if ts.questions[1].TestSetID != "explicit" {
		t.Fatalf("explicit TestSetID must be kept: %q", ts.questions[1].TestSetID)
	}
	// Live children now drive the accessors (summary override ignored).
	if got := ts.QuestionCount(); got != 2 {
		t.Fatalf("QuestionCount after hydrate: want 2, got %d", got)
	}
	if got := ts.TotalPoints(); got != 10 {
		t.Fatalf("TotalPoints after hydrate: want 10, got %f", got)
	}

	// Questions() orders by display_order asc, breaks ties by CreatedAt asc,
	// and copies (mutating the returned slice must not touch the aggregate).
	ts.questions[0].DisplayOrder = 2
	ts.questions[1].DisplayOrder = 1
	all := ts.Questions()
	if len(all) != 2 || all[0].ID != "child-2" || all[1].ID != "child-1" {
		t.Fatalf("Questions ordering: %+v", idsOf(all))
	}
	all[0].Points = 999
	if ts.questions[1].Points != 6 {
		t.Fatal("Questions() must return copies")
	}
}

func TestTestSet_Questions_ExcludesDeleted(t *testing.T) {
	ts := covTestSet(t)
	live, _ := ts.AddQuestion(AddQuestionInput{QuestionAtomID: "a", QuestionID: "q1", QuestionType: "mcq", Points: 1})
	gone, _ := ts.AddQuestion(AddQuestionInput{QuestionAtomID: "b", QuestionID: "q2", QuestionType: "mcq", Points: 2, DisplayOrder: 5})
	now := time.Now().UTC()
	gone.DeletedAt = &now
	all := ts.Questions()
	if len(all) != 1 || all[0].ID != live.ID {
		t.Fatalf("deleted question must be excluded: %+v", idsOf(all))
	}
	// nextDisplayOrderLocked skips deleted rows → 6 (max live order 1 + 1).
	added, err := ts.AddQuestion(AddQuestionInput{QuestionAtomID: "c", QuestionID: "q3", QuestionType: "mcq", Points: 1})
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if added.DisplayOrder != 2 {
		t.Fatalf("next display order: want 2 (deleted order-5 skipped), got %d", added.DisplayOrder)
	}
}

func TestTestSet_PublishWithSnapshot_Paths(t *testing.T) {
	ctx := context.Background()
	stub := &stubSnapshotter{perQuestion: map[string]QuestionPayloadSnapshot{
		"q-1": {QuestionType: "mcq", PayloadJSON: `{"k":"v"}`, CapturedAt: time.Now().UTC()},
	}}
	actor := "00000000-0000-7000-8000-000000001999"

	// Idempotent re-publish.
	t1 := covTestSet(t)
	q1, _ := t1.AddQuestion(AddQuestionInput{QuestionAtomID: "a", QuestionID: "q-1", QuestionType: "mcq", Points: 1})
	if err := t1.PublishWithSnapshot(ctx, stub, actor); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if t1.State != TestSetStatePublished {
		t.Fatalf("state: want PUBLISHED, got %s", t1.State)
	}
	if q1.PayloadSnapshot != `{"k":"v"}` || q1.SnapshotAt == nil {
		t.Fatalf("snapshot not captured: %+v", q1)
	}
	if err := t1.PublishWithSnapshot(ctx, stub, actor); err != nil {
		t.Fatalf("idempotent re-publish: %v", err)
	}

	// Archived refuses.
	t2 := covTestSet(t)
	t2.Archive()
	if err := t2.PublishWithSnapshot(ctx, stub, actor); !errors.Is(err, ErrTestSetArchived) {
		t.Fatalf("archived publish: want ErrTestSetArchived, got %v", err)
	}

	// No questions refuses.
	t3 := covTestSet(t)
	if err := t3.PublishWithSnapshot(ctx, stub, actor); !errors.Is(err, ErrTestSetNoQuestions) {
		t.Fatalf("empty publish: want ErrTestSetNoQuestions, got %v", err)
	}

	// Snapshotter error fails loud; state stays DRAFT.
	t4 := covTestSet(t)
	if _, err := t4.AddQuestion(AddQuestionInput{QuestionAtomID: "a", QuestionID: "q-missing", QuestionType: "mcq", Points: 1}); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if err := t4.PublishWithSnapshot(ctx, stub, actor); err == nil {
		t.Fatal("missing snapshot must fail loud")
	}
	if t4.State != TestSetStateDraft {
		t.Fatalf("failed publish must stay DRAFT, got %s", t4.State)
	}

	// Zero CapturedAt falls back to the publish instant.
	t5 := covTestSet(t)
	zeroSnap := &stubSnapshotter{perQuestion: map[string]QuestionPayloadSnapshot{
		"q-5": {QuestionType: "mcq", PayloadJSON: `{}`},
	}}
	if _, err := t5.AddQuestion(AddQuestionInput{QuestionAtomID: "a", QuestionID: "q-5", QuestionType: "mcq", Points: 1}); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if err := t5.PublishWithSnapshot(ctx, zeroSnap, actor); err != nil {
		t.Fatalf("zero-captured publish: %v", err)
	}
	if t5.questions[0].SnapshotAt == nil || t5.questions[0].SnapshotAt.IsZero() {
		t.Fatal("zero CapturedAt must fall back to publish time")
	}
}

func TestTestSet_Archive_Idempotent(t *testing.T) {
	ts := covTestSet(t)
	ts.Archive()
	stamp := ts.UpdatedAt
	ts.Archive() // second call is a no-op
	if ts.State != TestSetStateArchived || !ts.UpdatedAt.Equal(stamp) {
		t.Fatalf("idempotent archive: %s updated=%v", ts.State, ts.UpdatedAt)
	}
}

func idsOf(qs []*TestSetQuestion) []string {
	out := make([]string, 0, len(qs))
	for _, q := range qs {
		out = append(out, q.ID)
	}
	return out
}
