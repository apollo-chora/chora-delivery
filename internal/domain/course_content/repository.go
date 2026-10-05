package course_content

import "context"

// Repository persists CourseContent aggregates. Implementations live in the
// adapter layer (inmem for tests/MVP, pg for production). Per ddd-enforcement
// the repository is the ONLY persistence boundary.
type Repository interface {
	// Get returns the curriculum for a course, or ErrNotFound if none exists
	// yet. Soft-deleted aggregates are treated as not-found.
	Get(ctx context.Context, tenantID, courseID string) (*CourseContent, error)
	// Save upserts the aggregate (and its ordered child items).
	Save(ctx context.Context, cc *CourseContent) error
}

// Publisher emits chora.delivery.course.content_composed.v1 carrying the full
// ordered curriculum after every mutation, so downstream consumers
// (chora-consumption) project it idempotently. Implementations wrap the
// delivery events.Publisher's generic PublishCustom path.
//
// NOTE on cross-DB: atoms live in chora_creation — delivery CANNOT validate
// atom existence (cross-DB queries forbidden, ddd-enforcement). The domain
// shape-validates the ref (UUID vs URL); existence is the author's
// responsibility and is eventually consistent via the publish/project flow.
type Publisher interface {
	// PublishContentComposed emits the full curriculum. actorGCID is the
	// instructor who composed it (carried as the event envelope gcid; the
	// outbox gcid column is UUID-typed so it must be a valid GCID, not empty).
	PublishContentComposed(ctx context.Context, actorGCID string, cc *CourseContent) error
}
