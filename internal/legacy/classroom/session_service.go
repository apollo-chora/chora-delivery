package classroom

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SessionService manages classroom session lifecycle, discussions,
// kiosk configuration, learner profiles, and session transactions.
type SessionService struct {
	sessions     ClassroomSessionRepository
	discussions  DiscussionThreadRepository
	kiosks       KioskConfigRepository
	learnerProfs LearnerClassProfileRepository
	transactions SessionTransactionRepository
	events       EventPublisher
}

// NewSessionService creates a SessionService with the given repositories
// and event publisher.
func NewSessionService(
	sessions ClassroomSessionRepository,
	discussions DiscussionThreadRepository,
	kiosks KioskConfigRepository,
	learnerProfs LearnerClassProfileRepository,
	transactions SessionTransactionRepository,
	events EventPublisher,
) *SessionService {
	return &SessionService{
		sessions:     sessions,
		discussions:  discussions,
		kiosks:       kiosks,
		learnerProfs: learnerProfs,
		transactions: transactions,
		events:       events,
	}
}

// ---------------------------------------------------------------------------
// Classroom Session Lifecycle (51.2.1)
// ---------------------------------------------------------------------------

// CreateSession validates and persists a new ClassroomSession.
func (s *SessionService) CreateSession(ctx context.Context, session *ClassroomSession) (*ClassroomSession, error) {
	if session.Title == "" {
		return nil, fmt.Errorf("title must not be empty: %w", ErrValidationFailed)
	}
	if !session.SessionType.IsValid() {
		return nil, fmt.Errorf("invalid session type %q: %w", session.SessionType, ErrValidationFailed)
	}

	now := time.Now().UTC()
	session.ID = uuid.Must(uuid.NewV7())
	session.Status = SessionStatusScheduled
	session.CreatedAt = now
	session.UpdatedAt = now

	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, err
	}

	return session, nil
}

// GetSession retrieves a classroom session by ID and tenant.
func (s *SessionService) GetSession(ctx context.Context, id, tenantID uuid.UUID) (*ClassroomSession, error) {
	session, err := s.sessions.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}
	return session, nil
}

// ListSessions returns all sessions for a tenant.
func (s *SessionService) ListSessions(ctx context.Context, tenantID uuid.UUID) ([]ClassroomSession, error) {
	return s.sessions.ListByTenant(ctx, tenantID)
}

// StartSession transitions a scheduled session to live status.
func (s *SessionService) StartSession(ctx context.Context, id, tenantID uuid.UUID) (*ClassroomSession, error) {
	session, err := s.sessions.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}

	if session.Status == SessionStatusEnded {
		return nil, ErrSessionAlreadyEnded
	}
	if session.Status == SessionStatusLive {
		return nil, ErrSessionAlreadyLive
	}

	now := time.Now().UTC()
	session.Status = SessionStatusLive
	session.StartedAt = &now
	session.UpdatedAt = now

	if err := s.sessions.Update(ctx, session); err != nil {
		return nil, err
	}

	// Record transaction.
	s.recordTransaction(ctx, session.ID, session.TenantID, session.InstructorID, TransactionTypeQuizStart, nil)

	evt := NewDomainEvent(
		EventSessionStarted,
		session.TenantID,
		&session.InstructorID,
		session.ID,
		AggregateClassroomSession,
		map[string]interface{}{
			"session_id":   session.ID.String(),
			"title":        session.Title,
			"session_type": string(session.SessionType),
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish session.started event: %w", err)
	}

	return session, nil
}

// EndSession transitions a live session to ended status.
func (s *SessionService) EndSession(ctx context.Context, id, tenantID uuid.UUID) (*ClassroomSession, error) {
	session, err := s.sessions.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}

	if session.Status == SessionStatusEnded {
		return nil, ErrSessionAlreadyEnded
	}
	if session.Status == SessionStatusScheduled {
		return nil, fmt.Errorf("cannot end a session that has not started: %w", ErrInvalidStateTransition)
	}

	now := time.Now().UTC()
	session.Status = SessionStatusEnded
	session.EndedAt = &now
	session.UpdatedAt = now

	if err := s.sessions.Update(ctx, session); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventSessionEnded,
		session.TenantID,
		&session.InstructorID,
		session.ID,
		AggregateClassroomSession,
		map[string]interface{}{
			"session_id": session.ID.String(),
			"title":      session.Title,
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish session.ended event: %w", err)
	}

	return session, nil
}

// CreateSessionQuiz creates a LiveQuizSession linked to a ClassroomSession.
func (s *SessionService) CreateSessionQuiz(ctx context.Context, sessionID, tenantID uuid.UUID, quizSessionID uuid.UUID) (*ClassroomSession, error) {
	session, err := s.sessions.GetByID(ctx, sessionID, tenantID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}

	session.QuizSessionID = &quizSessionID
	session.UpdatedAt = time.Now().UTC()

	if err := s.sessions.Update(ctx, session); err != nil {
		return nil, err
	}

	return session, nil
}

