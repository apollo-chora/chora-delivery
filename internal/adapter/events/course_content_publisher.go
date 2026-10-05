// course_content_publisher.go — adapter implementing the
// course_content.Publisher port (CHO-1612). Emits
// chora.delivery.course.content_composed.v1 via the generic PublishCustom
// outbox path (same lane as the assessment Lane-B + payments topics), carrying
// the FULL ordered curriculum so chora-consumption projects it idempotently.
//
// Wire format is JSON (no Schema Registry BINARY mode), consistent with the
// sibling chora.delivery.enrollment.created.v1; chora-consumption decodes via
// its protodecode JSON fallback.
package events

import (
	"context"
	"errors"
	"time"

	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
)

// TopicCourseContentComposed is the canonical topic for the course curriculum
// projection feed.
const TopicCourseContentComposed = "chora.delivery.course.content_composed.v1"

// customPublisher is the minimal generic-publish capability we need. Both
// InMemoryPublisher and CloudPublisher satisfy it (the latter tees to the
// outbox so the event actually reaches Pub/Sub).
type customPublisher interface {
	PublishCustom(topic, tenantID, gcid string, payload map[string]any) (PublishedEvent, error)
}

// CourseContentPublisher adapts a Publisher (asserted to the optional
// PublishCustom capability) to the course_content.Publisher domain port.
type CourseContentPublisher struct {
	inner Publisher
}

// NewCourseContentPublisher wraps the delivery Publisher. The concrete
// InMemoryPublisher + CloudPublisher both implement PublishCustom; the
// assertion happens at publish-time (mirrors payments_subscriber.go) so
// callers can pass the events.Publisher interface value directly.
func NewCourseContentPublisher(inner Publisher) *CourseContentPublisher {
	return &CourseContentPublisher{inner: inner}
}

// PublishContentComposed emits the full ordered curriculum for a course.
// actorGCID is stamped as the envelope gcid (UUID-typed in the outbox).
func (p *CourseContentPublisher) PublishContentComposed(_ context.Context, actorGCID string, c *cc.CourseContent) error {
	items := make([]map[string]any, 0, len(c.Items))
	for _, it := range c.Items {
		items = append(items, map[string]any{
			"item_id":  it.ItemID,
			"kind":     string(it.Kind),
			"ref":      it.Ref,
			"title":    it.Title,
			"position": it.Position,
		})
	}
	payload := map[string]any{
		"tenant_id":   c.TenantID, // carried in payload (mirrors enrollment.created)
		"course_id":   c.CourseID,
		"composed_at": time.Now().UTC().Format(time.RFC3339Nano),
		"items":       items,
	}
	pc, ok := p.inner.(customPublisher)
	if !ok {
		return errors.New("course_content_publisher: inner Publisher does not implement PublishCustom")
	}
	_, err := pc.PublishCustom(TopicCourseContentComposed, c.TenantID, actorGCID, payload)
	return err
}
