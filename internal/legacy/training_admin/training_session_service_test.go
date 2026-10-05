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

// testSessionDeps holds all mocks wired into a TrainingSessionService.
type testSessionDeps struct {
	svc       *TrainingSessionService
	sessionR  *mockSessionRepo
	scheduleR *mockScheduleRepo
	attendR   *mockAttendanceRepo
	publisher *mockEventPublisher
}

// newTestSessionService creates a TrainingSessionService with fresh mocks.
func newTestSessionService() testSessionDeps {
	sr := &mockSessionRepo{}
	scr := &mockScheduleRepo{}
	ar := &mockAttendanceRepo{}
	ep := &mockEventPublisher{}
	return testSessionDeps{
		svc:       NewTrainingSessionService(sr, scr, ar, ep),
		sessionR:  sr,
		scheduleR: scr,
		attendR:   ar,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// TestCreateSession
// ---------------------------------------------------------------------------

func TestCreateSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	instructorGCID := uuid.Must(uuid.NewV7())
	createdByGCID := uuid.Must(uuid.NewV7())
	lockedPathID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *TrainingSession
		setupMocks   func(d testSessionDeps)
		wantErr      error
		assertResult func(t *testing.T, got *TrainingSession)
	}{
		{
			name: "success: creates session with UUIDv7 and draft status",
			input: &TrainingSession{
				TenantID:       tenantID,
				Title:          "Go Fundamentals",
				Description:    "Introductory Go training",
				DeliveryMode:   DeliveryModePhysical,
				InstructorGCID: instructorGCID,
				MaxCapacity:    30,
				CreatedByGCID:  createdByGCID,
			},
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *TrainingSession) {
				assert.NotEqual(t, uuid.Nil, got.ID, "should assign UUIDv7")
				assert.Equal(t, SessionStatusDraft, got.Status, "should default to draft")
				assert.Equal(t, "Go Fundamentals", got.Title)
				assert.False(t, got.EnrollmentOpen)
			},
		},
		{
			name: "success: creates session with locked_path_id",
			input: &TrainingSession{
				TenantID:       tenantID,
				Title:          "Certification Prep",
				Description:    "Linked to locked path",
				DeliveryMode:   DeliveryModeVirtual,
				LockedPathID:   &lockedPathID,
				InstructorGCID: instructorGCID,
				MaxCapacity:    20,
				CreatedByGCID:  createdByGCID,
			},
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *TrainingSession) {
				assert.NotNil(t, got.LockedPathID)
				assert.Equal(t, lockedPathID, *got.LockedPathID)
				assert.Equal(t, SessionStatusDraft, got.Status)
			},
		},
		{
			name: "fails: empty title returns ErrValidationFailed",
			input: &TrainingSession{
				TenantID:       tenantID,
				Title:          "",
				DeliveryMode:   DeliveryModePhysical,
				InstructorGCID: instructorGCID,
				MaxCapacity:    30,
				CreatedByGCID:  createdByGCID,
			},
			setupMocks: func(d testSessionDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: invalid delivery mode returns ErrValidationFailed",
			input: &TrainingSession{
				TenantID:       tenantID,
				Title:          "Valid Title",
				DeliveryMode:   DeliveryMode("teleportation"),
				InstructorGCID: instructorGCID,
				MaxCapacity:    30,
				CreatedByGCID:  createdByGCID,
			},
			setupMocks: func(d testSessionDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: zero max capacity returns ErrValidationFailed",
			input: &TrainingSession{
				TenantID:       tenantID,
				Title:          "Valid Title",
				DeliveryMode:   DeliveryModeHybrid,
				InstructorGCID: instructorGCID,
				MaxCapacity:    0,
				CreatedByGCID:  createdByGCID,
			},
			setupMocks: func(d testSessionDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: repo error propagated",
			input: &TrainingSession{
				TenantID:       tenantID,
				Title:          "Valid Title",
				DeliveryMode:   DeliveryModePhysical,
				InstructorGCID: instructorGCID,
				MaxCapacity:    30,
				CreatedByGCID:  createdByGCID,
			},
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).
					Return(errors.New("db connection lost"))
			},
			wantErr: errors.New("db connection lost"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSessionService()
			tc.setupMocks(d)

			got, err := d.svc.CreateSession(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrValidationFailed) {
					assert.ErrorIs(t, err, ErrValidationFailed)
				}
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.sessionR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetSession
// ---------------------------------------------------------------------------

func TestGetSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	existing := &TrainingSession{
		ID:           sessionID,
		TenantID:     tenantID,
		Title:        "Existing Session",
		Status:       SessionStatusDraft,
		DeliveryMode: DeliveryModePhysical,
	}

	tests := []struct {
		name       string
		id         uuid.UUID
		tenantID   uuid.UUID
		setupMocks func(d testSessionDeps)
		wantErr    error
	}{
		{
			name:     "success: returns session",
			id:       sessionID,
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(existing, nil)
			},
		},
		{
			name:     "fails: session not found",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, ErrSessionNotFound)
			},
			wantErr: ErrSessionNotFound,
		},
		{
			name:     "fails: repo error propagated",
			id:       sessionID,
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errors.New("timeout"))
			},
			wantErr: errors.New("timeout"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSessionService()
			tc.setupMocks(d)

			got, err := d.svc.GetSession(context.Background(), tc.id, tc.tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrSessionNotFound) {
					assert.ErrorIs(t, err, ErrSessionNotFound)
				}
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) {
					assert.Equal(t, sessionID, got.ID)
				}
			}

			d.sessionR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestUpdateSession
