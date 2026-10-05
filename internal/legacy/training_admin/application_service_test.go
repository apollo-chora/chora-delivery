package training_admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestAppService() (*ApplicationService, *mockAppRepo, *mockRequestRepo, *mockSessionRepo, *mockEventPublisher) {
	appRepo := &mockAppRepo{}
	reqRepo := &mockRequestRepo{}
	sessRepo := &mockSessionRepo{}
	pub := &mockEventPublisher{}
	svc := NewApplicationService(appRepo, reqRepo, sessRepo, pub)
	return svc, appRepo, reqRepo, sessRepo, pub
}

func newTestRequestService() (*ApplicationService, *mockAppRepo, *mockRequestRepo, *mockSessionRepo, *mockEventPublisher) {
	return newTestAppService()
}

func ptrString(s string) *string { return &s }

// ---------------------------------------------------------------------------
// Part 1: ApplicationService — SubmitApplication
// ---------------------------------------------------------------------------

func TestSubmitApplication(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name    string
		input   *TrainingApplication
		setup   func(*mockAppRepo, *mockRequestRepo, *mockSessionRepo, *mockEventPublisher)
		wantErr error
	}{
		{
			name: "success: creates application with status=submitted and publishes event",
			input: &TrainingApplication{
				TenantID:          tenantID,
				GCID:              gcid,
				TrainingSessionID: sessionID,
				ApplicationText:   "I want to join this session",
			},
			setup: func(ar *mockAppRepo, _ *mockRequestRepo, sr *mockSessionRepo, ep *mockEventPublisher) {
				sr.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID: sessionID, TenantID: tenantID, EnrollmentOpen: true,
				}, nil)
				ar.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil)
				ep.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(nil)
			},
			wantErr: nil,
		},
		{
			name: "fails: empty training_session_id",
			input: &TrainingApplication{
				TenantID:          tenantID,
				GCID:              gcid,
				TrainingSessionID: uuid.Nil,
				ApplicationText:   "I want to join",
			},
			setup:   func(_ *mockAppRepo, _ *mockRequestRepo, _ *mockSessionRepo, _ *mockEventPublisher) {},
			wantErr: ErrValidationFailed,
		},
		{
			name: "fails: duplicate application (same gcid + session)",
			input: &TrainingApplication{
				TenantID:          tenantID,
				GCID:              gcid,
				TrainingSessionID: sessionID,
				ApplicationText:   "I want to join again",
			},
			setup: func(ar *mockAppRepo, _ *mockRequestRepo, sr *mockSessionRepo, _ *mockEventPublisher) {
				sr.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID: sessionID, TenantID: tenantID, EnrollmentOpen: true,
				}, nil)
				ar.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(ErrDuplicateApplication)
			},
			wantErr: ErrDuplicateApplication,
		},
		{
			name: "fails: enrollment not open for session",
			input: &TrainingApplication{
				TenantID:          tenantID,
				GCID:              gcid,
				TrainingSessionID: sessionID,
				ApplicationText:   "Please let me in",
			},
			setup: func(_ *mockAppRepo, _ *mockRequestRepo, sr *mockSessionRepo, _ *mockEventPublisher) {
				sr.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID: sessionID, TenantID: tenantID, EnrollmentOpen: false,
				}, nil)
			},
			wantErr: ErrEnrollmentClosed,
		},
		{
			name: "fails: repo error",
			input: &TrainingApplication{
				TenantID:          tenantID,
				GCID:              gcid,
				TrainingSessionID: sessionID,
				ApplicationText:   "I want to join",
			},
			setup: func(ar *mockAppRepo, _ *mockRequestRepo, sr *mockSessionRepo, _ *mockEventPublisher) {
				sr.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID: sessionID, TenantID: tenantID, EnrollmentOpen: true,
				}, nil)
				ar.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(errors.New("db connection lost"))
			},
			wantErr: errors.New("db connection lost"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, appRepo, reqRepo, sessRepo, pub := newTestAppService()
			tt.setup(appRepo, reqRepo, sessRepo, pub)

			result, err := svc.SubmitApplication(context.Background(), tt.input)

			if tt.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tt.wantErr, ErrValidationFailed) ||
					errors.Is(tt.wantErr, ErrDuplicateApplication) ||
					errors.Is(tt.wantErr, ErrEnrollmentClosed) {
					assert.ErrorIs(t, err, tt.wantErr)
				}
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, result) {
					assert.Equal(t, ApplicationStatusSubmitted, result.Status)
					assert.NotNil(t, result.SubmittedAt)
				}
				pub.AssertCalled(t, "Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent"))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Part 1: ApplicationService — GetApplication
// ---------------------------------------------------------------------------

