// coverage_test.go — closes the remaining statement gaps in the course_content
// service + aggregate: Service.AddItem's non-NotFound/New-error branches,
// RemoveItem/Reorder domain-error propagation, persistAndPublish's save/publish
// failure paths, and the aggregate Reorder-on-deleted guard.
package course_content

import (
	"context"
	"errors"
	"testing"
)

// errGetRepo returns a fixed error from Get but delegates everything else to the
// shared fakeRepo (Save is a no-op that still counts calls).
type errGetRepo struct {
	*fakeRepo
	err error
}

func (r *errGetRepo) Get(_ context.Context, _, _ string) (*CourseContent, error) {
	return nil, r.err
}

// errSaveRepo fails every Save.
type errSaveRepo struct {
	*fakeRepo
}

func (r *errSaveRepo) Save(_ context.Context, _ *CourseContent) error {
	return errors.New("db write failed")
}

// errPub fails every publish.
type errPub struct {
	*fakePublisher
}

func (p *errPub) PublishContentComposed(_ context.Context, _ string, _ *CourseContent) error {
	return errors.New("broker down")
}

func TestService_AddItem_RepoErrorsPropagate(t *testing.T) {
	boom := errors.New("db down")
	svc := NewService(&errGetRepo{fakeRepo: newFakeRepo(), err: boom}, &fakePublisher{})
	if _, err := svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "x"}); !errors.Is(err, boom) {
		t.Fatalf("non-NotFound repo error: want %v, got %v", boom, err)
	}
}

func TestService_AddItem_NewErrorPropagates(t *testing.T) {
	// Empty store → Get returns ErrNotFound → New runs with the SAME (blank)
	// keys and fails loudly rather than fabricating a curriculum.
	svc, _, _ := newSvc()
	if _, err := svc.AddItem(context.Background(), "", "", "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("constructor error: want ErrInvalidArgument, got %v", err)
	}
}

func TestService_RemoveItem_DomainErrorPropagates(t *testing.T) {
	svc, repo, pub := newSvc()
	if _, err := svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.RemoveItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", "ghost"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("want ErrItemNotFound, got %v", err)
	}
	if repo.saveCalls != 1 || len(pub.published) != 1 {
		t.Fatalf("a rejected remove must not save/publish: saves=%d pubs=%d", repo.saveCalls, len(pub.published))
	}
}

func TestService_Reorder_InvalidPermutationPropagates(t *testing.T) {
	svc, repo, pub := newSvc()
	if _, err := svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.Reorder(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", []string{"ghost"}); !errors.Is(err, ErrInvalidReorder) {
		t.Fatalf("want ErrInvalidReorder, got %v", err)
	}
	if repo.saveCalls != 1 || len(pub.published) != 1 {
		t.Fatalf("a rejected reorder must not save/publish: saves=%d pubs=%d", repo.saveCalls, len(pub.published))
	}
}

func TestService_Reorder_CourseNotFound(t *testing.T) {
	svc, _, _ := newSvc()
	if _, err := svc.Reorder(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", []string{"x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestService_SaveFailure_Propagates(t *testing.T) {
	svc := NewService(&errSaveRepo{fakeRepo: newFakeRepo()}, &fakePublisher{})
	if _, err := svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"}); err == nil {
		t.Fatalf("save failure must propagate, got nil")
	}
}

func TestService_PublishFailure_Propagates(t *testing.T) {
	svc := NewService(newFakeRepo(), &errPub{fakePublisher: &fakePublisher{}})
	if _, err := svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"}); err == nil {
		t.Fatalf("publish failure must propagate (fail-loud), got nil")
	}
}

func TestReorder_OnDeletedModule(t *testing.T) {
	cc := mustNew(t)
	it, _ := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"})
	if err := cc.SoftDelete(); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if err := cc.Reorder([]string{it.ItemID}); !errors.Is(err, ErrDeleted) {
		t.Fatalf("reorder on deleted want ErrDeleted, got %v", err)
	}
}
