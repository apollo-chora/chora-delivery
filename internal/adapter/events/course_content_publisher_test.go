// course_content_publisher_test.go — adapter tests for the CHO-1612
// course-curriculum emitter (external package, mirroring the sibling
// exam_result_publisher_test.go style).
package events_test

import (
	"context"
	"testing"

	cc "github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

func TestCourseContentPublisher_PublishContentComposed(t *testing.T) {
	inner := cc.NewInMemoryPublisher("chora-489812", "chora-delivery")
	pub := cc.NewCourseContentPublisher(inner)

	c := &course_content.CourseContent{
		TenantID: "tenant-1",
		CourseID: "course-1",
		Items: []*course_content.ContentItem{
			{ItemID: "i1", Kind: course_content.KindAtom, Ref: "atom-1", Title: "Intro", Position: 1},
			{ItemID: "i2", Kind: course_content.KindAssessment, Ref: "ts-2", Title: "Quiz", Position: 2},
		},
	}
	if err := pub.PublishContentComposed(context.Background(), "gcid-actor", c); err != nil {
		t.Fatalf("PublishContentComposed: %v", err)
	}

	hist := inner.History()
	if len(hist) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(hist))
	}
	ev := hist[0]
	if ev.Topic != cc.TopicCourseContentComposed {
		t.Fatalf("topic = %q, want %q", ev.Topic, cc.TopicCourseContentComposed)
	}
	if ev.Envelope.TenantID != "tenant-1" || ev.Envelope.GCID != "gcid-actor" {
		t.Fatalf("envelope tenant/gcid = %q/%q", ev.Envelope.TenantID, ev.Envelope.GCID)
	}
	if ev.Payload["tenant_id"] != "tenant-1" || ev.Payload["course_id"] != "course-1" {
		t.Fatalf("payload tenant/course = %v/%v", ev.Payload["tenant_id"], ev.Payload["course_id"])
	}
	items, ok := ev.Payload["items"].([]map[string]any)
	if !ok {
		t.Fatalf("payload items = %T, want []map[string]any", ev.Payload["items"])
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	first := items[0]
	if first["item_id"] != "i1" || first["kind"] != "atom" || first["ref"] != "atom-1" ||
		first["title"] != "Intro" || first["position"] != 1 {
		t.Fatalf("item[0] projection wrong: %v", first)
	}
	second := items[1]
	if second["item_id"] != "i2" || second["kind"] != "assessment" || second["position"] != 2 {
		t.Fatalf("item[1] projection wrong: %v", second)
	}
}

func TestCourseContentPublisher_PublishContentComposed_InnerWithoutCustomCapability(t *testing.T) {
	inner := cc.NewInMemoryPublisher("chora-489812", "chora-delivery")
	pub := cc.NewCourseContentPublisher(&noCustomPublisher{inner: inner})
	err := pub.PublishContentComposed(context.Background(), "gcid-actor", &course_content.CourseContent{
		TenantID: "t", CourseID: "c",
	})
	if err == nil || err.Error() == "" {
		t.Fatalf("expected capability error, got %v", err)
	}
	if got := len(inner.History()); got != 0 {
		t.Fatalf("no event must be published; got %d", got)
	}
}

func TestCourseContentPublisher_NewConstructs(t *testing.T) {
	pub := cc.NewCourseContentPublisher(cc.NewInMemoryPublisher("p", "s"))
	if pub == nil {
		t.Fatal("nil CourseContentPublisher")
	}
}

// TestExamResultPublisher_InnerWithoutCustomCapability — the ExamResultPublisher
// negative branch (validation + success paths covered in
// exam_result_publisher_test.go).
func TestExamResultPublisher_InnerWithoutCustomCapability(t *testing.T) {
	inner := cc.NewInMemoryPublisher("chora-489812", "chora-delivery")
	pub := cc.NewExamResultPublisher(&noCustomPublisher{inner: inner})
	err := pub.PublishExamResultReleased(context.Background(), "g", exam.ExamResultReleased{
		TenantID: "t", ResultID: "r",
	})
	if err == nil || err.Error() == "" {
		t.Fatalf("expected capability error, got %v", err)
	}
	if got := len(inner.History()); got != 0 {
		t.Fatalf("no event must be published; got %d", got)
	}
}
