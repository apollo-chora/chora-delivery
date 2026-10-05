package course_content

import (
	"errors"
	"strings"
	"testing"
)

const (
	tenant   = "11111111-1111-7111-8111-111111111111"
	courseID = "019e30db-692f-7d10-8ce0-59669fe9298d"
	atomRef  = "019e30db-0000-7000-8000-0000000000a1"
	tsRef    = "019e30db-0000-7000-8000-0000000000b2"
	clsRef   = "019e30db-0000-7000-8000-0000000000c3"
)

// ---- constructor ----

func TestNew_Valid(t *testing.T) {
	cc, err := New(NewParams{CourseID: courseID, TenantID: tenant})
	if err != nil {
		t.Fatalf("New: unexpected err %v", err)
	}
	if cc.CourseID != courseID || cc.TenantID != tenant {
		t.Fatalf("New: ids not set: %+v", cc)
	}
	if len(cc.Items) != 0 {
		t.Fatalf("New: expected empty items, got %d", len(cc.Items))
	}
	if cc.CreatedAt.IsZero() || cc.UpdatedAt.IsZero() {
		t.Fatalf("New: timestamps not set")
	}
}

func TestNew_RejectsMissingIDs(t *testing.T) {
	if _, err := New(NewParams{CourseID: "", TenantID: tenant}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing course_id: want ErrInvalidArgument, got %v", err)
	}
	if _, err := New(NewParams{CourseID: courseID, TenantID: ""}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing tenant_id: want ErrInvalidArgument, got %v", err)
	}
}

// ---- Kind ----

func TestKind_Valid(t *testing.T) {
	for _, k := range []Kind{KindAtom, KindVideo, KindYouTube, KindDocument, KindLiveClassroom, KindAssessment} {
		if !k.Valid() {
			t.Fatalf("kind %q should be valid", k)
		}
	}
	if Kind("podcast").Valid() {
		t.Fatalf("unknown kind should be invalid")
	}
}

// ---- AddItem: per-kind ref validation ----

func TestAddItem_AllKinds(t *testing.T) {
	cc := mustNew(t)
	cases := []struct {
		kind Kind
		ref  string
	}{
		{KindAtom, atomRef},
		{KindAssessment, tsRef},
		{KindLiveClassroom, clsRef},
		{KindVideo, "https://cdn.chora.site/v/intro.mp4"},
		{KindYouTube, "https://www.youtube.com/watch?v=abc123"},
		{KindDocument, "https://docs.chora.site/handbook.pdf"},
	}
	for i, c := range cases {
		it, err := cc.AddItem(AddItemParams{Kind: c.kind, Ref: c.ref, Title: "Item"})
		if err != nil {
			t.Fatalf("AddItem(%s): unexpected err %v", c.kind, err)
		}
		if it.Position != i {
			t.Fatalf("AddItem(%s): position want %d got %d", c.kind, i, it.Position)
		}
		if it.ItemID == "" {
			t.Fatalf("AddItem(%s): item_id not minted", c.kind)
		}
		if it.CourseID != courseID {
			t.Fatalf("AddItem(%s): course_id not propagated", c.kind)
		}
	}
	if len(cc.Items) != len(cases) {
		t.Fatalf("expected %d items, got %d", len(cases), len(cc.Items))
	}
}

func TestAddItem_RejectsInvalidKind(t *testing.T) {
	cc := mustNew(t)
	if _, err := cc.AddItem(AddItemParams{Kind: "podcast", Ref: atomRef, Title: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("invalid kind: want ErrInvalidArgument, got %v", err)
	}
}

func TestAddItem_RejectsEmptyRefAndTitle(t *testing.T) {
	cc := mustNew(t)
	if _, err := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: "  ", Title: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty ref: want ErrInvalidArgument, got %v", err)
	}
	if _, err := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "   "}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty title: want ErrInvalidArgument, got %v", err)
	}
}