// ---------------------------------------------------------------------------

func TestUpdateSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		input      *TrainingSession
		setupMocks func(d testSessionDeps)
		wantErr    error
	}{
		{
			name: "success: updates mutable fields when draft",
			input: &TrainingSession{
				ID:           sessionID,
				TenantID:     tenantID,
				Title:        "Updated Title",
				DeliveryMode: DeliveryModeVirtual,
				Status:       SessionStatusDraft,
				MaxCapacity:  50,
			},
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:           sessionID,
					TenantID:     tenantID,
					Title:        "Original Title",
					Status:       SessionStatusDraft,
					DeliveryMode: DeliveryModePhysical,
					MaxCapacity:  30,
				}, nil)
				d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
		},
		{
			name: "success: updates when scheduled",
			input: &TrainingSession{
				ID:           sessionID,
				TenantID:     tenantID,
				Title:        "Updated Scheduled",
				DeliveryMode: DeliveryModePhysical,
				Status:       SessionStatusScheduled,
				MaxCapacity:  40,
			},
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:           sessionID,
					TenantID:     tenantID,
					Title:        "Original Scheduled",
					Status:       SessionStatusScheduled,
					DeliveryMode: DeliveryModePhysical,
					MaxCapacity:  30,
				}, nil)
				d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
		},
		{
			name: "fails: cannot update completed session",
			input: &TrainingSession{
				ID:       sessionID,
				TenantID: tenantID,
				Title:    "Try Update",
			},
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:       sessionID,
					TenantID: tenantID,
					Status:   SessionStatusCompleted,
				}, nil)
			},
			wantErr: ErrSessionNotModifiable,
		},
		{
			name: "fails: cannot update cancelled session",
			input: &TrainingSession{
				ID:       sessionID,
				TenantID: tenantID,
				Title:    "Try Update Cancelled",
			},
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:       sessionID,
					TenantID: tenantID,
					Status:   SessionStatusCancelled,
				}, nil)
			},
			wantErr: ErrSessionNotModifiable,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSessionService()
			tc.setupMocks(d)

			got, err := d.svc.UpdateSession(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}

			d.sessionR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestDeleteSession
// ---------------------------------------------------------------------------

func TestDeleteSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		tenantID   uuid.UUID
		setupMocks func(d testSessionDeps)
		wantErr    error
	}{
		{
			name:     "success: soft-deletes draft session",
			id:       sessionID,
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:       sessionID,
					TenantID: tenantID,
					Status:   SessionStatusDraft,
				}, nil)
				d.sessionR.On("Delete", mock.Anything, sessionID, tenantID).Return(nil)
			},
		},
		{
			name:     "fails: cannot delete in_progress session",
			id:       sessionID,
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:       sessionID,
					TenantID: tenantID,
					Status:   SessionStatusInProgress,
				}, nil)
			},
			wantErr: ErrSessionNotDeletable,
		},
		{
			name:     "fails: session not found",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, ErrSessionNotFound)
			},
			wantErr: ErrSessionNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSessionService()
			tc.setupMocks(d)

			err := d.svc.DeleteSession(context.Background(), tc.id, tc.tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}

			d.sessionR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestOpenEnrollment
// ---------------------------------------------------------------------------

func TestOpenEnrollment(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		id           uuid.UUID
		tenantID     uuid.UUID
		setupMocks   func(d testSessionDeps)
		wantErr      error
		assertResult func(t *testing.T, got *TrainingSession)
	}{
		{
			name:     "success: draft to scheduled with enrollment_open=true",
			id:       sessionID,
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:             sessionID,
					TenantID:       tenantID,
					Title:          "Draft Session",
					Status:         SessionStatusDraft,
					EnrollmentOpen: false,
				}, nil)
				d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *TrainingSession) {
				assert.Equal(t, SessionStatusScheduled, got.Status)
				assert.True(t, got.EnrollmentOpen)
			},
		},
		{
			name:     "fails: session not in draft status",
			id:       sessionID,
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:       sessionID,
					TenantID: tenantID,
					Status:   SessionStatusInProgress,
				}, nil)
			},
			wantErr: ErrInvalidStateTransition,
		},
		{
			name:     "fails: session not found",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, ErrSessionNotFound)
			},
			wantErr: ErrSessionNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSessionService()
			tc.setupMocks(d)

			got, err := d.svc.OpenEnrollment(context.Background(), tc.id, tc.tenantID)

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

			d.sessionR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestStartSession
// ---------------------------------------------------------------------------

func TestStartSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		id           uuid.UUID
		tenantID     uuid.UUID
		setupMocks   func(d testSessionDeps)
		wantErr      error
		assertResult func(t *testing.T, got *TrainingSession)
	}{
		{
			name:     "success: scheduled to in_progress",
			id:       sessionID,
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:       sessionID,
					TenantID: tenantID,
					Title:    "Scheduled Session",
					Status:   SessionStatusScheduled,
				}, nil)
				d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *TrainingSession) {
				assert.Equal(t, SessionStatusInProgress, got.Status)
			},
		},
		{
			name:     "fails: not in scheduled status",
			id:       sessionID,
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:       sessionID,
					TenantID: tenantID,
					Status:   SessionStatusDraft,
				}, nil)
			},
			wantErr: ErrInvalidStateTransition,
		},
		{
			name:     "fails: session not found",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, ErrSessionNotFound)
			},
			wantErr: ErrSessionNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSessionService()
			tc.setupMocks(d)

			got, err := d.svc.StartSession(context.Background(), tc.id, tc.tenantID)

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

			d.sessionR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestCompleteSession
// ---------------------------------------------------------------------------

