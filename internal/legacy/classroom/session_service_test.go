package classroom

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// CreateSession tests
// ---------------------------------------------------------------------------

func TestSessionService_CreateSession_Success(t *testing.T) {
	sessionRepo := new(mockSessionRepo)
	discussionRepo := new(mockDiscussionRepo)
	kioskRepo := new(mockKioskRepo)
	learnerRepo := new(mockLearnerProfileRepo)
	txRepo := new(mockTransactionRepo)
	eventPub := new(mockEventPublisher)

	svc := NewSessionService(sessionRepo, discussionRepo, kioskRepo, learnerRepo, txRepo, eventPub)

	tenantID := uuid.Must(uuid.NewV7())
	instructorID := uuid.Must(uuid.NewV7())

	session := &ClassroomSession{
		TenantID:     tenantID,
		Title:        "Go Workshop",
		InstructorID: instructorID,
		SessionType:  SessionTypeWorkshop,
	}

	sessionRepo.On("Create", mock.Anything, mock.AnythingOfType("*classroom.ClassroomSession")).Return(nil)

	result, err := svc.CreateSession(context.Background(), session)
	require.NoError(t, err)
	assert.Equal(t, "Go Workshop", result.Title)
	assert.Equal(t, SessionStatusScheduled, result.Status)
	assert.NotEqual(t, uuid.Nil, result.ID)
}

func TestSessionService_CreateSession_EmptyTitle(t *testing.T) {
	svc := NewSessionService(nil, nil, nil, nil, nil, nil)

	session := &ClassroomSession{
		SessionType: SessionTypeLecture,
	}

	_, err := svc.CreateSession(context.Background(), session)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

func TestSessionService_CreateSession_InvalidType(t *testing.T) {
	svc := NewSessionService(nil, nil, nil, nil, nil, nil)

	session := &ClassroomSession{
		Title:       "Test",
		SessionType: "invalid",
	}

	_, err := svc.CreateSession(context.Background(), session)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// StartSession tests
// ---------------------------------------------------------------------------

func TestSessionService_StartSession_Success(t *testing.T) {
	sessionRepo := new(mockSessionRepo)
	txRepo := new(mockTransactionRepo)
	eventPub := new(mockEventPublisher)

	svc := NewSessionService(sessionRepo, nil, nil, nil, txRepo, eventPub)

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	instructorID := uuid.Must(uuid.NewV7())

	existing := &ClassroomSession{
		ID:           sessionID,
		TenantID:     tenantID,
		Title:        "Workshop",
		InstructorID: instructorID,
		SessionType:  SessionTypeWorkshop,
		Status:       SessionStatusScheduled,
	}

	sessionRepo.On("GetByID", mock.Anything, sessionID, tenantID).Return(existing, nil)
	sessionRepo.On("Update", mock.Anything, mock.AnythingOfType("*classroom.ClassroomSession")).Return(nil)
	txRepo.On("Create", mock.Anything, mock.AnythingOfType("*classroom.SessionTransaction")).Return(nil)
	eventPub.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)

	result, err := svc.StartSession(context.Background(), sessionID, tenantID)
	require.NoError(t, err)
	assert.Equal(t, SessionStatusLive, result.Status)
	assert.NotNil(t, result.StartedAt)
}

func TestSessionService_StartSession_AlreadyLive(t *testing.T) {
	sessionRepo := new(mockSessionRepo)
	svc := NewSessionService(sessionRepo, nil, nil, nil, nil, nil)

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	existing := &ClassroomSession{
		ID:       sessionID,
		TenantID: tenantID,
		Status:   SessionStatusLive,
	}

	sessionRepo.On("GetByID", mock.Anything, sessionID, tenantID).Return(existing, nil)

	_, err := svc.StartSession(context.Background(), sessionID, tenantID)
	assert.ErrorIs(t, err, ErrSessionAlreadyLive)
}

// ---------------------------------------------------------------------------
// EndSession tests
// ---------------------------------------------------------------------------

func TestSessionService_EndSession_Success(t *testing.T) {
	sessionRepo := new(mockSessionRepo)
	eventPub := new(mockEventPublisher)

	svc := NewSessionService(sessionRepo, nil, nil, nil, nil, eventPub)

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	instructorID := uuid.Must(uuid.NewV7())

	existing := &ClassroomSession{
		ID:           sessionID,
		TenantID:     tenantID,
		Title:        "Workshop",
		InstructorID: instructorID,
		Status:       SessionStatusLive,
	}

	sessionRepo.On("GetByID", mock.Anything, sessionID, tenantID).Return(existing, nil)
	sessionRepo.On("Update", mock.Anything, mock.AnythingOfType("*classroom.ClassroomSession")).Return(nil)
	eventPub.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)

	result, err := svc.EndSession(context.Background(), sessionID, tenantID)
	require.NoError(t, err)
	assert.Equal(t, SessionStatusEnded, result.Status)
	assert.NotNil(t, result.EndedAt)
}

// ---------------------------------------------------------------------------
// Discussion tests
// ---------------------------------------------------------------------------

