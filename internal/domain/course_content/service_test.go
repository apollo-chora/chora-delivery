package course_content

import (
	"context"
	"errors"
	"testing"
)

// ---- fakes ----

type fakeRepo struct {
	store     map[string]*CourseContent
	saveCalls int
}

func newFakeRepo() *fakeRepo { return &fakeRepo{store: map[string]*CourseContent{}} }

func (r *fakeRepo) Get(_ context.Context, tenantID, courseID string) (*CourseContent, error) {
	cc, ok := r.store[tenantID+"/"+courseID]
	if !ok || cc.DeletedAt != nil {
		return nil, ErrNotFound
	}
	return cc, nil
}

func (r *fakeRepo) Save(_ context.Context, cc *CourseContent) error {
	r.saveCalls++
	r.store[cc.TenantID+"/"+cc.CourseID] = cc
	return nil
}

type fakePublisher struct {
	published []*CourseContent
}

func (p *fakePublisher) PublishContentComposed(_ context.Context, _ string, cc *CourseContent) error {
	// snapshot item count at publish time
	p.published = append(p.published, cc)
	return nil
}

func newSvc() (*Service, *fakeRepo, *fakePublisher) {
	repo := newFakeRepo()
	pub := &fakePublisher{}
	return NewService(repo, pub), repo, pub
}

// ---- tests ----

func TestService_AddItem_AutoCreatesAndPublishes(t *testing.T) {
	svc, repo, pub := newSvc()
	cc, err := svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{
		Kind: KindAtom, Ref: atomRef, Title: "Intro",
	})
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if len(cc.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(cc.Items))
	}
	if repo.saveCalls != 1 {
		t.Fatalf("expected 1 save, got %d", repo.saveCalls)
	}
	if len(pub.published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub.published))
	}
	if len(pub.published[0].Items) != 1 {
		t.Fatalf("published payload should carry full curriculum")
	}
}

func TestService_AddItem_AppendsToExisting(t *testing.T) {
	svc, _, pub := newSvc()
	if _, err := svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	cc, err := svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindVideo, Ref: "https://x/v.mp4", Title: "b"})
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if len(cc.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(cc.Items))
	}
	if len(pub.published) != 2 {
		t.Fatalf("expected 2 publishes, got %d", len(pub.published))
	}
}

func TestService_AddItem_PropagatesDomainError_NoPublish(t *testing.T) {
	svc, repo, pub := newSvc()
	if _, err := svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: "bogus", Ref: atomRef, Title: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("want ErrInvalidArgument, got %v", err)
	}
	if repo.saveCalls != 0 || len(pub.published) != 0 {
		t.Fatalf("invalid add must not save or publish")
	}
}

func TestService_RemoveItem(t *testing.T) {
	svc, _, pub := newSvc()
	cc, _ := svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"})
	itemID := cc.Items[0].ItemID
	out, err := svc.RemoveItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", itemID)
	if err != nil {
		t.Fatalf("RemoveItem: %v", err)
	}
	if len(out.Items) != 0 {
		t.Fatalf("expected 0 items after remove")
	}
	// add(1) + remove(1) = 2 publishes
	if len(pub.published) != 2 {
		t.Fatalf("expected 2 publishes, got %d", len(pub.published))
	}
}

func TestService_RemoveItem_CourseNotFound(t *testing.T) {
	svc, _, _ := newSvc()
	if _, err := svc.RemoveItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestService_Reorder(t *testing.T) {
	svc, _, _ := newSvc()
	cc, _ := svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"})
	cc, _ = svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindVideo, Ref: "https://x/v.mp4", Title: "b"})
	a, b := cc.Items[0].ItemID, cc.Items[1].ItemID
	out, err := svc.Reorder(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", []string{b, a})
	if err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if out.Items[0].ItemID != b || out.Items[1].ItemID != a {
		t.Fatalf("reorder not applied")
	}
}

func TestService_Get(t *testing.T) {
	svc, _, _ := newSvc()
	if _, err := svc.Get(context.Background(), tenant, courseID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty Get: want ErrNotFound, got %v", err)
	}
	_, _ = svc.AddItem(context.Background(), tenant, courseID, "00000000-0000-7000-8000-0000000000ac", AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"})
	got, err := svc.Get(context.Background(), tenant, courseID)
	if err != nil {
		t.Fatalf("Get after add: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 item")
	}
}