func TestCompleteSession(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		id           uuid.UUID
		tenantID     uuid.UUID
		setupMocks   func(d testSessionDeps)
		wantErr      error
		assertResult func(t *testing.T, got *TrainingSession)
	}{
		{
			name:     "success: in_progress to completed",
			id:       sessionID,
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:       sessionID,
					TenantID: tenantID,
					Title:    "In Progress Session",
					Status:   SessionStatusInProgress,
				}, nil)
				d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *TrainingSession) {
				assert.Equal(t, SessionStatusCompleted, got.Status)
			},
		},
		{
			name:     "fails: not in in_progress status",
			id:       sessionID,
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
					ID:       sessionID,
					TenantID: tenantID,
					Status:   SessionStatusScheduled,
				}, nil)
			},
			wantErr: ErrInvalidStateTransition,
		},
		{
			name:     "fails: session not found",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, ErrSessionNotFound)
			},
			wantErr: ErrSessionNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSessionService()
			tc.setupMocks(d)

			got, err := d.svc.CompleteSession(context.Background(), tc.id, tc.tenantID)

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

			d.sessionR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestRecordAttendance
// ---------------------------------------------------------------------------

func TestRecordAttendance(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())
	markerGCID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	tests := []struct {
		name       string
		records    []Attendance
		setupMocks func(d testSessionDeps)
		wantErr    error
	}{
		{
			name: "success: records batch of attendance",
			records: []Attendance{
				{
					TenantID:          tenantID,
					TrainingSessionID: sessionID,
					LearnerGCID:       learnerGCID,
					Status:            AttendanceStatusPresent,
					CheckInMethod:     CheckInMethodQRScan,
					SessionDate:       now,
					MarkedByGCID:      markerGCID,
				},
				{
					TenantID:          tenantID,
					TrainingSessionID: sessionID,
					LearnerGCID:       uuid.Must(uuid.NewV7()),
					Status:            AttendanceStatusLate,
					CheckInMethod:     CheckInMethodManual,
					SessionDate:       now,
					MarkedByGCID:      markerGCID,
				},
			},
			setupMocks: func(d testSessionDeps) {
				d.attendR.On("Create", mock.Anything, mock.AnythingOfType("[]training_admin.Attendance")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
		},
		{
			name: "fails: invalid attendance status",
			records: []Attendance{
				{
					TenantID:          tenantID,
					TrainingSessionID: sessionID,
					LearnerGCID:       learnerGCID,
					Status:            AttendanceStatus("teleported"),
					CheckInMethod:     CheckInMethodManual,
					SessionDate:       now,
					MarkedByGCID:      markerGCID,
				},
			},
			setupMocks: func(d testSessionDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: repo error propagated",
			records: []Attendance{
				{
					TenantID:          tenantID,
					TrainingSessionID: sessionID,
					LearnerGCID:       learnerGCID,
					Status:            AttendanceStatusPresent,
					CheckInMethod:     CheckInMethodManual,
					SessionDate:       now,
					MarkedByGCID:      markerGCID,
				},
			},
			setupMocks: func(d testSessionDeps) {
				d.attendR.On("Create", mock.Anything, mock.AnythingOfType("[]training_admin.Attendance")).
					Return(errors.New("write conflict"))
			},
			wantErr: errors.New("write conflict"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSessionService()
			tc.setupMocks(d)

			err := d.svc.RecordAttendance(context.Background(), tc.records)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrValidationFailed) {
					assert.ErrorIs(t, err, ErrValidationFailed)
				}
			} else {
				assert.NoError(t, err)
			}

			d.attendR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListAttendance
// ---------------------------------------------------------------------------

func TestListAttendance(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	tests := []struct {
		name       string
		sessionID  uuid.UUID
		tenantID   uuid.UUID
		cursor     *uuid.UUID
		limit      int
		setupMocks func(d testSessionDeps)
		wantErr    error
		wantCount  int
	}{
		{
			name:      "success: returns paginated list",
			sessionID: sessionID,
			tenantID:  tenantID,
			cursor:    nil,
			limit:     20,
			setupMocks: func(d testSessionDeps) {
				d.attendR.On("ListBySession", mock.Anything, sessionID, tenantID, (*uuid.UUID)(nil), 20).
					Return([]Attendance{
						{
							ID:                uuid.Must(uuid.NewV7()),
							TenantID:          tenantID,
							TrainingSessionID: sessionID,
							LearnerGCID:       uuid.Must(uuid.NewV7()),
							Status:            AttendanceStatusPresent,
							SessionDate:       now,
						},
					}, nil)
			},
			wantCount: 1,
		},
		{
			name:      "fails: repo error",
			sessionID: sessionID,
			tenantID:  tenantID,
			cursor:    nil,
			limit:     20,
			setupMocks: func(d testSessionDeps) {
				d.attendR.On("ListBySession", mock.Anything, sessionID, tenantID, (*uuid.UUID)(nil), 20).
					Return(nil, errors.New("connection refused"))
			},
			wantErr: errors.New("connection refused"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSessionService()
			tc.setupMocks(d)

			got, err := d.svc.ListAttendance(context.Background(), tc.sessionID, tc.tenantID, tc.cursor, tc.limit)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.Len(t, got, tc.wantCount)
			}

			d.attendR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAddSchedule
// ---------------------------------------------------------------------------

func TestAddSchedule(t *testing.T) {
	t.Parallel()

	sessionID := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	tests := []struct {
		name         string
		input        *Schedule
		setupMocks   func(d testSessionDeps)
		wantErr      error
		assertResult func(t *testing.T, got *Schedule)
	}{
		{
			name: "success: adds schedule entry",
			input: &Schedule{
				TrainingSessionID: sessionID,
				DayOfWeek:         DayOfWeekMon,
				StartTime:         "09:00",
				EndTime:           "17:00",
				Recurrence:        RecurrenceTypeWeekly,
				EffectiveFrom:     now,
			},
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, sessionID, mock.Anything).Return(&TrainingSession{
					ID:       sessionID,
					TenantID: tenantID,
					Status:   SessionStatusDraft,
				}, nil)
				d.scheduleR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.Schedule")).Return(nil)
			},
			assertResult: func(t *testing.T, got *Schedule) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, sessionID, got.TrainingSessionID)
			},
		},
		{
			name: "fails: end_time before start_time",
			input: &Schedule{
				TrainingSessionID: sessionID,
				DayOfWeek:         DayOfWeekTue,
				StartTime:         "17:00",
				EndTime:           "09:00",
				Recurrence:        RecurrenceTypeOneOff,
				EffectiveFrom:     now,
			},
			setupMocks: func(d testSessionDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: session not found",
			input: &Schedule{
				TrainingSessionID: uuid.Must(uuid.NewV7()),
				DayOfWeek:         DayOfWeekWed,
				StartTime:         "10:00",
				EndTime:           "12:00",
				Recurrence:        RecurrenceTypeOneOff,
				EffectiveFrom:     now,
			},
			setupMocks: func(d testSessionDeps) {
				d.sessionR.On("GetByID", mock.Anything, mock.Anything, mock.Anything).Return(nil, ErrSessionNotFound)
			},
			wantErr: ErrSessionNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSessionService()
			tc.setupMocks(d)

			got, err := d.svc.AddSchedule(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrValidationFailed) {
					assert.ErrorIs(t, err, ErrValidationFailed)
				}
				if errors.Is(tc.wantErr, ErrSessionNotFound) {
					assert.ErrorIs(t, err, ErrSessionNotFound)
				}
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.sessionR.AssertExpectations(t)
			d.scheduleR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListSessions
// ---------------------------------------------------------------------------

func TestListSessions(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.sessionR.On("List", mock.Anything, tenantID, mock.Anything, 25).Return([]TrainingSession{
		{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID},
	}, nil)

	got, err := d.svc.ListSessions(context.Background(), tenantID, nil, 25)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.sessionR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestGetAttendanceSummary
// ---------------------------------------------------------------------------

func TestGetAttendanceSummary(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.attendR.On("GetSummary", mock.Anything, sessionID, tenantID).Return(&AttendanceSummary{
		TotalSessions: 10, PresentCount: 8, AttendanceRate: 80,
	}, nil)

	got, err := d.svc.GetAttendanceSummary(context.Background(), sessionID, tenantID)
	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, 10, got.TotalSessions)
	assert.Equal(t, 8, got.PresentCount)
	d.attendR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestListSchedules
// ---------------------------------------------------------------------------

func TestListSchedules(t *testing.T) {
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.scheduleR.On("ListBySession", mock.Anything, sessionID).Return([]Schedule{
		{ID: uuid.Must(uuid.NewV7()), TrainingSessionID: sessionID},
	}, nil)

	got, err := d.svc.ListSchedules(context.Background(), sessionID)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.scheduleR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestDeleteSchedule
// ---------------------------------------------------------------------------

func TestDeleteSchedule(t *testing.T) {
	scheduleID := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.scheduleR.On("Delete", mock.Anything, scheduleID).Return(nil)

	err := d.svc.DeleteSchedule(context.Background(), scheduleID)
	assert.NoError(t, err)
	d.scheduleR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Error-path top-ups
// ---------------------------------------------------------------------------

func TestCreateSession_PublishError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.sessionR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)

	_, err := d.svc.CreateSession(context.Background(), &TrainingSession{
		TenantID: tenantID, Title: "T", DeliveryMode: DeliveryModePhysical, MaxCapacity: 10, CreatedByGCID: gcid,
	})
	assert.ErrorIs(t, err, errTestRepo)
	d.sessionR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestGetSession_RepoError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestRepo)

	_, err := d.svc.GetSession(context.Background(), sessionID, tenantID)
	assert.ErrorIs(t, err, errTestRepo)
	d.sessionR.AssertExpectations(t)
}

func TestUpdateSession_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	valid := &TrainingSession{
		ID: sessionID, TenantID: tenantID, Title: "T2", DeliveryMode: DeliveryModeVirtual, MaxCapacity: 20,
	}

	t.Run("repo error on lookup", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestRepo)
		_, err := d.svc.UpdateSession(context.Background(), valid)
		assert.ErrorIs(t, err, errTestRepo)
		d.sessionR.AssertExpectations(t)
	})

	t.Run("session not found", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)
		_, err := d.svc.UpdateSession(context.Background(), valid)
		assert.ErrorIs(t, err, ErrSessionNotFound)
		d.sessionR.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{ID: sessionID, TenantID: tenantID, Status: SessionStatusDraft}, nil)
		d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(errTestRepo)
		_, err := d.svc.UpdateSession(context.Background(), valid)
		assert.ErrorIs(t, err, errTestRepo)
		d.sessionR.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{ID: sessionID, TenantID: tenantID, Status: SessionStatusDraft}, nil)
		d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)
		_, err := d.svc.UpdateSession(context.Background(), valid)
		assert.ErrorIs(t, err, errTestRepo)
		d.sessionR.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}

func TestDeleteSession_NotDeletable(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{
		ID: sessionID, TenantID: tenantID, Status: SessionStatusInProgress,
	}, nil)

	err := d.svc.DeleteSession(context.Background(), sessionID, tenantID)
	assert.ErrorIs(t, err, ErrSessionNotDeletable)
	d.sessionR.AssertExpectations(t)
}

