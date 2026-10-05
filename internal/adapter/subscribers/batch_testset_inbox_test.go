// batch_testset_inbox_test.go — Lane 1c W4 (CHO-1703 / ADR-180 D10)
// subscriber tests for chora.creation.question_batch.accepted.v1.
//
// TDD RED phase: written FIRST. Drives the assembly contract:
//
//   - ONE DRAFT test set assembled via the domain aggregate (title/desc/
//     author/tenant from the event; items in curated order with points).
//   - Idempotency FIRST: an existing test_sets.source_job_id row ⇒ ACK
//     no-op (no second save, no second event).
//   - Question existence validated via the EXISTING QuestionSnapshotter
//     port: NotFound ⇒ skip that item + assemble the rest (never
//     infinite-retry a permanently-bad item); transport error ⇒ Nack.
//   - Emits the EXISTING chora.delivery.test_set.created.v1 (reuse).
//   - Unique-race loss at Save ⇒ re-check finds the winner's row ⇒ ACK.
package subscribers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	btTenant = "11111111-1111-7111-8111-111111111111"
	btAuthor = "00000000-0000-7000-8000-000000001999"
	btJobID  = "01985e7f-0000-7000-8000-00000000aaaa"
	btAtom1  = "01985e7f-aaaa-7000-8000-000000000001"
	btQ1     = "01985e7f-bbbb-7000-8000-000000000001"
	btAtom2  = "01985e7f-aaaa-7000-8000-000000000002"
	btQ2     = "01985e7f-bbbb-7000-8000-000000000002"
)

// fakeAssemblyStore records Save calls + serves GetBySourceJobID.
type fakeAssemblyStore struct {
	byJob    map[string]*domain.TestSet // sourceJobID → aggregate
	saved    []*domain.TestSet
	saveErrs []error // popped per Save call; nil entry = success

	// installAfter/pending simulate a concurrent winner landing between the
	// existence check and Save: after `installAfter` GetBySourceJobID
	// misses, `pending` becomes visible to subsequent lookups.
	installAfter int
	missCount    int
	pending      *domain.TestSet
}

func newFakeAssemblyStore() *fakeAssemblyStore {
	return &fakeAssemblyStore{byJob: map[string]*domain.TestSet{}}
}

// installAfterMisses arms the concurrent-winner simulation.
func (f *fakeAssemblyStore) installAfterMisses(n int, ts *domain.TestSet) {
	f.installAfter = n
	f.pending = ts
}

func (f *fakeAssemblyStore) GetBySourceJobID(_ context.Context, tenantID, sourceJobID string) (*domain.TestSet, bool, error) {
	ts, ok := f.byJob[sourceJobID]
	if !ok || ts.TenantID != tenantID {
		f.missCount++
		if f.pending != nil && f.missCount >= f.installAfter && f.pending.SourceJobID != nil {
			f.byJob[*f.pending.SourceJobID] = f.pending
			f.pending = nil
		}
		return nil, false, nil
	}
	return ts, true, nil
}

func (f *fakeAssemblyStore) Save(_ context.Context, ts *domain.TestSet) error {
	if len(f.saveErrs) > 0 {
		err := f.saveErrs[0]
		f.saveErrs = f.saveErrs[1:]
		if err != nil {
			return err
		}
	}
	f.saved = append(f.saved, ts)
	if ts.SourceJobID != nil {
		f.byJob[*ts.SourceJobID] = ts
	}
	return nil
}

// fakeSnapshotter returns ErrQuestionSnapshotNotFound for ids in missing,
// a transport error for ids in broken, success otherwise.
type fakeSnapshotter struct {
	missing map[string]bool
	broken  map[string]bool
	calls   []string
	// callers captures the ADR-229 reuse actor per probe (CHO-2133).
	callers []string
}

func (f *fakeSnapshotter) SnapshotQuestion(_ context.Context, _ string, questionID, callerGCID string) (domain.QuestionPayloadSnapshot, error) {
	f.calls = append(f.calls, questionID)
	f.callers = append(f.callers, callerGCID)
	if f.broken[questionID] {
		return domain.QuestionPayloadSnapshot{}, errors.New("grpc: unavailable")
	}
	if f.missing[questionID] {
		return domain.QuestionPayloadSnapshot{}, domain.ErrQuestionSnapshotNotFound
	}
	return domain.QuestionPayloadSnapshot{QuestionType: "mcq", PayloadJSON: `{}`}, nil
}

