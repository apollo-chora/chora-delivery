package classroom

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// testSessionSvcDeps wires a SessionService with all repos fresh.
type testSessionSvcDeps struct {
	svc          *SessionService
	sessions     *mockSessionRepo
	discussions  *mockDiscussionRepo
	kiosks       *mockKioskRepo
	learnerProfs *mockLearnerProfileRepo
	transactions *mockTransactionRepo
	publisher    *mockEventPublisher
}

func newTestSessionSvc() testSessionSvcDeps {
	sr := &mockSessionRepo{}
	dr := &mockDiscussionRepo{}
	kr := &mockKioskRepo{}
	lr := &mockLearnerProfileRepo{}
	tr := &mockTransactionRepo{}
	ep := &mockEventPublisher{}
	return testSessionSvcDeps{
		svc:          NewSessionService(sr, dr, kr, lr, tr, ep),
		sessions:     sr,
		discussions:  dr,
		kiosks:       kr,
		learnerProfs: lr,
		transactions: tr,
		publisher:    ep,
	}
}

// ---------------------------------------------------------------------------
// TestGetSession
// ---------------------------------------------------------------------------

func TestGetSession(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testSessionSvcDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testSessionSvcDeps) {
				d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID, TenantID: tenantID}, nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testSessionSvcDeps) {
				d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)
			},
			wantErr: ErrSessionNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testSessionSvcDeps) {
				d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSessionSvc()
			tc.setupMocks(d)

			got, err := d.svc.GetSession(context.Background(), sessionID, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}
			d.sessions.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListSessions
// ---------------------------------------------------------------------------

func TestListSessions(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	d := newTestSessionSvc()
	d.sessions.On("ListByTenant", mock.Anything, tenantID).Return([]ClassroomSession{
		{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID},
	}, nil)

	got, err := d.svc.ListSessions(context.Background(), tenantID)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.sessions.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestCreateSessionQuiz
// ---------------------------------------------------------------------------

func TestCreateSessionQuiz(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	quizSessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testSessionSvcDeps)
		wantErr    error
	}{
		{
			name: "success: links quiz session",
			setupMocks: func(d testSessionSvcDeps) {
				d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID, TenantID: tenantID}, nil)
				d.sessions.On("Update", mock.Anything, mock.AnythingOfType("*classroom.ClassroomSession")).Return(nil)
			},
		},
		{
			name: "fails: session not found",
			setupMocks: func(d testSessionSvcDeps) {
				d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)
			},
			wantErr: ErrSessionNotFound,
		},
		{
			name: "fails: update error",
			setupMocks: func(d testSessionSvcDeps) {
				d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
				d.sessions.On("Update", mock.Anything, mock.AnythingOfType("*classroom.ClassroomSession")).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSessionSvc()
			tc.setupMocks(d)

			got, err := d.svc.CreateSessionQuiz(context.Background(), sessionID, tenantID, quizSessionID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) {
					assert.NotNil(t, got.QuizSessionID)
					assert.Equal(t, quizSessionID, *got.QuizSessionID)
				}
			}
			d.sessions.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListDiscussions
// ---------------------------------------------------------------------------

func TestListDiscussions(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testSessionSvcDeps)
		wantErr    error
		wantLen    int
	}{
		{
			name: "success",
			setupMocks: func(d testSessionSvcDeps) {
				d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
				d.discussions.On("ListBySession", mock.Anything, sessionID).Return([]DiscussionThread{
					{ID: uuid.Must(uuid.NewV7()), SessionID: sessionID},
				}, nil)
			},
			wantLen: 1,
		},
		{
			name: "fails: session not found",
			setupMocks: func(d testSessionSvcDeps) {
				d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)
			},
			wantErr: ErrSessionNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSessionSvc()
			tc.setupMocks(d)

			got, err := d.svc.ListDiscussions(context.Background(), sessionID, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.Len(t, got, tc.wantLen)
			}
			d.sessions.AssertExpectations(t)
			d.discussions.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestReplyToDiscussion
// ---------------------------------------------------------------------------

func TestReplyToDiscussion(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	threadID := uuid.Must(uuid.NewV7())
	authorID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		content      string
		setupMocks   func(d testSessionSvcDeps)
		wantErr      error
		assertResult func(t *testing.T, got *DiscussionThread)
	}{
		{
			name:    "success: creates reply with parent id",
			content: "Reply body",
			setupMocks: func(d testSessionSvcDeps) {
				d.discussions.On("GetByID", mock.Anything, threadID, tenantID).Return(&DiscussionThread{ID: threadID, TenantID: tenantID}, nil)
				d.discussions.On("Create", mock.Anything, mock.AnythingOfType("*classroom.DiscussionThread")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *DiscussionThread) {
				assert.NotNil(t, got.ParentID)
				assert.Equal(t, threadID, *got.ParentID)
				assert.Equal(t, "Reply body", got.Content)
			},
		},
		{
			name:    "fails: parent not found",
			content: "Reply body",
			setupMocks: func(d testSessionSvcDeps) {
				d.discussions.On("GetByID", mock.Anything, threadID, tenantID).Return(nil, nil)
			},
			wantErr: ErrDiscussionNotFound,
		},
		{
			name:    "fails: empty content",
			content: "",
			setupMocks: func(d testSessionSvcDeps) {
				d.discussions.On("GetByID", mock.Anything, threadID, tenantID).Return(&DiscussionThread{ID: threadID}, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:    "fails: parent lookup error",
			content: "Reply body",
			setupMocks: func(d testSessionSvcDeps) {
				d.discussions.On("GetByID", mock.Anything, threadID, tenantID).Return(nil, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
		{
			name:    "fails: create error",
			content: "Reply body",
			setupMocks: func(d testSessionSvcDeps) {
				d.discussions.On("GetByID", mock.Anything, threadID, tenantID).Return(&DiscussionThread{ID: threadID}, nil)
				d.discussions.On("Create", mock.Anything, mock.AnythingOfType("*classroom.DiscussionThread")).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
		{
			name:    "fails: publish error",
			content: "Reply body",
			setupMocks: func(d testSessionSvcDeps) {
				d.discussions.On("GetByID", mock.Anything, threadID, tenantID).Return(&DiscussionThread{ID: threadID}, nil)
				d.discussions.On("Create", mock.Anything, mock.AnythingOfType("*classroom.DiscussionThread")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSessionSvc()
			tc.setupMocks(d)

			got, err := d.svc.ReplyToDiscussion(context.Background(), sessionID, tenantID, threadID, authorID, tc.content)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}
			d.discussions.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetKioskConfig
// ---------------------------------------------------------------------------

func TestGetKioskConfig(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testSessionSvcDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testSessionSvcDeps) {
				d.kiosks.On("GetBySessionID", mock.Anything, sessionID, tenantID).Return(&KioskConfig{
					ID: uuid.Must(uuid.NewV7()), SessionID: sessionID, DisplayURL: "https://x",
				}, nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testSessionSvcDeps) {
				d.kiosks.On("GetBySessionID", mock.Anything, sessionID, tenantID).Return(nil, nil)
			},
			wantErr: ErrKioskConfigNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testSessionSvcDeps) {
				d.kiosks.On("GetBySessionID", mock.Anything, sessionID, tenantID).Return(nil, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSessionSvc()
			tc.setupMocks(d)

			got, err := d.svc.GetKioskConfig(context.Background(), sessionID, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}
			d.kiosks.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetLearnerProfile
// ---------------------------------------------------------------------------

func TestGetLearnerProfile(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	learnerID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testSessionSvcDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testSessionSvcDeps) {
				d.learnerProfs.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(&LearnerClassProfile{
					ID: uuid.Must(uuid.NewV7()), SessionID: sessionID, LearnerID: learnerID, DisplayName: "Alice",
				}, nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testSessionSvcDeps) {
				d.learnerProfs.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(nil, nil)
			},
			wantErr: ErrLearnerProfileNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testSessionSvcDeps) {
				d.learnerProfs.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(nil, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSessionSvc()
			tc.setupMocks(d)

			got, err := d.svc.GetLearnerProfile(context.Background(), sessionID, tenantID, learnerID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}
			d.learnerProfs.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestUpdateLearnerProfile
// ---------------------------------------------------------------------------

func TestUpdateLearnerProfile(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	learnerID := uuid.Must(uuid.NewV7())

	bio := ptrStr("bio")
	avatar := ptrStr("avatar")

	tests := []struct {
		name         string
		displayName  string
		setupMocks   func(d testSessionSvcDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LearnerClassProfile)
	}{
		{
			name:        "success: updates profile",
			displayName: "Bob",
			setupMocks: func(d testSessionSvcDeps) {
				d.learnerProfs.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(&LearnerClassProfile{
					ID: uuid.Must(uuid.NewV7()), DisplayName: "Alice",
				}, nil)
				d.learnerProfs.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LearnerClassProfile")).Return(nil)
			},
			assertResult: func(t *testing.T, got *LearnerClassProfile) {
				assert.Equal(t, "Bob", got.DisplayName)
				assert.Equal(t, avatar, got.AvatarURL)
				assert.Equal(t, bio, got.Bio)
			},
		},
		{
			name:        "fails: profile not found",
			displayName: "Bob",
			setupMocks: func(d testSessionSvcDeps) {
				d.learnerProfs.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(nil, nil)
			},
			wantErr: ErrLearnerProfileNotFound,
		},
		{
			name:        "fails: empty display name",
			displayName: "",
			setupMocks: func(d testSessionSvcDeps) {
				d.learnerProfs.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(&LearnerClassProfile{ID: uuid.Must(uuid.NewV7())}, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:        "fails: update repo error",
			displayName: "Bob",
			setupMocks: func(d testSessionSvcDeps) {
				d.learnerProfs.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(&LearnerClassProfile{ID: uuid.Must(uuid.NewV7())}, nil)
				d.learnerProfs.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LearnerClassProfile")).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSessionSvc()
			tc.setupMocks(d)

			got, err := d.svc.UpdateLearnerProfile(context.Background(), sessionID, tenantID, learnerID, tc.displayName, avatar, bio)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}
			d.learnerProfs.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// Error-path top-ups for partially covered session functions
// ---------------------------------------------------------------------------

func TestStartSession_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	t.Run("already ended", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID, Status: SessionStatusEnded}, nil)
		_, err := d.svc.StartSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, ErrSessionAlreadyEnded)
		d.sessions.AssertExpectations(t)
	})

	t.Run("already live", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID, Status: SessionStatusLive}, nil)
		_, err := d.svc.StartSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, ErrSessionAlreadyLive)
		d.sessions.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID, Status: SessionStatusScheduled}, nil)
		d.sessions.On("Update", mock.Anything, mock.AnythingOfType("*classroom.ClassroomSession")).Return(errTestClassroom)
		_, err := d.svc.StartSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{
			ID: sessionID, TenantID: tenantID, Status: SessionStatusScheduled, InstructorID: uuid.Must(uuid.NewV7()),
		}, nil)
		d.sessions.On("Update", mock.Anything, mock.AnythingOfType("*classroom.ClassroomSession")).Return(nil)
		d.transactions.On("Create", mock.Anything, mock.AnythingOfType("*classroom.SessionTransaction")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
		_, err := d.svc.StartSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
		d.transactions.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}

func TestEndSession_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	t.Run("already ended", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID, Status: SessionStatusEnded}, nil)
		_, err := d.svc.EndSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, ErrSessionAlreadyEnded)
		d.sessions.AssertExpectations(t)
	})

	t.Run("not started", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID, Status: SessionStatusScheduled}, nil)
		_, err := d.svc.EndSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, ErrInvalidStateTransition)
		d.sessions.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID, Status: SessionStatusLive}, nil)
		d.sessions.On("Update", mock.Anything, mock.AnythingOfType("*classroom.ClassroomSession")).Return(errTestClassroom)
		_, err := d.svc.EndSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{
			ID: sessionID, TenantID: tenantID, Status: SessionStatusLive, InstructorID: uuid.Must(uuid.NewV7()),
		}, nil)
		d.sessions.On("Update", mock.Anything, mock.AnythingOfType("*classroom.ClassroomSession")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
		_, err := d.svc.EndSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}

func TestCreateDiscussion_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	authorID := uuid.Must(uuid.NewV7())

	t.Run("session not found", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)
		_, err := d.svc.CreateDiscussion(context.Background(), sessionID, tenantID, authorID, "hello")
		assert.ErrorIs(t, err, ErrSessionNotFound)
		d.sessions.AssertExpectations(t)
	})

	t.Run("empty content", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
		_, err := d.svc.CreateDiscussion(context.Background(), sessionID, tenantID, authorID, "")
		assert.ErrorIs(t, err, ErrValidationFailed)
		d.sessions.AssertExpectations(t)
	})

	t.Run("create repo error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
		d.discussions.On("Create", mock.Anything, mock.AnythingOfType("*classroom.DiscussionThread")).Return(errTestClassroom)
		_, err := d.svc.CreateDiscussion(context.Background(), sessionID, tenantID, authorID, "hello")
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
		d.discussions.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
		d.discussions.On("Create", mock.Anything, mock.AnythingOfType("*classroom.DiscussionThread")).Return(nil)
		d.transactions.On("Create", mock.Anything, mock.AnythingOfType("*classroom.SessionTransaction")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
		_, err := d.svc.CreateDiscussion(context.Background(), sessionID, tenantID, authorID, "hello")
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
		d.discussions.AssertExpectations(t)
		d.transactions.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}

func TestPinDiscussion_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	threadID := uuid.Must(uuid.NewV7())
	authorID := uuid.Must(uuid.NewV7())

	t.Run("not found", func(t *testing.T) {
		d := newTestSessionSvc()
		d.discussions.On("GetByID", mock.Anything, threadID, tenantID).Return(nil, nil)
		_, err := d.svc.PinDiscussion(context.Background(), sessionID, tenantID, threadID, true)
		assert.ErrorIs(t, err, ErrDiscussionNotFound)
		d.discussions.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.discussions.On("GetByID", mock.Anything, threadID, tenantID).Return(&DiscussionThread{ID: threadID, AuthorID: authorID}, nil)
		d.discussions.On("Update", mock.Anything, mock.AnythingOfType("*classroom.DiscussionThread")).Return(errTestClassroom)
		_, err := d.svc.PinDiscussion(context.Background(), sessionID, tenantID, threadID, true)
		assert.ErrorIs(t, err, errTestClassroom)
		d.discussions.AssertExpectations(t)
	})

	t.Run("unpin does not record transaction", func(t *testing.T) {
		d := newTestSessionSvc()
		d.discussions.On("GetByID", mock.Anything, threadID, tenantID).Return(&DiscussionThread{ID: threadID, Pinned: true}, nil)
		d.discussions.On("Update", mock.Anything, mock.AnythingOfType("*classroom.DiscussionThread")).Return(nil)
		got, err := d.svc.PinDiscussion(context.Background(), sessionID, tenantID, threadID, false)
		assert.NoError(t, err)
		assert.False(t, got.Pinned)
		d.discussions.AssertExpectations(t)
		d.transactions.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	})
}

