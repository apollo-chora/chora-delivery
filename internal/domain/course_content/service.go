package course_content

import "context"

// Service orchestrates CourseContent mutations: load-or-create the aggregate,
// apply the domain method, persist, then publish the full ordered curriculum.
// It holds NO business rules itself — those live on the aggregate.
type Service struct {
	repo Repository
	pub  Publisher
}

// NewService wires the repository + publisher ports.
func NewService(repo Repository, pub Publisher) *Service {
	return &Service{repo: repo, pub: pub}
}

// Get returns the curriculum for a course (ErrNotFound if none exists).
func (s *Service) Get(ctx context.Context, tenantID, courseID string) (*CourseContent, error) {
	return s.repo.Get(ctx, tenantID, courseID)
}

// AddItem appends a typed content item, auto-creating the curriculum on first
// add. Persists + publishes on success. actorGCID is the composing instructor.
func (s *Service) AddItem(ctx context.Context, tenantID, courseID, actorGCID string, p AddItemParams) (*CourseContent, error) {
	cc, err := s.repo.Get(ctx, tenantID, courseID)
	if err != nil {
		if err != ErrNotFound {
			return nil, err
		}
		cc, err = New(NewParams{CourseID: courseID, TenantID: tenantID})
		if err != nil {
			return nil, err
		}
	}
	if _, err := cc.AddItem(p); err != nil {
		return nil, err
	}
	return s.persistAndPublish(ctx, actorGCID, cc)
}

// RemoveItem drops an item by id. ErrNotFound if the course has no curriculum.
func (s *Service) RemoveItem(ctx context.Context, tenantID, courseID, actorGCID, itemID string) (*CourseContent, error) {
	cc, err := s.repo.Get(ctx, tenantID, courseID)
	if err != nil {
		return nil, err
	}
	if err := cc.RemoveItem(itemID); err != nil {
		return nil, err
	}
	return s.persistAndPublish(ctx, actorGCID, cc)
}

// Reorder applies an explicit full permutation. ErrNotFound if no curriculum.
func (s *Service) Reorder(ctx context.Context, tenantID, courseID, actorGCID string, orderedItemIDs []string) (*CourseContent, error) {
	cc, err := s.repo.Get(ctx, tenantID, courseID)
	if err != nil {
		return nil, err
	}
	if err := cc.Reorder(orderedItemIDs); err != nil {
		return nil, err
	}
	return s.persistAndPublish(ctx, actorGCID, cc)
}

// persistAndPublish saves the aggregate then publishes the full curriculum.
// A publish failure is returned to the caller (fail-loud) — the save has
// already committed, so the outbox/republish path is the recovery mechanism.
func (s *Service) persistAndPublish(ctx context.Context, actorGCID string, cc *CourseContent) (*CourseContent, error) {
	if err := s.repo.Save(ctx, cc); err != nil {
		return nil, err
	}
	if err := s.pub.PublishContentComposed(ctx, actorGCID, cc); err != nil {
		return nil, err
	}
	return cc, nil
}