func TestGetApplication(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	appID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name    string
		id      uuid.UUID
		setup   func(*mockAppRepo)
		wantErr error
	}{
		{
			name: "success: returns application",
			id:   appID,
			setup: func(ar *mockAppRepo) {
				ar.On("GetByID", mock.Anything, appID, tenantID).Return(&TrainingApplication{
					ID:       appID,
					TenantID: tenantID,
					Status:   ApplicationStatusSubmitted,
				}, nil)
			},
			wantErr: nil,
		},
		{
			name: "fails: not found",
			id:   appID,
			setup: func(ar *mockAppRepo) {
				ar.On("GetByID", mock.Anything, appID, tenantID).Return(nil, ErrNotFound)
			},
			wantErr: ErrNotFound,
		},
		{
			name: "fails: repo error",
			id:   appID,
			setup: func(ar *mockAppRepo) {
				ar.On("GetByID", mock.Anything, appID, tenantID).Return(nil, errors.New("db timeout"))
			},
			wantErr: errors.New("db timeout"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, appRepo, _, _, _ := newTestAppService()
			tt.setup(appRepo)

			result, err := svc.GetApplication(context.Background(), tt.id, tenantID)

			if tt.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tt.wantErr, ErrNotFound) {
					assert.ErrorIs(t, err, ErrNotFound)
				}
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, result) {
					assert.Equal(t, appID, result.ID)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Part 1: ApplicationService — ListApplications
// ---------------------------------------------------------------------------

func TestListApplications(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name    string
		setup   func(*mockAppRepo)
		wantErr error
		wantLen int
	}{
		{
			name: "success: returns list",
			setup: func(ar *mockAppRepo) {
				ar.On("List", mock.Anything, sessionID, tenantID, (*uuid.UUID)(nil), 20).Return([]TrainingApplication{
					{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, TrainingSessionID: sessionID},
					{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, TrainingSessionID: sessionID},
				}, nil)
			},
			wantErr: nil,
			wantLen: 2,
		},
		{
			name: "fails: repo error",
			setup: func(ar *mockAppRepo) {
				ar.On("List", mock.Anything, sessionID, tenantID, (*uuid.UUID)(nil), 20).Return(nil, errors.New("db error"))
			},
			wantErr: errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, appRepo, _, _, _ := newTestAppService()
			tt.setup(appRepo)

			result, err := svc.ListApplications(context.Background(), sessionID, tenantID, nil, 20)

			if tt.wantErr != nil {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, result, tt.wantLen)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Part 1: ApplicationService — DecideApplication
// ---------------------------------------------------------------------------

func TestDecideApplication(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	appID := uuid.Must(uuid.NewV7())
	reviewerGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		approved   bool
		reason     *string
		setup      func(*mockAppRepo, *mockEventPublisher)
		wantErr    error
		wantStatus ApplicationStatus
	}{
		{
			name:     "success: approve sets status=approved, reviewed_by/at, publishes event",
			id:       appID,
			approved: true,
			reason:   nil,
			setup: func(ar *mockAppRepo, ep *mockEventPublisher) {
				ar.On("GetByID", mock.Anything, appID, tenantID).Return(&TrainingApplication{
					ID:       appID,
					TenantID: tenantID,
					GCID:     reviewerGCID,
					Status:   ApplicationStatusSubmitted,
				}, nil)
				ar.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil)
				ep.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(nil)
			},
			wantErr:    nil,
			wantStatus: ApplicationStatusApproved,
		},
		{
			name:     "success: reject with reason sets status=rejected and rejection_reason",
			id:       appID,
			approved: false,
			reason:   ptrString("Does not meet prerequisites"),
			setup: func(ar *mockAppRepo, ep *mockEventPublisher) {
				ar.On("GetByID", mock.Anything, appID, tenantID).Return(&TrainingApplication{
					ID:       appID,
					TenantID: tenantID,
					GCID:     reviewerGCID,
					Status:   ApplicationStatusSubmitted,
				}, nil)
				ar.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil)
				ep.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(nil)
			},
			wantErr:    nil,
			wantStatus: ApplicationStatusRejected,
		},
		{
			name:     "success: waitlist sets status=waitlisted",
			id:       appID,
			approved: false,
			reason:   ptrString("waitlist"),
			setup: func(ar *mockAppRepo, ep *mockEventPublisher) {
				ar.On("GetByID", mock.Anything, appID, tenantID).Return(&TrainingApplication{
					ID:       appID,
					TenantID: tenantID,
					GCID:     reviewerGCID,
					Status:   ApplicationStatusUnderReview,
				}, nil)
				ar.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil)
				ep.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(nil)
			},
			wantErr:    nil,
			wantStatus: ApplicationStatusWaitlisted,
		},
		{
			name:     "fails: application not in submitted/under_review",
			id:       appID,
			approved: true,
			reason:   nil,
			setup: func(ar *mockAppRepo, _ *mockEventPublisher) {
				ar.On("GetByID", mock.Anything, appID, tenantID).Return(&TrainingApplication{
					ID:       appID,
					TenantID: tenantID,
					Status:   ApplicationStatusApproved,
				}, nil)
			},
			wantErr: ErrApplicationNotReviewable,
		},
		{
			name:     "fails: application not found",
			id:       appID,
			approved: true,
			reason:   nil,
			setup: func(ar *mockAppRepo, _ *mockEventPublisher) {
				ar.On("GetByID", mock.Anything, appID, tenantID).Return(nil, ErrNotFound)
			},
			wantErr: ErrNotFound,
		},
		{
			name:     "fails: reject without reason",
			id:       appID,
			approved: false,
			reason:   nil,
			setup: func(ar *mockAppRepo, ep *mockEventPublisher) {
				ar.On("GetByID", mock.Anything, appID, tenantID).Return(&TrainingApplication{
					ID:       appID,
					TenantID: tenantID,
					Status:   ApplicationStatusSubmitted,
				}, nil)
			},
			wantErr: ErrValidationFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, appRepo, _, _, pub := newTestAppService()
			tt.setup(appRepo, pub)

			result, err := svc.DecideApplication(context.Background(), tt.id, tenantID, tt.approved, tt.reason)

			if tt.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tt.wantErr, ErrApplicationNotReviewable) ||
					errors.Is(tt.wantErr, ErrNotFound) ||
					errors.Is(tt.wantErr, ErrValidationFailed) {
					assert.ErrorIs(t, err, tt.wantErr)
				}
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, result) {
					assert.Equal(t, tt.wantStatus, result.Status)
					assert.NotNil(t, result.ReviewedAt)
					assert.NotNil(t, result.ReviewedByGCID)
				}
				pub.AssertCalled(t, "Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent"))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Part 2: TraineeRequest — SubmitRequest