func TestSessionService_CreateDiscussion_Success(t *testing.T) {
	sessionRepo := new(mockSessionRepo)
	discussionRepo := new(mockDiscussionRepo)
	txRepo := new(mockTransactionRepo)
	eventPub := new(mockEventPublisher)

	svc := NewSessionService(sessionRepo, discussionRepo, nil, nil, txRepo, eventPub)

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	authorID := uuid.Must(uuid.NewV7())

	sessionRepo.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{
		ID:       sessionID,
		TenantID: tenantID,
		Status:   SessionStatusLive,
	}, nil)
	discussionRepo.On("Create", mock.Anything, mock.AnythingOfType("*classroom.DiscussionThread")).Return(nil)
	txRepo.On("Create", mock.Anything, mock.AnythingOfType("*classroom.SessionTransaction")).Return(nil)
	eventPub.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)

	result, err := svc.CreateDiscussion(context.Background(), sessionID, tenantID, authorID, "Hello world")
	require.NoError(t, err)
	assert.Equal(t, "Hello world", result.Content)
	assert.False(t, result.Pinned)
	assert.Nil(t, result.ParentID)
}

func TestSessionService_PinDiscussion_Success(t *testing.T) {
	discussionRepo := new(mockDiscussionRepo)
	txRepo := new(mockTransactionRepo)

	svc := NewSessionService(nil, discussionRepo, nil, nil, txRepo, nil)

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	threadID := uuid.Must(uuid.NewV7())
	authorID := uuid.Must(uuid.NewV7())

	discussionRepo.On("GetByID", mock.Anything, threadID, tenantID).Return(&DiscussionThread{
		ID:       threadID,
		TenantID: tenantID,
		AuthorID: authorID,
		Pinned:   false,
	}, nil)
	discussionRepo.On("Update", mock.Anything, mock.AnythingOfType("*classroom.DiscussionThread")).Return(nil)
	txRepo.On("Create", mock.Anything, mock.AnythingOfType("*classroom.SessionTransaction")).Return(nil)

	result, err := svc.PinDiscussion(context.Background(), sessionID, tenantID, threadID, true)
	require.NoError(t, err)
	assert.True(t, result.Pinned)
}

// ---------------------------------------------------------------------------
// KioskConfig tests
// ---------------------------------------------------------------------------

func TestSessionService_UpsertKioskConfig_Create(t *testing.T) {
	sessionRepo := new(mockSessionRepo)
	kioskRepo := new(mockKioskRepo)

	svc := NewSessionService(sessionRepo, nil, kioskRepo, nil, nil, nil)

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	sessionRepo.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{
		ID:       sessionID,
		TenantID: tenantID,
	}, nil)
	kioskRepo.On("GetBySessionID", mock.Anything, sessionID, tenantID).Return(nil, nil)
	kioskRepo.On("Save", mock.Anything, mock.AnythingOfType("*classroom.KioskConfig")).Return(nil)

	result, err := svc.UpsertKioskConfig(context.Background(), sessionID, tenantID, "https://example.com", true, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com", result.DisplayURL)
	assert.True(t, result.AutoAdvance)
}

// ---------------------------------------------------------------------------
// LearnerProfile tests
// ---------------------------------------------------------------------------

func TestSessionService_CreateLearnerProfile_Success(t *testing.T) {
	sessionRepo := new(mockSessionRepo)
	learnerRepo := new(mockLearnerProfileRepo)
	txRepo := new(mockTransactionRepo)

	svc := NewSessionService(sessionRepo, nil, nil, learnerRepo, txRepo, nil)

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	learnerID := uuid.Must(uuid.NewV7())

	sessionRepo.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{
		ID:       sessionID,
		TenantID: tenantID,
	}, nil)
	learnerRepo.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(nil, nil)
	learnerRepo.On("Create", mock.Anything, mock.AnythingOfType("*classroom.LearnerClassProfile")).Return(nil)
	txRepo.On("Create", mock.Anything, mock.AnythingOfType("*classroom.SessionTransaction")).Return(nil)

	result, err := svc.CreateLearnerProfile(context.Background(), sessionID, tenantID, learnerID, "Alice", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "Alice", result.DisplayName)
}

// ---------------------------------------------------------------------------
// Transactions tests
// ---------------------------------------------------------------------------

func TestSessionService_ListTransactions_Success(t *testing.T) {
	sessionRepo := new(mockSessionRepo)
	txRepo := new(mockTransactionRepo)

	svc := NewSessionService(sessionRepo, nil, nil, nil, txRepo, nil)

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	sessionRepo.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{
		ID:       sessionID,
		TenantID: tenantID,
	}, nil)
	txRepo.On("ListBySession", mock.Anything, sessionID).Return([]SessionTransaction{
		{ID: uuid.Must(uuid.NewV7()), SessionID: sessionID, TransactionType: TransactionTypeJoin},
	}, nil)

	results, err := svc.ListTransactions(context.Background(), sessionID, tenantID)
	require.NoError(t, err)
	assert.Len(t, results, 1)
}