func btEnv(eventID string) events.EventEnvelope {
	return events.EventEnvelope{
		EventID:     eventID,
		TenantID:    btTenant,
		GCID:        btAuthor,
		Traceparent: "00-abc-def-01",
		OccurredAt:  time.Now().UTC(),
	}
}

func btPayload() subscribers.QuestionBatchAcceptedPayload {
	return subscribers.QuestionBatchAcceptedPayload{
		JobID:       btJobID,
		HostAtomID:  "01985e7f-cccc-7000-8000-000000000001",
		TenantID:    btTenant,
		AuthorGCID:  btAuthor,
		Title:       "Algebra unit test",
		Description: "Composed from chapter-3.pdf",
		Items: []subscribers.QuestionBatchItem{
			{QuestionAtomID: btAtom2, QuestionID: btQ2, QuestionType: "oe", Points: 20, DisplayOrder: 2},
			{QuestionAtomID: btAtom1, QuestionID: btQ1, QuestionType: "mcq", Points: 5, DisplayOrder: 1},
		},
	}
}

func newBatchSubscriber(store *fakeAssemblyStore, snap domain.QuestionSnapshotter) (*subscribers.BatchTestSetSubscriber, *events.InMemoryPublisher) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	sub := subscribers.NewBatchTestSetSubscriber(store, snap, pub, idempotent.NewMemoryStore())
	return sub, pub
}

func TestBatchTestSet_AssemblesDraftWithCuratedOrder(t *testing.T) {
	store := newFakeAssemblyStore()
	snap := &fakeSnapshotter{}
	sub, pub := newBatchSubscriber(store, snap)

	if err := sub.Handle(context.Background(), btEnv("evt-1"), btPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("expected 1 Save; got %d", len(store.saved))
	}
	ts := store.saved[0]
	if ts.State != domain.TestSetStateDraft {
		t.Fatalf("state: got %s want DRAFT (D3 — publish stays manual)", ts.State)
	}
	if ts.TenantID != btTenant || ts.AuthorGCID != btAuthor {
		t.Fatalf("tenant/author: %s/%s", ts.TenantID, ts.AuthorGCID)
	}
	if ts.Title != "Algebra unit test" || ts.Description != "Composed from chapter-3.pdf" {
		t.Fatalf("title/desc: %q/%q", ts.Title, ts.Description)
	}
	if ts.SourceJobID == nil || *ts.SourceJobID != btJobID {
		t.Fatalf("SourceJobID: %v", ts.SourceJobID)
	}
	qs := ts.Questions()
	if len(qs) != 2 {
		t.Fatalf("expected 2 questions; got %d", len(qs))
	}
	// Questions() orders by display_order asc — curated order preserved.
	if qs[0].QuestionAtomID != btAtom1 || qs[0].DisplayOrder != 1 || qs[0].Points != 5 || qs[0].QuestionType != "mcq" {
		t.Fatalf("q1 mismatch: %+v", qs[0])
	}
	if qs[1].QuestionAtomID != btAtom2 || qs[1].DisplayOrder != 2 || qs[1].Points != 20 || qs[1].QuestionType != "oe" {
		t.Fatalf("q2 mismatch: %+v", qs[1])
	}
	if qs[0].QuestionID != btQ1 || qs[1].QuestionID != btQ2 {
		t.Fatalf("question_id carry-through: %q/%q", qs[0].QuestionID, qs[1].QuestionID)
	}

	// REUSE: the existing test_set.created.v1 publisher path.
	evs := pub.History()
	found := false
	for _, ev := range evs {
		if ev.Topic == "chora.delivery.test_set.created.v1" {
			found = true
			if ev.Envelope.TenantID != btTenant {
				t.Fatalf("event tenant: %s", ev.Envelope.TenantID)
			}
		}
	}
	if !found {
		t.Fatalf("expected chora.delivery.test_set.created.v1; got %+v", evs)
	}
}