// ---------------------------------------------------------------------------

func TestSubmitTraineeRequest(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	targetSessionID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name    string
		input   *TraineeRequest
		setup   func(*mockAppRepo, *mockRequestRepo, *mockEventPublisher)
		wantErr error
	}{
		{
			name: "success: submits request with status=submitted, publishes event",
			input: &TraineeRequest{
				TenantID:          tenantID,
				GCID:              gcid,
				TrainingSessionID: sessionID,
				RequestType:       RequestTypeDeferral,
				Reason:            "Medical leave",
			},
			setup: func(_ *mockAppRepo, rr *mockRequestRepo, ep *mockEventPublisher) {
				rr.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.TraineeRequest")).Return(nil)
				ep.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(nil)
			},
			wantErr: nil,
		},
		{
			name: "fails: empty reason",
			input: &TraineeRequest{
				TenantID:          tenantID,
				GCID:              gcid,
				TrainingSessionID: sessionID,
				RequestType:       RequestTypeDeferral,
				Reason:            "",
			},
			setup:   func(_ *mockAppRepo, _ *mockRequestRepo, _ *mockEventPublisher) {},
			wantErr: ErrValidationFailed,
		},
		{
			name: "fails: transfer to same session",
			input: &TraineeRequest{
				TenantID:          tenantID,
				GCID:              gcid,
				TrainingSessionID: sessionID,
				RequestType:       RequestTypeTransfer,
				Reason:            "Want to transfer",
				TargetSessionID:   &sessionID,
			},
			setup:   func(_ *mockAppRepo, _ *mockRequestRepo, _ *mockEventPublisher) {},
			wantErr: ErrTransferSameSession,
		},
		{
			name: "fails: transfer without target_session_id",
			input: &TraineeRequest{
				TenantID:          tenantID,
				GCID:              gcid,
				TrainingSessionID: sessionID,
				RequestType:       RequestTypeTransfer,
				Reason:            "Want to move sessions",
				TargetSessionID:   nil,
			},
			setup:   func(_ *mockAppRepo, _ *mockRequestRepo, _ *mockEventPublisher) {},
			wantErr: ErrValidationFailed,
		},
		{
			name: "fails: repo error",
			input: &TraineeRequest{
				TenantID:          tenantID,
				GCID:              gcid,
				TrainingSessionID: sessionID,
				RequestType:       RequestTypeWithdrawal,
				Reason:            "Personal reasons",
				TargetSessionID:   &targetSessionID,
			},
			setup: func(_ *mockAppRepo, rr *mockRequestRepo, _ *mockEventPublisher) {
				rr.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.TraineeRequest")).Return(errors.New("db write failed"))
			},
			wantErr: errors.New("db write failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, appRepo, reqRepo, _, pub := newTestRequestService()
			tt.setup(appRepo, reqRepo, pub)

			result, err := svc.SubmitRequest(context.Background(), tt.input)

			if tt.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tt.wantErr, ErrValidationFailed) ||
					errors.Is(tt.wantErr, ErrTransferSameSession) {
					assert.ErrorIs(t, err, tt.wantErr)
				}
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, result) {
					assert.Equal(t, RequestStatusSubmitted, result.Status)
				}
				pub.AssertCalled(t, "Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent"))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Part 2: TraineeRequest — DecideRequest
// ---------------------------------------------------------------------------