func TestAddItem_RejectsTitleTooLong(t *testing.T) {
	cc := mustNew(t)
	long := strings.Repeat("a", MaxTitleLength+1)
	if _, err := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: atomRef, Title: long}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("title too long: want ErrInvalidArgument, got %v", err)
	}
}

func TestAddItem_RefShapePerKind(t *testing.T) {
	cc := mustNew(t)
	// atom/assessment/live_classroom require a UUID ref
	if _, err := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: "not-a-uuid", Title: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("atom non-uuid ref: want ErrInvalidArgument, got %v", err)
	}
	// video/youtube/document require an http(s) URL
	if _, err := cc.AddItem(AddItemParams{Kind: KindVideo, Ref: "ftp://x/y", Title: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("video non-http ref: want ErrInvalidArgument, got %v", err)
	}
}

func TestAddItem_AcceptsGsUriForUploadedMedia(t *testing.T) {
	cc := mustNew(t)
	// video + document accept a gs:// ref minted by the upload-url endpoint.
	for _, c := range []struct {
		kind Kind
		ref  string
	}{
		{KindVideo, "gs://chora-delivery-course-media-dev/tenants/" + tenant + "/courses/" + courseID + "/019e30db-0000-7000-8000-0000000000d4.mp4"},
		{KindDocument, "gs://chora-delivery-course-media-dev/tenants/" + tenant + "/courses/" + courseID + "/019e30db-0000-7000-8000-0000000000e5.pdf"},
	} {
		if _, err := cc.AddItem(AddItemParams{Kind: c.kind, Ref: c.ref, Title: "Uploaded"}); err != nil {
			t.Fatalf("AddItem(%s, gs://): unexpected err %v", c.kind, err)
		}
	}
}

func TestAddItem_RejectsGsUriForUuidKindsAndYoutube(t *testing.T) {
	cc := mustNew(t)
	gs := "gs://chora-delivery-course-media-dev/tenants/x/courses/y/z.mp4"
	// atom/assessment/live_classroom still require a UUID — gs:// is rejected.
	if _, err := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: gs, Title: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("atom gs:// ref: want ErrInvalidArgument, got %v", err)
	}
	// youtube is always a watch URL, never an uploaded blob.
	if _, err := cc.AddItem(AddItemParams{Kind: KindYouTube, Ref: gs, Title: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("youtube gs:// ref: want ErrInvalidArgument, got %v", err)
	}
}

func TestAddItem_RejectsMalformedGsUri(t *testing.T) {
	cc := mustNew(t)
	// gs:// with a bucket but no object key is malformed.
	if _, err := cc.AddItem(AddItemParams{Kind: KindVideo, Ref: "gs://bucket-only", Title: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bucket-only gs:// ref: want ErrInvalidArgument, got %v", err)
	}
	if _, err := cc.AddItem(AddItemParams{Kind: KindVideo, Ref: "gs:///no-bucket.mp4", Title: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("no-bucket gs:// ref: want ErrInvalidArgument, got %v", err)
	}
}

func TestAddItem_RejectsDuplicate(t *testing.T) {
	cc := mustNew(t)
	if _, err := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "x"}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "dup"}); !errors.Is(err, ErrDuplicateItem) {
		t.Fatalf("dup (kind,ref): want ErrDuplicateItem, got %v", err)
	}
	// same ref but different kind is NOT a duplicate
	if _, err := cc.AddItem(AddItemParams{Kind: KindAssessment, Ref: atomRef, Title: "ok"}); err != nil {
		t.Fatalf("same ref different kind should be allowed: %v", err)
	}
}

func TestAddItem_CapExceeded(t *testing.T) {
	cc := mustNew(t)
	cc.Items = make([]*ContentItem, MaxItemsPerCourse)
	if _, err := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "x"}); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("cap exceeded: want ErrCapExceeded, got %v", err)
	}
}

// ---- RemoveItem ----