func TestUpsertKioskConfig_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	t.Run("session not found", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)
		_, err := d.svc.UpsertKioskConfig(context.Background(), sessionID, tenantID, "https://x", true, nil, nil)
		assert.ErrorIs(t, err, ErrSessionNotFound)
		d.sessions.AssertExpectations(t)
	})

	t.Run("empty display url", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
		_, err := d.svc.UpsertKioskConfig(context.Background(), sessionID, tenantID, "", true, nil, nil)
		assert.ErrorIs(t, err, ErrValidationFailed)
		d.sessions.AssertExpectations(t)
	})

	t.Run("kiosk lookup error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
		d.kiosks.On("GetBySessionID", mock.Anything, sessionID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.UpsertKioskConfig(context.Background(), sessionID, tenantID, "https://x", true, nil, nil)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
		d.kiosks.AssertExpectations(t)
	})

	t.Run("save error on update", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
		d.kiosks.On("GetBySessionID", mock.Anything, sessionID, tenantID).Return(&KioskConfig{ID: uuid.Must(uuid.NewV7())}, nil)
		d.kiosks.On("Save", mock.Anything, mock.AnythingOfType("*classroom.KioskConfig")).Return(errTestClassroom)
		_, err := d.svc.UpsertKioskConfig(context.Background(), sessionID, tenantID, "https://x", true, nil, nil)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
		d.kiosks.AssertExpectations(t)
	})

	t.Run("existing config is updated", func(t *testing.T) {
		d := newTestSessionSvc()
		existingID := uuid.Must(uuid.NewV7())
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
		d.kiosks.On("GetBySessionID", mock.Anything, sessionID, tenantID).Return(&KioskConfig{ID: existingID, DisplayURL: "old"}, nil)
		d.kiosks.On("Save", mock.Anything, mock.AnythingOfType("*classroom.KioskConfig")).Return(nil)
		got, err := d.svc.UpsertKioskConfig(context.Background(), sessionID, tenantID, "https://new", false, nil, nil)
		assert.NoError(t, err)
		assert.Equal(t, existingID, got.ID)
		assert.Equal(t, "https://new", got.DisplayURL)
		assert.False(t, got.AutoAdvance)
		d.sessions.AssertExpectations(t)
		d.kiosks.AssertExpectations(t)
	})
}