func TestOpenEnrollment_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	t.Run("invalid state transition", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{ID: sessionID, Status: SessionStatusCompleted}, nil)
		_, err := d.svc.OpenEnrollment(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, ErrInvalidStateTransition)
		d.sessionR.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{ID: sessionID, Status: SessionStatusDraft}, nil)
		d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(errTestRepo)
		_, err := d.svc.OpenEnrollment(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestRepo)
		d.sessionR.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{ID: sessionID, TenantID: tenantID, Status: SessionStatusDraft}, nil)
		d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)
		_, err := d.svc.OpenEnrollment(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestRepo)
		d.sessionR.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}

func TestStartSession_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	t.Run("invalid state transition", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{ID: sessionID, Status: SessionStatusDraft}, nil)
		_, err := d.svc.StartSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, ErrInvalidStateTransition)
		d.sessionR.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{ID: sessionID, Status: SessionStatusScheduled}, nil)
		d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(errTestRepo)
		_, err := d.svc.StartSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestRepo)
		d.sessionR.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{ID: sessionID, TenantID: tenantID, Status: SessionStatusScheduled}, nil)
		d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)
		_, err := d.svc.StartSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestRepo)
		d.sessionR.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}

func TestCompleteSession_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	t.Run("invalid state transition", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{ID: sessionID, Status: SessionStatusScheduled}, nil)
		_, err := d.svc.CompleteSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, ErrInvalidStateTransition)
		d.sessionR.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{ID: sessionID, Status: SessionStatusInProgress}, nil)
		d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(errTestRepo)
		_, err := d.svc.CompleteSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestRepo)
		d.sessionR.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(&TrainingSession{ID: sessionID, TenantID: tenantID, Status: SessionStatusInProgress}, nil)
		d.sessionR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingSession")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)
		_, err := d.svc.CompleteSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestRepo)
		d.sessionR.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}