func TestRemoveItem_CompactsPositions(t *testing.T) {
	cc := mustNew(t)
	a, _ := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"})
	_, _ = cc.AddItem(AddItemParams{Kind: KindVideo, Ref: "https://x/v.mp4", Title: "b"})
	c, _ := cc.AddItem(AddItemParams{Kind: KindDocument, Ref: "https://x/d.pdf", Title: "c"})
	if err := cc.RemoveItem(a.ItemID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(cc.Items) != 2 {
		t.Fatalf("expected 2 items after remove, got %d", len(cc.Items))
	}
	for i, it := range cc.Items {
		if it.Position != i {
			t.Fatalf("positions not compacted: item %d has position %d", i, it.Position)
		}
	}
	if cc.Items[1].ItemID != c.ItemID {
		t.Fatalf("unexpected order after remove")
	}
}

func TestRemoveItem_NotFound(t *testing.T) {
	cc := mustNew(t)
	if err := cc.RemoveItem("nope"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("remove missing: want ErrItemNotFound, got %v", err)
	}
}

// ---- Reorder ----

func TestReorder_Valid(t *testing.T) {
	cc := mustNew(t)
	a, _ := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"})
	b, _ := cc.AddItem(AddItemParams{Kind: KindVideo, Ref: "https://x/v.mp4", Title: "b"})
	c, _ := cc.AddItem(AddItemParams{Kind: KindDocument, Ref: "https://x/d.pdf", Title: "c"})
	if err := cc.Reorder([]string{c.ItemID, a.ItemID, b.ItemID}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	want := []string{c.ItemID, a.ItemID, b.ItemID}
	for i, it := range cc.Items {
		if it.ItemID != want[i] || it.Position != i {
			t.Fatalf("reorder mismatch at %d: got %s pos %d", i, it.ItemID, it.Position)
		}
	}
}

func TestReorder_RejectsBadPermutation(t *testing.T) {
	cc := mustNew(t)
	a, _ := cc.AddItem(AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"})
	_, _ = cc.AddItem(AddItemParams{Kind: KindVideo, Ref: "https://x/v.mp4", Title: "b"})
	// wrong length
	if err := cc.Reorder([]string{a.ItemID}); !errors.Is(err, ErrInvalidReorder) {
		t.Fatalf("short reorder: want ErrInvalidReorder, got %v", err)
	}
	// unknown id
	if err := cc.Reorder([]string{a.ItemID, "ghost"}); !errors.Is(err, ErrInvalidReorder) {
		t.Fatalf("unknown id reorder: want ErrInvalidReorder, got %v", err)
	}
	// duplicate id
	if err := cc.Reorder([]string{a.ItemID, a.ItemID}); !errors.Is(err, ErrInvalidReorder) {
		t.Fatalf("dup id reorder: want ErrInvalidReorder, got %v", err)
	}
}

// ---- SoftDelete blocks mutation ----

func TestSoftDelete_BlocksMutation(t *testing.T) {
	cc := mustNew(t)
	_, _ = cc.AddItem(AddItemParams{Kind: KindAtom, Ref: atomRef, Title: "a"})
	if err := cc.SoftDelete(); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if cc.IsActive() {
		t.Fatalf("expected inactive after soft delete")
	}
	if _, err := cc.AddItem(AddItemParams{Kind: KindVideo, Ref: "https://x/v.mp4", Title: "b"}); !errors.Is(err, ErrDeleted) {
		t.Fatalf("add after delete: want ErrDeleted, got %v", err)
	}
	if err := cc.RemoveItem("x"); !errors.Is(err, ErrDeleted) {
		t.Fatalf("remove after delete: want ErrDeleted, got %v", err)
	}
	// idempotent
	if err := cc.SoftDelete(); err != nil {
		t.Fatalf("second soft delete should be no-op: %v", err)
	}
}

// ---- helpers ----

func mustNew(t *testing.T) *CourseContent {
	t.Helper()
	cc, err := New(NewParams{CourseID: courseID, TenantID: tenant})
	if err != nil {
		t.Fatalf("mustNew: %v", err)
	}
	return cc
}