func TestCreateLearnerProfile_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	learnerID := uuid.Must(uuid.NewV7())

	t.Run("session not found", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)
		_, err := d.svc.CreateLearnerProfile(context.Background(), sessionID, tenantID, learnerID, "Alice", nil, nil)
		assert.ErrorIs(t, err, ErrSessionNotFound)
		d.sessions.AssertExpectations(t)
	})

	t.Run("empty display name", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
		_, err := d.svc.CreateLearnerProfile(context.Background(), sessionID, tenantID, learnerID, "", nil, nil)
		assert.ErrorIs(t, err, ErrValidationFailed)
		d.sessions.AssertExpectations(t)
	})

	t.Run("existing profile returned", func(t *testing.T) {
		d := newTestSessionSvc()
		existing := &LearnerClassProfile{ID: uuid.Must(uuid.NewV7()), DisplayName: "Existing"}
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
		d.learnerProfs.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(existing, nil)
		got, err := d.svc.CreateLearnerProfile(context.Background(), sessionID, tenantID, learnerID, "Alice", nil, nil)
		assert.NoError(t, err)
		assert.Equal(t, existing, got)
		d.sessions.AssertExpectations(t)
		d.learnerProfs.AssertExpectations(t)
	})

	t.Run("profile lookup error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
		d.learnerProfs.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(nil, errTestClassroom)
		_, err := d.svc.CreateLearnerProfile(context.Background(), sessionID, tenantID, learnerID, "Alice", nil, nil)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
		d.learnerProfs.AssertExpectations(t)
	})

	t.Run("create repo error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(&ClassroomSession{ID: sessionID}, nil)
		d.learnerProfs.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(nil, nil)
		d.learnerProfs.On("Create", mock.Anything, mock.AnythingOfType("*classroom.LearnerClassProfile")).Return(errTestClassroom)
		_, err := d.svc.CreateLearnerProfile(context.Background(), sessionID, tenantID, learnerID, "Alice", nil, nil)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
		d.learnerProfs.AssertExpectations(t)
	})
}

func TestListTransactions_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestSessionSvc()
	d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)

	_, err := d.svc.ListTransactions(context.Background(), sessionID, tenantID)
	assert.ErrorIs(t, err, ErrSessionNotFound)
	d.sessions.AssertExpectations(t)
}

func ptrStr(s string) *string { return &s }