func TestDecideTraineeRequest(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	requestID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		approved   bool
		notes      *string
		setup      func(*mockAppRepo, *mockRequestRepo, *mockEventPublisher)
		wantErr    error
		wantStatus RequestStatus
	}{
		{
			name:     "success: approve sets status=approved, publishes event",
			id:       requestID,
			approved: true,
			notes:    nil,
			setup: func(_ *mockAppRepo, rr *mockRequestRepo, ep *mockEventPublisher) {
				rr.On("GetByID", mock.Anything, requestID, tenantID).Return(&TraineeRequest{
					ID:       requestID,
					TenantID: tenantID,
					Status:   RequestStatusSubmitted,
				}, nil)
				rr.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TraineeRequest")).Return(nil)
				ep.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(nil)
			},
			wantErr:    nil,
			wantStatus: RequestStatusApproved,
		},
		{
			name:     "success: reject with resolution_notes",
			id:       requestID,
			approved: false,
			notes:    ptrString("Insufficient documentation provided"),
			setup: func(_ *mockAppRepo, rr *mockRequestRepo, ep *mockEventPublisher) {
				rr.On("GetByID", mock.Anything, requestID, tenantID).Return(&TraineeRequest{
					ID:       requestID,
					TenantID: tenantID,
					Status:   RequestStatusUnderReview,
				}, nil)
				rr.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TraineeRequest")).Return(nil)
				ep.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(nil)
			},
			wantErr:    nil,
			wantStatus: RequestStatusRejected,
		},
		{
			name:     "fails: request not in submitted/under_review",
			id:       requestID,
			approved: true,
			notes:    nil,
			setup: func(_ *mockAppRepo, rr *mockRequestRepo, _ *mockEventPublisher) {
				rr.On("GetByID", mock.Anything, requestID, tenantID).Return(&TraineeRequest{
					ID:       requestID,
					TenantID: tenantID,
					Status:   RequestStatusApproved,
				}, nil)
			},
			wantErr: ErrRequestNotDecidable,
		},
		{
			name:     "fails: not found",
			id:       requestID,
			approved: true,
			notes:    nil,
			setup: func(_ *mockAppRepo, rr *mockRequestRepo, _ *mockEventPublisher) {
				rr.On("GetByID", mock.Anything, requestID, tenantID).Return(nil, ErrNotFound)
			},
			wantErr: ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, appRepo, reqRepo, _, pub := newTestRequestService()
			tt.setup(appRepo, reqRepo, pub)

			result, err := svc.DecideRequest(context.Background(), tt.id, tenantID, tt.approved, tt.notes)

			if tt.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tt.wantErr, ErrRequestNotDecidable) ||
					errors.Is(tt.wantErr, ErrNotFound) {
					assert.ErrorIs(t, err, tt.wantErr)
				}
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, result) {
					assert.Equal(t, tt.wantStatus, result.Status)
					assert.NotNil(t, result.ReviewedAt)
					assert.NotNil(t, result.ReviewedByGCID)
					if tt.notes != nil {
						assert.NotNil(t, result.ResolutionNotes)
						assert.Equal(t, *tt.notes, *result.ResolutionNotes)
					}
				}
				pub.AssertCalled(t, "Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent"))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Part 3: GetRequest + ListRequests (delegation coverage)
// ---------------------------------------------------------------------------

func TestGetRequest(t *testing.T) {
	t.Parallel()
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("success", func(t *testing.T) {
		svc, _, reqRepo, _, _ := newTestRequestService()
		reqID := uuid.Must(uuid.NewV7())
		expected := &TraineeRequest{ID: reqID, TenantID: tenantID}
		reqRepo.On("GetByID", mock.Anything, reqID, tenantID).Return(expected, nil)
		result, err := svc.GetRequest(context.Background(), reqID, tenantID)
		assert.NoError(t, err)
		assert.Equal(t, reqID, result.ID)
		reqRepo.AssertExpectations(t)
	})

	t.Run("not found", func(t *testing.T) {
		svc, _, reqRepo, _, _ := newTestRequestService()
		reqID := uuid.Must(uuid.NewV7())
		reqRepo.On("GetByID", mock.Anything, reqID, tenantID).Return(nil, nil)
		_, err := svc.GetRequest(context.Background(), reqID, tenantID)
		assert.ErrorIs(t, err, ErrNotFound)
		reqRepo.AssertExpectations(t)
	})

	t.Run("repo error", func(t *testing.T) {
		svc, _, reqRepo, _, _ := newTestRequestService()
		reqID := uuid.Must(uuid.NewV7())
		reqRepo.On("GetByID", mock.Anything, reqID, tenantID).Return(nil, errors.New("db error"))
		_, err := svc.GetRequest(context.Background(), reqID, tenantID)
		assert.Error(t, err)
		reqRepo.AssertExpectations(t)
	})
}