func TestBatchTestSet_DuplicateJobIsAckNoOp(t *testing.T) {
	store := newFakeAssemblyStore()
	sub, pub := newBatchSubscriber(store, &fakeSnapshotter{})

	if err := sub.Handle(context.Background(), btEnv("evt-1"), btPayload()); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	// Redelivery with a DIFFERENT event id (so the event-level inbox cannot
	// mask the source_job_id existence check).
	if err := sub.Handle(context.Background(), btEnv("evt-2"), btPayload()); err != nil {
		t.Fatalf("duplicate Handle must ACK (nil); got %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("duplicate delivery must not save a second set; got %d saves", len(store.saved))
	}
	created := 0
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.test_set.created.v1" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("duplicate delivery must not re-emit created.v1; got %d", created)
	}
}

func TestBatchTestSet_MissingQuestionSkippedRestAssembled(t *testing.T) {
	store := newFakeAssemblyStore()
	snap := &fakeSnapshotter{missing: map[string]bool{btQ1: true}}
	sub, _ := newBatchSubscriber(store, snap)

	if err := sub.Handle(context.Background(), btEnv("evt-1"), btPayload()); err != nil {
		t.Fatalf("Handle must assemble the rest on a missing question; got %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("expected 1 Save; got %d", len(store.saved))
	}
	qs := store.saved[0].Questions()
	if len(qs) != 1 {
		t.Fatalf("missing question must be skipped; got %d items", len(qs))
	}
	if qs[0].QuestionID != btQ2 {
		t.Fatalf("surviving item: %q", qs[0].QuestionID)
	}
}

func TestBatchTestSet_TransientSnapshotterErrorNacks(t *testing.T) {
	store := newFakeAssemblyStore()
	snap := &fakeSnapshotter{broken: map[string]bool{btQ2: true}}
	sub, _ := newBatchSubscriber(store, snap)

	if err := sub.Handle(context.Background(), btEnv("evt-1"), btPayload()); err == nil {
		t.Fatal("transient snapshotter failure must return error (Nack → retry)")
	}
	if len(store.saved) != 0 {
		t.Fatalf("no partial save on transient failure; got %d", len(store.saved))
	}
}

func TestBatchTestSet_NilSnapshotterSkipsValidation(t *testing.T) {
	store := newFakeAssemblyStore()
	sub, _ := newBatchSubscriber(store, nil)

	if err := sub.Handle(context.Background(), btEnv("evt-1"), btPayload()); err != nil {
		t.Fatalf("nil snapshotter (dev wiring) must assemble unvalidated: %v", err)
	}
	if len(store.saved) != 1 || len(store.saved[0].Questions()) != 2 {
		t.Fatal("expected full assembly without validation")
	}
}

func TestBatchTestSet_ZeroPointsDefaultsTo10(t *testing.T) {
	store := newFakeAssemblyStore()
	sub, _ := newBatchSubscriber(store, &fakeSnapshotter{})

	p := btPayload()
	p.Items[1].Points = 0 // proto3 absent ⇒ composer default fallback (D5)
	if err := sub.Handle(context.Background(), btEnv("evt-1"), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	qs := store.saved[0].Questions()
	if qs[0].Points != 10 {
		t.Fatalf("absent points must default to 10; got %v", qs[0].Points)
	}
}

func TestBatchTestSet_InvalidItemTypeSkipped(t *testing.T) {
	store := newFakeAssemblyStore()
	sub, _ := newBatchSubscriber(store, &fakeSnapshotter{})

	p := btPayload()
	p.Items[0].QuestionType = "jigsaw" // permanently-bad item — skip, not Nack
	if err := sub.Handle(context.Background(), btEnv("evt-1"), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := len(store.saved[0].Questions()); got != 1 {
		t.Fatalf("invalid-type item must be skipped; got %d items", got)
	}
}

func TestBatchTestSet_RequiredFieldsRejected(t *testing.T) {
	store := newFakeAssemblyStore()
	sub, _ := newBatchSubscriber(store, &fakeSnapshotter{})

	noJob := btPayload()
	noJob.JobID = ""
	if err := sub.Handle(context.Background(), btEnv("evt-1"), noJob); err == nil {
		t.Fatal("missing job_id must error")
	}

	noTenant := btPayload()
	noTenant.TenantID = ""
	env := btEnv("evt-2")
	env.TenantID = ""
	if err := sub.Handle(context.Background(), env, noTenant); err == nil {
		t.Fatal("missing tenant must error")
	}

	// Envelope fallback: payload tenant empty but envelope carries it — OK.
	envOK := btEnv("evt-3")
	pl := btPayload()
	pl.TenantID = ""
	pl.AuthorGCID = ""
	if err := sub.Handle(context.Background(), envOK, pl); err != nil {
		t.Fatalf("envelope tenant/gcid fallback must assemble: %v", err)
	}
	if got := store.saved[0].AuthorGCID; got != btAuthor {
		t.Fatalf("author from envelope gcid: %q", got)
	}
}

func TestBatchTestSet_SaveUniqueRaceLost_AckNoOp(t *testing.T) {
	store := newFakeAssemblyStore()
	// First Save errors (unique violation at the 0028 partial index); by the
	// re-check, the winner's row is visible.
	winner, _ := domain.NewTestSet(domain.NewTestSetInput{
		TenantID: btTenant, AuthorGCID: btAuthor, Title: "winner", SourceJobID: btJobID,
	})
	store.saveErrs = []error{errors.New(`duplicate key value violates unique constraint "idx_test_sets_source_job_id"`)}
	sub, pub := newBatchSubscriber(store, &fakeSnapshotter{})

	// Concurrent winner lands between the existence check (miss #1) and
	// Save (23505); the post-Save re-check must find it and ACK.
	store.installAfterMisses(1, winner)

	if err := sub.Handle(context.Background(), btEnv("evt-1"), btPayload()); err != nil {
		t.Fatalf("losing the unique race must ACK no-op; got %v", err)
	}
	if len(store.saved) != 0 {
		t.Fatalf("loser must not record a save; got %d", len(store.saved))
	}
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.test_set.created.v1" {
			t.Fatal("loser must not emit created.v1")
		}
	}
}

func TestBatchTestSet_SaveErrorWithoutWinnerNacks(t *testing.T) {
	store := newFakeAssemblyStore()
	store.saveErrs = []error{errors.New("pg: connection reset")}
	sub, _ := newBatchSubscriber(store, &fakeSnapshotter{})

	if err := sub.Handle(context.Background(), btEnv("evt-1"), btPayload()); err == nil {
		t.Fatal("save failure without a winner row must Nack (error)")
	}
}

func TestBatchTestSet_SubscribedTopic(t *testing.T) {
	sub, _ := newBatchSubscriber(newFakeAssemblyStore(), nil)
	if got := sub.SubscribedTopic(); got != "chora.creation.question_batch.accepted.v1" {
		t.Fatalf("SubscribedTopic: %q", got)
	}
}

func TestBatchTestSet_WithTTL(t *testing.T) {
	sub, _ := newBatchSubscriber(newFakeAssemblyStore(), nil)
	if got := sub.WithTTL(time.Hour); got != sub {
		t.Fatal("WithTTL must return the receiver")
	}
	if got := sub.WithTTL(0); got != sub {
		t.Fatal("WithTTL(0) must no-op and return the receiver")
	}
	var nilSub *subscribers.BatchTestSetSubscriber
	if got := nilSub.WithTTL(time.Hour); got != nil {
		t.Fatal("nil receiver must pass through")
	}
}

// ADR-229 WS-2 (CHO-2133) — the batch existence probe carries the composing
// author as the reuse actor, so creation's snapshot gate evaluates consent
// for the REAL caller (not a legacy empty gcid).
func TestBatchTestSet_ThreadsAuthorAsSnapshotCaller(t *testing.T) {
	store := newFakeAssemblyStore()
	snap := &fakeSnapshotter{}
	sub, _ := newBatchSubscriber(store, snap)

	if err := sub.Handle(context.Background(), btEnv("evt-actor-1"), btPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(snap.callers) != 2 {
		t.Fatalf("expected 2 snapshot probes; got %d", len(snap.callers))
	}
	for i, c := range snap.callers {
		if c != btAuthor {
			t.Errorf("probe %d caller = %q; want %q (the batch author)", i, c, btAuthor)
		}
	}
}