func TestRecordAttendance_PublishError(t *testing.T) {
	d := newTestSessionService()
	records := []Attendance{
		{TenantID: uuid.Must(uuid.NewV7()), TrainingSessionID: uuid.Must(uuid.NewV7()), Status: AttendanceStatusPresent},
	}
	d.attendR.On("Create", mock.Anything, mock.Anything).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)

	err := d.svc.RecordAttendance(context.Background(), records)
	assert.ErrorIs(t, err, errTestRepo)
	d.attendR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestAddSchedule_ErrorPaths(t *testing.T) {
	sessionID := uuid.Must(uuid.NewV7())
	valid := &Schedule{
		TrainingSessionID: sessionID,
		StartTime:         "09:00",
		EndTime:           "10:00",
	}

	t.Run("session not found", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, uuid.Nil).Return(nil, nil)
		_, err := d.svc.AddSchedule(context.Background(), valid)
		assert.ErrorIs(t, err, ErrSessionNotFound)
		d.sessionR.AssertExpectations(t)
	})

	t.Run("schedule create error", func(t *testing.T) {
		d := newTestSessionService()
		d.sessionR.On("GetByID", mock.Anything, sessionID, uuid.Nil).Return(&TrainingSession{ID: sessionID}, nil)
		d.scheduleR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.Schedule")).Return(errTestRepo)
		_, err := d.svc.AddSchedule(context.Background(), valid)
		assert.ErrorIs(t, err, errTestRepo)
		d.sessionR.AssertExpectations(t)
		d.scheduleR.AssertExpectations(t)
	})
} // ---------------------------------------------------------------------------
// Remaining branch top-ups
// ---------------------------------------------------------------------------

func TestGetSession_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)

	_, err := d.svc.GetSession(context.Background(), sessionID, tenantID)
	assert.ErrorIs(t, err, ErrSessionNotFound)
	d.sessionR.AssertExpectations(t)
}

func TestDeleteSession_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)

	err := d.svc.DeleteSession(context.Background(), sessionID, tenantID)
	assert.ErrorIs(t, err, ErrSessionNotFound)
	d.sessionR.AssertExpectations(t)
}

func TestOpenEnrollment_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)

	_, err := d.svc.OpenEnrollment(context.Background(), sessionID, tenantID)
	assert.ErrorIs(t, err, ErrSessionNotFound)
	d.sessionR.AssertExpectations(t)
}

func TestStartSession_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)

	_, err := d.svc.StartSession(context.Background(), sessionID, tenantID)
	assert.ErrorIs(t, err, ErrSessionNotFound)
	d.sessionR.AssertExpectations(t)
}

func TestCompleteSession_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	d := newTestSessionService()
	d.sessionR.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)

	_, err := d.svc.CompleteSession(context.Background(), sessionID, tenantID)
	assert.ErrorIs(t, err, ErrSessionNotFound)
	d.sessionR.AssertExpectations(t)
}