func TestListRequests(t *testing.T) {
	t.Parallel()
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	t.Run("success", func(t *testing.T) {
		svc, _, reqRepo, _, _ := newTestRequestService()
		expected := []TraineeRequest{{ID: uuid.Must(uuid.NewV7())}}
		reqRepo.On("List", mock.Anything, sessionID, tenantID, (*uuid.UUID)(nil), 20).Return(expected, nil)
		result, err := svc.ListRequests(context.Background(), sessionID, tenantID, nil, 20)
		assert.NoError(t, err)
		assert.Len(t, result, 1)
		reqRepo.AssertExpectations(t)
	})

	t.Run("repo error", func(t *testing.T) {
		svc, _, reqRepo, _, _ := newTestRequestService()
		reqRepo.On("List", mock.Anything, sessionID, tenantID, (*uuid.UUID)(nil), 20).Return(nil, errors.New("db error"))
		_, err := svc.ListRequests(context.Background(), sessionID, tenantID, nil, 20)
		assert.Error(t, err)
		reqRepo.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// Part 4: TimeoutStaleApplications
// ---------------------------------------------------------------------------

func TestTimeoutStaleApplications(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	maxAge := 7 * 24 * time.Hour // 7 days

	t.Run("success: times out stale applications and publishes events", func(t *testing.T) {
		svc, appRepo, _, _, pub := newTestAppService()

		staleTime := time.Now().UTC().Add(-10 * 24 * time.Hour) // 10 days ago
		freshTime := time.Now().UTC().Add(-1 * 24 * time.Hour)  // 1 day ago

		apps := []TrainingApplication{
			{
				ID:          uuid.Must(uuid.NewV7()),
				TenantID:    tenantID,
				GCID:        uuid.Must(uuid.NewV7()),
				Status:      ApplicationStatusSubmitted,
				SubmittedAt: &staleTime,
			},
			{
				ID:          uuid.Must(uuid.NewV7()),
				TenantID:    tenantID,
				GCID:        uuid.Must(uuid.NewV7()),
				Status:      ApplicationStatusSubmitted,
				SubmittedAt: &freshTime,
			},
		}

		appRepo.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusSubmitted).Return(apps, nil)
		appRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil)
		pub.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(nil)

		count, err := svc.TimeoutStaleApplications(context.Background(), tenantID, maxAge)

		assert.NoError(t, err)
		assert.Equal(t, 1, count, "only the stale application should be timed out")
		appRepo.AssertNumberOfCalls(t, "Update", 1)
		pub.AssertNumberOfCalls(t, "Publish", 1)
	})

	t.Run("success: no stale applications returns zero", func(t *testing.T) {
		svc, appRepo, _, _, _ := newTestAppService()

		freshTime := time.Now().UTC().Add(-1 * time.Hour)
		apps := []TrainingApplication{
			{
				ID:          uuid.Must(uuid.NewV7()),
				TenantID:    tenantID,
				GCID:        uuid.Must(uuid.NewV7()),
				Status:      ApplicationStatusSubmitted,
				SubmittedAt: &freshTime,
			},
		}

		appRepo.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusSubmitted).Return(apps, nil)

		count, err := svc.TimeoutStaleApplications(context.Background(), tenantID, maxAge)

		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("success: empty list returns zero", func(t *testing.T) {
		svc, appRepo, _, _, _ := newTestAppService()

		appRepo.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusSubmitted).Return([]TrainingApplication{}, nil)

		count, err := svc.TimeoutStaleApplications(context.Background(), tenantID, maxAge)

		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("success: application with nil submitted_at is skipped", func(t *testing.T) {
		svc, appRepo, _, _, _ := newTestAppService()

		apps := []TrainingApplication{
			{
				ID:          uuid.Must(uuid.NewV7()),
				TenantID:    tenantID,
				GCID:        uuid.Must(uuid.NewV7()),
				Status:      ApplicationStatusSubmitted,
				SubmittedAt: nil,
			},
		}

		appRepo.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusSubmitted).Return(apps, nil)

		count, err := svc.TimeoutStaleApplications(context.Background(), tenantID, maxAge)

		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("fails: ListByStatus repo error", func(t *testing.T) {
		svc, appRepo, _, _, _ := newTestAppService()

		appRepo.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusSubmitted).Return(nil, errors.New("db error"))

		count, err := svc.TimeoutStaleApplications(context.Background(), tenantID, maxAge)

		assert.Error(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("fails: Update repo error stops and returns partial count", func(t *testing.T) {
		svc, appRepo, _, _, _ := newTestAppService()

		staleTime := time.Now().UTC().Add(-10 * 24 * time.Hour)
		apps := []TrainingApplication{
			{
				ID:          uuid.Must(uuid.NewV7()),
				TenantID:    tenantID,
				GCID:        uuid.Must(uuid.NewV7()),
				Status:      ApplicationStatusSubmitted,
				SubmittedAt: &staleTime,
			},
		}

		appRepo.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusSubmitted).Return(apps, nil)
		appRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(errors.New("db write failed"))

		count, err := svc.TimeoutStaleApplications(context.Background(), tenantID, maxAge)

		assert.Error(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("fails: Publish error stops and returns partial count", func(t *testing.T) {
		svc, appRepo, _, _, pub := newTestAppService()

		staleTime := time.Now().UTC().Add(-10 * 24 * time.Hour)
		apps := []TrainingApplication{
			{
				ID:          uuid.Must(uuid.NewV7()),
				TenantID:    tenantID,
				GCID:        uuid.Must(uuid.NewV7()),
				Status:      ApplicationStatusSubmitted,
				SubmittedAt: &staleTime,
			},
		}

		appRepo.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusSubmitted).Return(apps, nil)
		appRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil)
		pub.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(errors.New("pubsub down"))

		count, err := svc.TimeoutStaleApplications(context.Background(), tenantID, maxAge)

		assert.Error(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("success: sets rejection reason and status correctly", func(t *testing.T) {
		svc, appRepo, _, _, pub := newTestAppService()

		staleTime := time.Now().UTC().Add(-10 * 24 * time.Hour)
		apps := []TrainingApplication{
			{
				ID:          uuid.Must(uuid.NewV7()),
				TenantID:    tenantID,
				GCID:        uuid.Must(uuid.NewV7()),
				Status:      ApplicationStatusSubmitted,
				SubmittedAt: &staleTime,
			},
		}

		appRepo.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusSubmitted).Return(apps, nil)
		appRepo.On("Update", mock.Anything, mock.MatchedBy(func(app *TrainingApplication) bool {
			return app.Status == ApplicationStatusRejected &&
				app.RejectionReason != nil &&
				*app.RejectionReason == "Application timed out — no decision within review period" &&
				app.ReviewedAt != nil
		})).Return(nil)
		pub.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(nil)

		count, err := svc.TimeoutStaleApplications(context.Background(), tenantID, maxAge)

		assert.NoError(t, err)
		assert.Equal(t, 1, count)
		appRepo.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// Part 5: TimeoutStaleRequests
// ---------------------------------------------------------------------------

func TestTimeoutStaleRequests(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	maxAge := 14 * 24 * time.Hour // 14 days

	t.Run("success: times out stale requests and publishes events", func(t *testing.T) {
		svc, _, reqRepo, _, pub := newTestRequestService()

		staleTime := time.Now().UTC().Add(-20 * 24 * time.Hour) // 20 days ago
		freshTime := time.Now().UTC().Add(-1 * 24 * time.Hour)  // 1 day ago

		reqs := []TraineeRequest{
			{
				ID:                uuid.Must(uuid.NewV7()),
				TenantID:          tenantID,
				GCID:              uuid.Must(uuid.NewV7()),
				TrainingSessionID: uuid.Must(uuid.NewV7()),
				RequestType:       RequestTypeDeferral,
				Status:            RequestStatusSubmitted,
				Reason:            "Medical leave",
				CreatedAt:         staleTime,
			},
			{
				ID:                uuid.Must(uuid.NewV7()),
				TenantID:          tenantID,
				GCID:              uuid.Must(uuid.NewV7()),
				TrainingSessionID: uuid.Must(uuid.NewV7()),
				RequestType:       RequestTypeWithdrawal,
				Status:            RequestStatusSubmitted,
				Reason:            "Personal reasons",
				CreatedAt:         freshTime,
			},
		}

		reqRepo.On("ListByStatus", mock.Anything, tenantID, RequestStatusSubmitted).Return(reqs, nil)
		reqRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TraineeRequest")).Return(nil)
		pub.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(nil)

		count, err := svc.TimeoutStaleRequests(context.Background(), tenantID, maxAge)

		assert.NoError(t, err)
		assert.Equal(t, 1, count, "only the stale request should be timed out")
		reqRepo.AssertNumberOfCalls(t, "Update", 1)
		pub.AssertNumberOfCalls(t, "Publish", 1)
	})

	t.Run("success: empty list returns zero", func(t *testing.T) {
		svc, _, reqRepo, _, _ := newTestRequestService()

		reqRepo.On("ListByStatus", mock.Anything, tenantID, RequestStatusSubmitted).Return([]TraineeRequest{}, nil)

		count, err := svc.TimeoutStaleRequests(context.Background(), tenantID, maxAge)

		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("success: no stale requests returns zero", func(t *testing.T) {
		svc, _, reqRepo, _, _ := newTestRequestService()

		freshTime := time.Now().UTC().Add(-1 * time.Hour)
		reqs := []TraineeRequest{
			{
				ID:                uuid.Must(uuid.NewV7()),
				TenantID:          tenantID,
				GCID:              uuid.Must(uuid.NewV7()),
				TrainingSessionID: uuid.Must(uuid.NewV7()),
				RequestType:       RequestTypeDeferral,
				Status:            RequestStatusSubmitted,
				Reason:            "Travel conflict",
				CreatedAt:         freshTime,
			},
		}

		reqRepo.On("ListByStatus", mock.Anything, tenantID, RequestStatusSubmitted).Return(reqs, nil)

		count, err := svc.TimeoutStaleRequests(context.Background(), tenantID, maxAge)

		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("fails: ListByStatus repo error", func(t *testing.T) {
		svc, _, reqRepo, _, _ := newTestRequestService()

		reqRepo.On("ListByStatus", mock.Anything, tenantID, RequestStatusSubmitted).Return(nil, errors.New("db error"))

		count, err := svc.TimeoutStaleRequests(context.Background(), tenantID, maxAge)

		assert.Error(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("fails: Update repo error stops and returns partial count", func(t *testing.T) {
		svc, _, reqRepo, _, _ := newTestRequestService()

		staleTime := time.Now().UTC().Add(-20 * 24 * time.Hour)
		reqs := []TraineeRequest{
			{
				ID:                uuid.Must(uuid.NewV7()),
				TenantID:          tenantID,
				GCID:              uuid.Must(uuid.NewV7()),
				TrainingSessionID: uuid.Must(uuid.NewV7()),
				RequestType:       RequestTypeDeferral,
				Status:            RequestStatusSubmitted,
				Reason:            "Medical leave",
				CreatedAt:         staleTime,
			},
		}

		reqRepo.On("ListByStatus", mock.Anything, tenantID, RequestStatusSubmitted).Return(reqs, nil)
		reqRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TraineeRequest")).Return(errors.New("db write failed"))

		count, err := svc.TimeoutStaleRequests(context.Background(), tenantID, maxAge)

		assert.Error(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("success: sets resolution notes and status correctly", func(t *testing.T) {
		svc, _, reqRepo, _, pub := newTestRequestService()

		staleTime := time.Now().UTC().Add(-20 * 24 * time.Hour)
		reqs := []TraineeRequest{
			{
				ID:                uuid.Must(uuid.NewV7()),
				TenantID:          tenantID,
				GCID:              uuid.Must(uuid.NewV7()),
				TrainingSessionID: uuid.Must(uuid.NewV7()),
				RequestType:       RequestTypeDeferral,
				Status:            RequestStatusSubmitted,
				Reason:            "Medical leave",
				CreatedAt:         staleTime,
			},
		}

		reqRepo.On("ListByStatus", mock.Anything, tenantID, RequestStatusSubmitted).Return(reqs, nil)
		reqRepo.On("Update", mock.Anything, mock.MatchedBy(func(req *TraineeRequest) bool {
			return req.Status == RequestStatusRejected &&
				req.ResolutionNotes != nil &&
				*req.ResolutionNotes == "Request timed out — no decision within review period" &&
				req.ReviewedAt != nil
		})).Return(nil)
		pub.On("Publish", mock.Anything, TopicTrainingEvents, mock.AnythingOfType("DomainEvent")).Return(nil)

		count, err := svc.TimeoutStaleRequests(context.Background(), tenantID, maxAge)

		assert.NoError(t, err)
		assert.Equal(t, 1, count)
		reqRepo.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// Error-path top-ups
// ---------------------------------------------------------------------------

// testAppDeps wraps the 5-tuple returned by newTestAppService for convenience.
type testAppDeps struct {
	svc         *ApplicationService
	appRepo     *mockAppRepo
	reqRepo     *mockRequestRepo
	sessionRepo *mockSessionRepo
	pub         *mockEventPublisher
}

// newTestTestAppDeps creates an ApplicationService with fresh mocks exposed via a struct.
func newTestTestAppDeps() testAppDeps {
	svc, appRepo, reqRepo, sessRepo, pub := newTestAppService()
	return testAppDeps{svc: svc, appRepo: appRepo, reqRepo: reqRepo, sessionRepo: sessRepo, pub: pub}
}

func TestSubmitApplication_LookupError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestTestAppDeps()
	d.sessionRepo.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestRepo)

	_, err := d.svc.SubmitApplication(context.Background(), &TrainingApplication{
		TenantID: tenantID, TrainingSessionID: sessionID,
	})
	assert.ErrorIs(t, err, errTestRepo)
	d.appRepo.AssertExpectations(t)
	d.sessionRepo.AssertExpectations(t)
}

func TestSubmitApplication_PublishError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestTestAppDeps()
	d.sessionRepo.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
		ID: sessionID, TenantID: tenantID, EnrollmentOpen: true,
	}, nil)
	d.appRepo.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil)
	d.pub.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)

	_, err := d.svc.SubmitApplication(context.Background(), &TrainingApplication{
		TenantID: tenantID, TrainingSessionID: sessionID,
	})
	assert.ErrorIs(t, err, errTestRepo)
	d.appRepo.AssertExpectations(t)
	d.sessionRepo.AssertExpectations(t)
	d.pub.AssertExpectations(t)
}

func TestGetApplication_RepoError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	appID := uuid.Must(uuid.NewV7())

	d := newTestTestAppDeps()
	d.appRepo.On("GetByID", mock.Anything, appID, tenantID).Return(nil, errTestRepo)

	_, err := d.svc.GetApplication(context.Background(), appID, tenantID)
	assert.ErrorIs(t, err, errTestRepo)
	d.appRepo.AssertExpectations(t)
}

func TestDecideApplication_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	appID := uuid.Must(uuid.NewV7())

	t.Run("repo error on lookup", func(t *testing.T) {
		d := newTestTestAppDeps()
		d.appRepo.On("GetByID", mock.Anything, appID, tenantID).Return(nil, errTestRepo)
		_, err := d.svc.DecideApplication(context.Background(), appID, tenantID, true, nil)
		assert.ErrorIs(t, err, errTestRepo)
		d.appRepo.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestTestAppDeps()
		d.appRepo.On("GetByID", mock.Anything, appID, tenantID).Return(&TrainingApplication{ID: appID, Status: ApplicationStatusSubmitted}, nil)
		d.appRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(errTestRepo)
		_, err := d.svc.DecideApplication(context.Background(), appID, tenantID, true, nil)
		assert.ErrorIs(t, err, errTestRepo)
		d.appRepo.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestTestAppDeps()
		d.appRepo.On("GetByID", mock.Anything, appID, tenantID).Return(&TrainingApplication{ID: appID, Status: ApplicationStatusSubmitted}, nil)
		d.appRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil)
		d.pub.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)
		_, err := d.svc.DecideApplication(context.Background(), appID, tenantID, true, nil)
		assert.ErrorIs(t, err, errTestRepo)
		d.appRepo.AssertExpectations(t)
		d.pub.AssertExpectations(t)
	})
}

func TestSubmitRequest_PublishError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	d := newTestTestAppDeps()
	d.reqRepo.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.TraineeRequest")).Return(nil)
	d.pub.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)

	_, err := d.svc.SubmitRequest(context.Background(), &TraineeRequest{
		TenantID: tenantID, Reason: "because", RequestType: RequestTypeDeferral,
	})
	assert.ErrorIs(t, err, errTestRepo)
	d.reqRepo.AssertExpectations(t)
	d.pub.AssertExpectations(t)
}

func TestDecideRequest_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	reqID := uuid.Must(uuid.NewV7())

	t.Run("repo error on lookup", func(t *testing.T) {
		d := newTestTestAppDeps()
		d.reqRepo.On("GetByID", mock.Anything, reqID, tenantID).Return(nil, errTestRepo)
		_, err := d.svc.DecideRequest(context.Background(), reqID, tenantID, true, nil)
		assert.ErrorIs(t, err, errTestRepo)
		d.reqRepo.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestTestAppDeps()
		d.reqRepo.On("GetByID", mock.Anything, reqID, tenantID).Return(&TraineeRequest{ID: reqID, Status: RequestStatusSubmitted}, nil)
		d.reqRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TraineeRequest")).Return(errTestRepo)
		_, err := d.svc.DecideRequest(context.Background(), reqID, tenantID, true, nil)
		assert.ErrorIs(t, err, errTestRepo)
		d.reqRepo.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestTestAppDeps()
		d.reqRepo.On("GetByID", mock.Anything, reqID, tenantID).Return(&TraineeRequest{ID: reqID, Status: RequestStatusSubmitted}, nil)
		d.reqRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TraineeRequest")).Return(nil)
		d.pub.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)
		_, err := d.svc.DecideRequest(context.Background(), reqID, tenantID, true, nil)
		assert.ErrorIs(t, err, errTestRepo)
		d.reqRepo.AssertExpectations(t)
		d.pub.AssertExpectations(t)
	})
}

func TestTimeoutStaleRequests_PublishError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	old := time.Now().UTC().Add(-48 * time.Hour)

	d := newTestTestAppDeps()
	d.reqRepo.On("ListByStatus", mock.Anything, tenantID, RequestStatusSubmitted).Return([]TraineeRequest{
		{ID: uuid.Must(uuid.NewV7()), CreatedAt: old},
	}, nil)
	d.reqRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TraineeRequest")).Return(nil)
	d.pub.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)

	count, err := d.svc.TimeoutStaleRequests(context.Background(), tenantID, 24*time.Hour)
	assert.Equal(t, 0, count)
	assert.ErrorIs(t, err, errTestRepo)
	d.reqRepo.AssertExpectations(t)
	d.pub.AssertExpectations(t)
} // ---------------------------------------------------------------------------
// Remaining branch top-ups
// ---------------------------------------------------------------------------

func TestGetApplication_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	appID := uuid.Must(uuid.NewV7())

	d := newTestTestAppDeps()
	d.appRepo.On("GetByID", mock.Anything, appID, tenantID).Return(nil, nil)

	_, err := d.svc.GetApplication(context.Background(), appID, tenantID)
	assert.ErrorIs(t, err, ErrNotFound)
	d.appRepo.AssertExpectations(t)
} // ---------------------------------------------------------------------------
// Remaining branch top-ups
// ---------------------------------------------------------------------------

func TestDecideApplication_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	appID := uuid.Must(uuid.NewV7())

	d := newTestTestAppDeps()
	d.appRepo.On("GetByID", mock.Anything, appID, tenantID).Return(nil, nil)

	_, err := d.svc.DecideApplication(context.Background(), appID, tenantID, true, nil)
	assert.ErrorIs(t, err, ErrNotFound)
	d.appRepo.AssertExpectations(t)
}

func TestDecideRequest_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	reqID := uuid.Must(uuid.NewV7())

	d := newTestTestAppDeps()
	d.reqRepo.On("GetByID", mock.Anything, reqID, tenantID).Return(nil, nil)

	_, err := d.svc.DecideRequest(context.Background(), reqID, tenantID, true, nil)
	assert.ErrorIs(t, err, ErrNotFound)
	d.reqRepo.AssertExpectations(t)
}