// ---------------------------------------------------------------------------
// Discussions (51.2.3)
// ---------------------------------------------------------------------------

// CreateDiscussion creates a new discussion thread in a classroom session.
func (s *SessionService) CreateDiscussion(ctx context.Context, sessionID, tenantID, authorID uuid.UUID, content string) (*DiscussionThread, error) {
	session, err := s.sessions.GetByID(ctx, sessionID, tenantID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}

	if content == "" {
		return nil, fmt.Errorf("content must not be empty: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	thread := &DiscussionThread{
		ID:        uuid.Must(uuid.NewV7()),
		TenantID:  tenantID,
		SessionID: sessionID,
		AuthorID:  authorID,
		Content:   content,
		Pinned:    false,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.discussions.Create(ctx, thread); err != nil {
		return nil, err
	}

	// Record transaction.
	s.recordTransaction(ctx, sessionID, tenantID, authorID, TransactionTypeMessage, map[string]interface{}{
		"thread_id": thread.ID.String(),
	})

	evt := NewDomainEvent(
		EventDiscussionPosted,
		tenantID,
		&authorID,
		sessionID,
		AggregateClassroomSession,
		map[string]interface{}{
			"session_id": sessionID.String(),
			"thread_id":  thread.ID.String(),
			"author_id":  authorID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish discussion.posted event: %w", err)
	}

	return thread, nil
}

// ListDiscussions returns all discussion threads for a session.
func (s *SessionService) ListDiscussions(ctx context.Context, sessionID, tenantID uuid.UUID) ([]DiscussionThread, error) {
	session, err := s.sessions.GetByID(ctx, sessionID, tenantID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}

	return s.discussions.ListBySession(ctx, sessionID)
}

// ReplyToDiscussion creates a reply to an existing discussion thread.
func (s *SessionService) ReplyToDiscussion(ctx context.Context, sessionID, tenantID, threadID, authorID uuid.UUID, content string) (*DiscussionThread, error) {
	parent, err := s.discussions.GetByID(ctx, threadID, tenantID)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, ErrDiscussionNotFound
	}

	if content == "" {
		return nil, fmt.Errorf("content must not be empty: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	reply := &DiscussionThread{
		ID:        uuid.Must(uuid.NewV7()),
		TenantID:  tenantID,
		SessionID: sessionID,
		AuthorID:  authorID,
		Content:   content,
		ParentID:  &threadID,
		Pinned:    false,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.discussions.Create(ctx, reply); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventDiscussionPosted,
		tenantID,
		&authorID,
		sessionID,
		AggregateClassroomSession,
		map[string]interface{}{
			"session_id": sessionID.String(),
			"thread_id":  reply.ID.String(),
			"author_id":  authorID.String(),
			"parent_id":  threadID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish discussion.posted event: %w", err)
	}

	return reply, nil
}

// PinDiscussion pins or unpins a discussion thread.
func (s *SessionService) PinDiscussion(ctx context.Context, sessionID, tenantID, threadID uuid.UUID, pinned bool) (*DiscussionThread, error) {
	thread, err := s.discussions.GetByID(ctx, threadID, tenantID)
	if err != nil {
		return nil, err
	}
	if thread == nil {
		return nil, ErrDiscussionNotFound
	}

	thread.Pinned = pinned
	thread.UpdatedAt = time.Now().UTC()

	if err := s.discussions.Update(ctx, thread); err != nil {
		return nil, err
	}

	if pinned {
		// Record pin transaction.
		s.recordTransaction(ctx, sessionID, tenantID, thread.AuthorID, TransactionTypePin, map[string]interface{}{
			"thread_id": threadID.String(),
		})
	}

	return thread, nil
}

// ---------------------------------------------------------------------------
// Kiosk Config (51.2.4)
// ---------------------------------------------------------------------------

// UpsertKioskConfig creates or updates the kiosk configuration for a session.
func (s *SessionService) UpsertKioskConfig(ctx context.Context, sessionID, tenantID uuid.UUID, displayURL string, autoAdvance bool, advanceIntervalSeconds *int, contentFilter map[string]interface{}) (*KioskConfig, error) {
	session, err := s.sessions.GetByID(ctx, sessionID, tenantID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}

	if displayURL == "" {
		return nil, fmt.Errorf("display_url must not be empty: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()

	existing, err := s.kiosks.GetBySessionID(ctx, sessionID, tenantID)
	if err != nil {
		return nil, err
	}

	var config *KioskConfig
	if existing != nil {
		existing.DisplayURL = displayURL
		existing.AutoAdvance = autoAdvance
		existing.AdvanceIntervalSeconds = advanceIntervalSeconds
		existing.ContentFilter = contentFilter
		existing.UpdatedAt = now
		config = existing
	} else {
		config = &KioskConfig{
			ID:                     uuid.Must(uuid.NewV7()),
			TenantID:               tenantID,
			SessionID:              sessionID,
			DisplayURL:             displayURL,
			AutoAdvance:            autoAdvance,
			AdvanceIntervalSeconds: advanceIntervalSeconds,
			ContentFilter:          contentFilter,
			CreatedAt:              now,
			UpdatedAt:              now,
		}
	}

	if err := s.kiosks.Save(ctx, config); err != nil {
		return nil, err
	}

	return config, nil
}

// GetKioskConfig retrieves the kiosk configuration for a session.
func (s *SessionService) GetKioskConfig(ctx context.Context, sessionID, tenantID uuid.UUID) (*KioskConfig, error) {
	config, err := s.kiosks.GetBySessionID(ctx, sessionID, tenantID)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, ErrKioskConfigNotFound
	}
	return config, nil
}

// ---------------------------------------------------------------------------
// Learner Class Profile (51.2.5)
// ---------------------------------------------------------------------------

// CreateLearnerProfile creates a learner social identity for a classroom session.
func (s *SessionService) CreateLearnerProfile(ctx context.Context, sessionID, tenantID, learnerID uuid.UUID, displayName string, avatarURL, bio *string) (*LearnerClassProfile, error) {
	session, err := s.sessions.GetByID(ctx, sessionID, tenantID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}

	if displayName == "" {
		return nil, fmt.Errorf("display_name must not be empty: %w", ErrValidationFailed)
	}

	// Check if profile already exists.
	existing, err := s.learnerProfs.GetBySessionAndLearner(ctx, sessionID, learnerID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	now := time.Now().UTC()
	profile := &LearnerClassProfile{
		ID:          uuid.Must(uuid.NewV7()),
		TenantID:    tenantID,
		LearnerID:   learnerID,
		SessionID:   sessionID,
		DisplayName: displayName,
		AvatarURL:   avatarURL,
		Bio:         bio,
		JoinedAt:    now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.learnerProfs.Create(ctx, profile); err != nil {
		return nil, err
	}

	// Record join transaction.
	s.recordTransaction(ctx, sessionID, tenantID, learnerID, TransactionTypeJoin, map[string]interface{}{
		"display_name": displayName,
	})

	return profile, nil
}

// GetLearnerProfile retrieves the learner profile for a session.
func (s *SessionService) GetLearnerProfile(ctx context.Context, sessionID, tenantID, learnerID uuid.UUID) (*LearnerClassProfile, error) {
	profile, err := s.learnerProfs.GetBySessionAndLearner(ctx, sessionID, learnerID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, ErrLearnerProfileNotFound
	}
	return profile, nil
}

// UpdateLearnerProfile updates the learner's social identity for a session.
func (s *SessionService) UpdateLearnerProfile(ctx context.Context, sessionID, tenantID, learnerID uuid.UUID, displayName string, avatarURL, bio *string) (*LearnerClassProfile, error) {
	profile, err := s.learnerProfs.GetBySessionAndLearner(ctx, sessionID, learnerID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, ErrLearnerProfileNotFound
	}

	if displayName == "" {
		return nil, fmt.Errorf("display_name must not be empty: %w", ErrValidationFailed)
	}

	profile.DisplayName = displayName
	profile.AvatarURL = avatarURL
	profile.Bio = bio
	profile.UpdatedAt = time.Now().UTC()

	if err := s.learnerProfs.Update(ctx, profile); err != nil {
		return nil, err
	}

	return profile, nil
}

// ---------------------------------------------------------------------------
// Session Transactions (51.2.6)
// ---------------------------------------------------------------------------

// ListTransactions returns all transactions for a session.
func (s *SessionService) ListTransactions(ctx context.Context, sessionID, tenantID uuid.UUID) ([]SessionTransaction, error) {
	session, err := s.sessions.GetByID(ctx, sessionID, tenantID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}

	return s.transactions.ListBySession(ctx, sessionID)
}

// recordTransaction is an internal helper that appends a transaction to the audit log.
func (s *SessionService) recordTransaction(ctx context.Context, sessionID, tenantID, actorID uuid.UUID, txType TransactionType, payload map[string]interface{}) {
	tx := &SessionTransaction{
		ID:              uuid.Must(uuid.NewV7()),
		TenantID:        tenantID,
		SessionID:       sessionID,
		ActorID:         actorID,
		TransactionType: txType,
		Payload:         payload,
		CreatedAt:       time.Now().UTC(),
	}
	// Best-effort — transaction recording should not block the main operation.
	_ = s.transactions.Create(ctx, tx)
}
