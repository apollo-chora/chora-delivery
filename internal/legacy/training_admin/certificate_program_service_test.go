package training_admin

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

type testProgramService struct {
	svc         *CertificateProgramService
	programRepo *mockProgramRepo
	enrollRepo  *mockEnrollmentRepo
	events      *mockEventPublisher
}

func newTestProgramService() *testProgramService {
	pr := new(mockProgramRepo)
	er := new(mockEnrollmentRepo)
	ev := new(mockEventPublisher)
	return &testProgramService{
		svc:         NewCertificateProgramService(pr, er, ev),
		programRepo: pr,
		enrollRepo:  er,
		events:      ev,
	}
}

// ---------------------------------------------------------------------------
// TestCreateProgram
// ---------------------------------------------------------------------------

func TestCreateProgram(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	creatorGCID := uuid.Must(uuid.NewV7())
	pathID1 := uuid.Must(uuid.NewV7())
	pathID2 := uuid.Must(uuid.NewV7())
	pathID3 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *CertificateProgram
		setupProgram func(*mockProgramRepo)
		setupEnroll  func(*mockEnrollmentRepo)
		setupEvents  func(*mockEventPublisher)
		wantErr      error
		assertResult func(t *testing.T, got *CertificateProgram)
	}{
		{
			name: "success: creates program with UUIDv7, status=draft, total_paths=len(path_ids)",
			input: &CertificateProgram{
				TenantID:      tenantID,
				Title:         "Go Developer Certification",
				Description:   "Full Go certification program",
				PathIDs:       []uuid.UUID{pathID1, pathID2, pathID3},
				CreatedByGCID: creatorGCID,
			},
			setupProgram: func(r *mockProgramRepo) {
				r.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.CertificateProgram")).Return(nil)
			},
			setupEnroll: func(_ *mockEnrollmentRepo) {},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *CertificateProgram) {
				t.Helper()
				assert.NotEqual(t, uuid.Nil, got.ID, "ID should be a non-nil UUIDv7")
				assert.Equal(t, ProgramStatusDraft, got.Status, "new programs must start as draft")
				assert.Equal(t, 3, got.TotalPaths, "total_paths should equal len(path_ids)")
				assert.False(t, got.CreatedAt.IsZero(), "created_at should be set")
				assert.False(t, got.UpdatedAt.IsZero(), "updated_at should be set")
			},
		},
		{
			name: "success: with estimated_duration_hours",
			input: func() *CertificateProgram {
				dur := 40.5
				return &CertificateProgram{
					TenantID:               tenantID,
					Title:                  "Advanced Security Path",
					Description:            "Security certification with time estimate",
					PathIDs:                []uuid.UUID{pathID1, pathID2},
					EstimatedDurationHours: &dur,
					CreatedByGCID:          creatorGCID,
				}
			}(),
			setupProgram: func(r *mockProgramRepo) {
				r.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.CertificateProgram")).Return(nil)
			},
			setupEnroll: func(_ *mockEnrollmentRepo) {},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *CertificateProgram) {
				t.Helper()
				require.NotNil(t, got.EstimatedDurationHours, "estimated_duration_hours should be preserved")
				assert.Equal(t, 40.5, *got.EstimatedDurationHours)
				assert.Equal(t, 2, got.TotalPaths)
			},
		},
		{
			name: "fails: empty title returns ErrValidationFailed",
			input: &CertificateProgram{
				TenantID:      tenantID,
				Title:         "",
				Description:   "Program without a title",
				PathIDs:       []uuid.UUID{pathID1},
				CreatedByGCID: creatorGCID,
			},
			setupProgram: func(_ *mockProgramRepo) {},
			setupEnroll:  func(_ *mockEnrollmentRepo) {},
			setupEvents:  func(_ *mockEventPublisher) {},
			wantErr:      ErrValidationFailed,
		},
		{
			name: "fails: empty path_ids returns ErrValidationFailed",
			input: &CertificateProgram{
				TenantID:      tenantID,
				Title:         "Program Without Paths",
				Description:   "No paths at all",
				PathIDs:       []uuid.UUID{},
				CreatedByGCID: creatorGCID,
			},
			setupProgram: func(_ *mockProgramRepo) {},
			setupEnroll:  func(_ *mockEnrollmentRepo) {},
			setupEvents:  func(_ *mockEventPublisher) {},
			wantErr:      ErrValidationFailed,
		},
		{
			name: "fails: repo error propagated",
			input: &CertificateProgram{
				TenantID:      tenantID,
				Title:         "Valid Program",
				Description:   "Should fail on repo",
				PathIDs:       []uuid.UUID{pathID1},
				CreatedByGCID: creatorGCID,
			},
			setupProgram: func(r *mockProgramRepo) {
				r.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.CertificateProgram")).
					Return(fmt.Errorf("db connection lost"))
			},
			setupEnroll: func(_ *mockEnrollmentRepo) {},
			setupEvents: func(_ *mockEventPublisher) {},
			wantErr:     errors.New("db connection lost"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestProgramService()
			tt.setupProgram(h.programRepo)
			tt.setupEnroll(h.enrollRepo)
			tt.setupEvents(h.events)

			got, err := h.svc.CreateProgram(context.Background(), tt.input)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			tt.assertResult(t, got)
			h.programRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetProgram
// ---------------------------------------------------------------------------

func TestGetProgram(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	programID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		id           uuid.UUID
		tenantID     uuid.UUID
		setupProgram func(*mockProgramRepo)
		wantErr      error
	}{
		{
			name:     "success: returns program",
			id:       programID,
			tenantID: tenantID,
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{
					ID:         programID,
					TenantID:   tenantID,
					Title:      "Go Certification",
					Status:     ProgramStatusActive,
					PathIDs:    []uuid.UUID{uuid.Must(uuid.NewV7())},
					TotalPaths: 1,
				}, nil)
			},
		},
		{
			name:     "fails: not found returns ErrNotFound",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID"), tenantID).
					Return(nil, ErrNotFound)
			},
			wantErr: ErrNotFound,
		},
		{
			name:     "fails: repo error propagated",
			id:       programID,
			tenantID: tenantID,
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).
					Return(nil, fmt.Errorf("connection timeout"))
			},
			wantErr: errors.New("connection timeout"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestProgramService()
			tt.setupProgram(h.programRepo)

			got, err := h.svc.GetProgram(context.Background(), tt.id, tt.tenantID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, programID, got.ID)
			h.programRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestUpdateProgram
// ---------------------------------------------------------------------------

func TestUpdateProgram(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	programID := uuid.Must(uuid.NewV7())
	pathID1 := uuid.Must(uuid.NewV7())
	pathID2 := uuid.Must(uuid.NewV7())
	pathID3 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *CertificateProgram
		setupProgram func(*mockProgramRepo)
		setupEvents  func(*mockEventPublisher)
		wantErr      error
		assertResult func(t *testing.T, got *CertificateProgram)
	}{
		{
			name: "success: updates title and description",
			input: &CertificateProgram{
				ID:          programID,
				TenantID:    tenantID,
				Title:       "Updated Title",
				Description: "Updated description",
				PathIDs:     []uuid.UUID{pathID1, pathID2},
			},
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{
					ID:         programID,
					TenantID:   tenantID,
					Title:      "Original Title",
					Status:     ProgramStatusDraft,
					PathIDs:    []uuid.UUID{pathID1, pathID2},
					TotalPaths: 2,
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.CertificateProgram")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *CertificateProgram) {
				t.Helper()
				assert.Equal(t, "Updated Title", got.Title)
				assert.Equal(t, "Updated description", got.Description)
			},
		},
		{
			name: "success: updates path_ids and recalculates total_paths",
			input: &CertificateProgram{
				ID:       programID,
				TenantID: tenantID,
				Title:    "Same Title",
				PathIDs:  []uuid.UUID{pathID1, pathID2, pathID3},
			},
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{
					ID:         programID,
					TenantID:   tenantID,
					Title:      "Same Title",
					Status:     ProgramStatusDraft,
					PathIDs:    []uuid.UUID{pathID1},
					TotalPaths: 1,
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.CertificateProgram")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *CertificateProgram) {
				t.Helper()
				assert.Equal(t, 3, got.TotalPaths, "total_paths should be recalculated from new path_ids")
				assert.Len(t, got.PathIDs, 3)
			},
		},
		{
			name: "fails: archived program returns ErrSessionNotModifiable",
			input: &CertificateProgram{
				ID:       programID,
				TenantID: tenantID,
				Title:    "Try To Update",
				PathIDs:  []uuid.UUID{pathID1},
			},
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{
					ID:         programID,
					TenantID:   tenantID,
					Title:      "Archived Program",
					Status:     ProgramStatusArchived,
					PathIDs:    []uuid.UUID{pathID1},
					TotalPaths: 1,
				}, nil)
			},
			setupEvents: func(_ *mockEventPublisher) {},
			wantErr:     ErrSessionNotModifiable,
		},
		{
			name: "fails: not found",
			input: &CertificateProgram{
				ID:       uuid.Must(uuid.NewV7()),
				TenantID: tenantID,
				Title:    "Nonexistent",
				PathIDs:  []uuid.UUID{pathID1},
			},
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID"), tenantID).
					Return(nil, ErrNotFound)
			},
			setupEvents: func(_ *mockEventPublisher) {},
			wantErr:     ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestProgramService()
			tt.setupProgram(h.programRepo)
			tt.setupEvents(h.events)

			got, err := h.svc.UpdateProgram(context.Background(), tt.input)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			tt.assertResult(t, got)
			h.programRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestEnrollInProgram
// ---------------------------------------------------------------------------

func TestEnrollInProgram(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	programID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())
	pathID1 := uuid.Must(uuid.NewV7())
	pathID2 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *ProgramEnrollment
		setupProgram func(*mockProgramRepo)
		setupEnroll  func(*mockEnrollmentRepo)
		setupEvents  func(*mockEventPublisher)
		wantErr      error
		assertResult func(t *testing.T, got *ProgramEnrollment)
	}{
		{
			name: "success: creates enrollment with status=enrolled, progress_pct=0",
			input: &ProgramEnrollment{
				TenantID:  tenantID,
				GCID:      learnerGCID,
				ProgramID: programID,
			},
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{
					ID:         programID,
					TenantID:   tenantID,
					Title:      "Active Program",
					Status:     ProgramStatusActive,
					PathIDs:    []uuid.UUID{pathID1, pathID2},
					TotalPaths: 2,
				}, nil)
			},
			setupEnroll: func(r *mockEnrollmentRepo) {
				r.On("ListByProgram", mock.Anything, programID, tenantID, (*uuid.UUID)(nil), mock.AnythingOfType("int")).
					Return([]ProgramEnrollment{}, nil)
				r.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.ProgramEnrollment")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *ProgramEnrollment) {
				t.Helper()
				assert.NotEqual(t, uuid.Nil, got.ID, "ID should be a non-nil UUIDv7")
				assert.Equal(t, EnrollmentStatusEnrolled, got.Status, "initial status must be enrolled")
				assert.Equal(t, float64(0), got.ProgressPct, "initial progress must be 0")
				assert.Equal(t, 2, got.TotalPaths, "total_paths should mirror the program")
				assert.False(t, got.EnrolledAt.IsZero(), "enrolled_at should be set")
				assert.Nil(t, got.CompletedAt, "completed_at should be nil for new enrollment")
			},
		},
		{
			name: "fails: program not found returns ErrNotFound",
			input: &ProgramEnrollment{
				TenantID:  tenantID,
				GCID:      learnerGCID,
				ProgramID: uuid.Must(uuid.NewV7()),
			},
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID"), tenantID).
					Return(nil, ErrNotFound)
			},
			setupEnroll: func(_ *mockEnrollmentRepo) {},
			setupEvents: func(_ *mockEventPublisher) {},
			wantErr:     ErrNotFound,
		},
		{
			name: "fails: program not active returns ErrValidationFailed",
			input: &ProgramEnrollment{
				TenantID:  tenantID,
				GCID:      learnerGCID,
				ProgramID: programID,
			},
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{
					ID:         programID,
					TenantID:   tenantID,
					Title:      "Draft Program",
					Status:     ProgramStatusDraft,
					PathIDs:    []uuid.UUID{pathID1},
					TotalPaths: 1,
				}, nil)
			},
			setupEnroll: func(_ *mockEnrollmentRepo) {},
			setupEvents: func(_ *mockEventPublisher) {},
			wantErr:     ErrValidationFailed,
		},
		{
			name: "fails: already enrolled returns ErrAlreadyEnrolled",
			input: &ProgramEnrollment{
				TenantID:  tenantID,
				GCID:      learnerGCID,
				ProgramID: programID,
			},
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{
					ID:         programID,
					TenantID:   tenantID,
					Title:      "Active Program",
					Status:     ProgramStatusActive,
					PathIDs:    []uuid.UUID{pathID1, pathID2},
					TotalPaths: 2,
				}, nil)
			},
			setupEnroll: func(r *mockEnrollmentRepo) {
				// Simulate existing enrollment for the same learner
				r.On("ListByProgram", mock.Anything, programID, tenantID, (*uuid.UUID)(nil), mock.AnythingOfType("int")).
					Return([]ProgramEnrollment{
						{
							ID:        uuid.Must(uuid.NewV7()),
							TenantID:  tenantID,
							GCID:      learnerGCID,
							ProgramID: programID,
							Status:    EnrollmentStatusEnrolled,
						},
					}, nil)
			},
			setupEvents: func(_ *mockEventPublisher) {},
			wantErr:     ErrAlreadyEnrolled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestProgramService()
			tt.setupProgram(h.programRepo)
			tt.setupEnroll(h.enrollRepo)
			tt.setupEvents(h.events)

			got, err := h.svc.EnrollInProgram(context.Background(), tt.input)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			tt.assertResult(t, got)
			h.programRepo.AssertExpectations(t)
			h.enrollRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetEnrollment
// ---------------------------------------------------------------------------

func TestGetEnrollment(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	enrollmentID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		id          uuid.UUID
		tenantID    uuid.UUID
		setupEnroll func(*mockEnrollmentRepo)
		wantErr     error
	}{
		{
			name:     "success: returns enrollment",
			id:       enrollmentID,
			tenantID: tenantID,
			setupEnroll: func(r *mockEnrollmentRepo) {
				r.On("GetByID", mock.Anything, enrollmentID, tenantID).Return(&ProgramEnrollment{
					ID:       enrollmentID,
					TenantID: tenantID,
					GCID:     uuid.Must(uuid.NewV7()),
					Status:   EnrollmentStatusEnrolled,
				}, nil)
			},
		},
		{
			name:     "fails: not found returns ErrNotFound",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupEnroll: func(r *mockEnrollmentRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID"), tenantID).
					Return(nil, ErrNotFound)
			},
			wantErr: ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestProgramService()
			tt.setupEnroll(h.enrollRepo)

			got, err := h.svc.GetEnrollment(context.Background(), tt.id, tt.tenantID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, enrollmentID, got.ID)
			h.enrollRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListEnrollments
// ---------------------------------------------------------------------------

func TestListEnrollments(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	programID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		programID   uuid.UUID
		tenantID    uuid.UUID
		setupEnroll func(*mockEnrollmentRepo)
		wantErr     error
		wantLen     int
	}{
		{
			name:      "success: returns list of enrollments",
			programID: programID,
			tenantID:  tenantID,
			setupEnroll: func(r *mockEnrollmentRepo) {
				r.On("ListByProgram", mock.Anything, programID, tenantID, (*uuid.UUID)(nil), 20).
					Return([]ProgramEnrollment{
						{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, ProgramID: programID, Status: EnrollmentStatusEnrolled},
						{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, ProgramID: programID, Status: EnrollmentStatusCompleted},
					}, nil)
			},
			wantLen: 2,
		},
		{
			name:      "fails: repo error propagated",
			programID: programID,
			tenantID:  tenantID,
			setupEnroll: func(r *mockEnrollmentRepo) {
				r.On("ListByProgram", mock.Anything, programID, tenantID, (*uuid.UUID)(nil), 20).
					Return(nil, fmt.Errorf("query timeout"))
			},
			wantErr: errors.New("query timeout"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestProgramService()
			tt.setupEnroll(h.enrollRepo)

			got, err := h.svc.ListEnrollments(context.Background(), tt.programID, tt.tenantID, nil, 20)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			assert.Len(t, got, tt.wantLen)
			h.enrollRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestMarkPathComplete
// ---------------------------------------------------------------------------

func TestMarkPathComplete(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	enrollmentID := uuid.Must(uuid.NewV7())
	programID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())
	pathID1 := uuid.Must(uuid.NewV7())
	pathID2 := uuid.Must(uuid.NewV7())
	pathID3 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		enrollmentID uuid.UUID
		tenantID     uuid.UUID
		pathID       uuid.UUID
		setupProgram func(*mockProgramRepo)
		setupEnroll  func(*mockEnrollmentRepo)
		setupEvents  func(*mockEventPublisher)
		wantErr      error
		assertResult func(t *testing.T, got *ProgramEnrollment)
	}{
		{
			name:         "success: marks path complete and updates progress_pct",
			enrollmentID: enrollmentID,
			tenantID:     tenantID,
			pathID:       pathID2,
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{
					ID:         programID,
					TenantID:   tenantID,
					Title:      "Three-Path Program",
					Status:     ProgramStatusActive,
					PathIDs:    []uuid.UUID{pathID1, pathID2, pathID3},
					TotalPaths: 3,
				}, nil)
			},
			setupEnroll: func(r *mockEnrollmentRepo) {
				r.On("GetByID", mock.Anything, enrollmentID, tenantID).Return(&ProgramEnrollment{
					ID:               enrollmentID,
					TenantID:         tenantID,
					GCID:             learnerGCID,
					ProgramID:        programID,
					Status:           EnrollmentStatusInProgress,
					CompletedPathIDs: []uuid.UUID{pathID1},
					TotalPaths:       3,
					ProgressPct:      33.33,
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.ProgramEnrollment")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				p.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *ProgramEnrollment) {
				t.Helper()
				assert.Contains(t, got.CompletedPathIDs, pathID2, "pathID2 should be in completed list")
				assert.Len(t, got.CompletedPathIDs, 2, "should have 2 completed paths")
				// 2/3 = 66.67% (approximately)
				assert.InDelta(t, 66.67, got.ProgressPct, 1.0, "progress should be ~66.67%%")
				assert.Nil(t, got.CompletedAt, "should not be completed yet")
				assert.NotEqual(t, EnrollmentStatusCompleted, got.Status, "should not be completed yet")
			},
		},
		{
			name:         "success: marks final path — status=completed, certificate issued, publishes event",
			enrollmentID: enrollmentID,
			tenantID:     tenantID,
			pathID:       pathID2,
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{
					ID:         programID,
					TenantID:   tenantID,
					Title:      "Two-Path Program",
					Status:     ProgramStatusActive,
					PathIDs:    []uuid.UUID{pathID1, pathID2},
					TotalPaths: 2,
				}, nil)
			},
			setupEnroll: func(r *mockEnrollmentRepo) {
				r.On("GetByID", mock.Anything, enrollmentID, tenantID).Return(&ProgramEnrollment{
					ID:               enrollmentID,
					TenantID:         tenantID,
					GCID:             learnerGCID,
					ProgramID:        programID,
					Status:           EnrollmentStatusInProgress,
					CompletedPathIDs: []uuid.UUID{pathID1},
					TotalPaths:       2,
					ProgressPct:      50.0,
				}, nil)
				r.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.ProgramEnrollment")).Return(nil)
			},
			setupEvents: func(p *mockEventPublisher) {
				// Expect certificate.issued event to be published
				p.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *ProgramEnrollment) {
				t.Helper()
				assert.Equal(t, EnrollmentStatusCompleted, got.Status, "status should be completed")
				assert.Equal(t, float64(100), got.ProgressPct, "progress should be 100%%")
				assert.NotNil(t, got.CompletedAt, "completed_at should be set")
				assert.NotNil(t, got.CertificateIssuedAt, "certificate_issued_at should be set")
				assert.NotNil(t, got.CertificateID, "certificate_id should be issued")
				assert.NotEqual(t, uuid.Nil, *got.CertificateID)
			},
		},
		{
			name:         "fails: path not in program returns ErrPathNotInProgram",
			enrollmentID: enrollmentID,
			tenantID:     tenantID,
			pathID:       uuid.Must(uuid.NewV7()), // not in the program
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{
					ID:         programID,
					TenantID:   tenantID,
					Title:      "Limited Program",
					Status:     ProgramStatusActive,
					PathIDs:    []uuid.UUID{pathID1, pathID2},
					TotalPaths: 2,
				}, nil)
			},
			setupEnroll: func(r *mockEnrollmentRepo) {
				r.On("GetByID", mock.Anything, enrollmentID, tenantID).Return(&ProgramEnrollment{
					ID:               enrollmentID,
					TenantID:         tenantID,
					GCID:             learnerGCID,
					ProgramID:        programID,
					Status:           EnrollmentStatusInProgress,
					CompletedPathIDs: []uuid.UUID{},
					TotalPaths:       2,
					ProgressPct:      0,
				}, nil)
			},
			setupEvents: func(_ *mockEventPublisher) {},
			wantErr:     ErrPathNotInProgram,
		},
		{
			name:         "fails: enrollment not found returns ErrNotFound",
			enrollmentID: uuid.Must(uuid.NewV7()),
			tenantID:     tenantID,
			pathID:       pathID1,
			setupProgram: func(_ *mockProgramRepo) {},
			setupEnroll: func(r *mockEnrollmentRepo) {
				r.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID"), tenantID).
					Return(nil, ErrNotFound)
			},
			setupEvents: func(_ *mockEventPublisher) {},
			wantErr:     ErrNotFound,
		},
		{
			name:         "idempotent: path already completed — no error, no change",
			enrollmentID: enrollmentID,
			tenantID:     tenantID,
			pathID:       pathID1, // already completed
			setupProgram: func(r *mockProgramRepo) {
				r.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{
					ID:         programID,
					TenantID:   tenantID,
					Title:      "Two-Path Program",
					Status:     ProgramStatusActive,
					PathIDs:    []uuid.UUID{pathID1, pathID2},
					TotalPaths: 2,
				}, nil)
			},
			setupEnroll: func(r *mockEnrollmentRepo) {
				r.On("GetByID", mock.Anything, enrollmentID, tenantID).Return(&ProgramEnrollment{
					ID:               enrollmentID,
					TenantID:         tenantID,
					GCID:             learnerGCID,
					ProgramID:        programID,
					Status:           EnrollmentStatusInProgress,
					CompletedPathIDs: []uuid.UUID{pathID1},
					TotalPaths:       2,
					ProgressPct:      50.0,
				}, nil)
			},
			setupEvents: func(_ *mockEventPublisher) {},
			assertResult: func(t *testing.T, got *ProgramEnrollment) {
				t.Helper()
				assert.Len(t, got.CompletedPathIDs, 1, "should still have only 1 completed path")
				assert.Equal(t, 50.0, got.ProgressPct, "progress should remain unchanged")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newTestProgramService()
			tt.setupProgram(h.programRepo)
			tt.setupEnroll(h.enrollRepo)
			tt.setupEvents(h.events)

			got, err := h.svc.MarkPathComplete(context.Background(), tt.enrollmentID, tt.tenantID, tt.pathID)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr.Error())
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			tt.assertResult(t, got)
			h.enrollRepo.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListPrograms
// ---------------------------------------------------------------------------

func TestListPrograms(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	d := newTestProgramService()
	d.programRepo.On("List", mock.Anything, tenantID, mock.Anything, 40).Return([]CertificateProgram{
		{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID},
	}, nil)

	got, err := d.svc.ListPrograms(context.Background(), tenantID, nil, 40)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.programRepo.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Error-path top-ups
// ---------------------------------------------------------------------------

func TestGetProgram_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	programID := uuid.Must(uuid.NewV7())

	d := newTestProgramService()
	d.programRepo.On("GetByID", mock.Anything, programID, tenantID).Return(nil, nil)

	_, err := d.svc.GetProgram(context.Background(), programID, tenantID)
	assert.ErrorIs(t, err, ErrNotFound)
	d.programRepo.AssertExpectations(t)
}

func TestUpdateProgram_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	programID := uuid.Must(uuid.NewV7())

	p := &CertificateProgram{ID: programID, TenantID: tenantID, Title: "P2", PathIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}}

	t.Run("program not found", func(t *testing.T) {
		d := newTestProgramService()
		d.programRepo.On("GetByID", mock.Anything, programID, tenantID).Return(nil, nil)
		_, err := d.svc.UpdateProgram(context.Background(), p)
		assert.ErrorIs(t, err, ErrNotFound)
		d.programRepo.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestProgramService()
		d.programRepo.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{ID: programID, Status: ProgramStatusDraft}, nil)
		d.programRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.CertificateProgram")).Return(errTestRepo)
		_, err := d.svc.UpdateProgram(context.Background(), p)
		assert.ErrorIs(t, err, errTestRepo)
		d.programRepo.AssertExpectations(t)
	})
}

func TestEnrollInProgram_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	programID := uuid.Must(uuid.NewV7())

	e := &ProgramEnrollment{ProgramID: programID, TenantID: tenantID, GCID: uuid.Must(uuid.NewV7())}

	t.Run("program not found", func(t *testing.T) {
		d := newTestProgramService()
		d.programRepo.On("GetByID", mock.Anything, programID, tenantID).Return(nil, nil)
		_, err := d.svc.EnrollInProgram(context.Background(), e)
		assert.ErrorIs(t, err, ErrNotFound)
		d.programRepo.AssertExpectations(t)
	})

	t.Run("list existing enrollments error", func(t *testing.T) {
		d := newTestProgramService()
		d.programRepo.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{ID: programID, Status: ProgramStatusActive}, nil)
		d.enrollRepo.On("ListByProgram", mock.Anything, programID, tenantID, mock.Anything, 1000).Return(nil, errTestRepo)
		_, err := d.svc.EnrollInProgram(context.Background(), e)
		assert.ErrorIs(t, err, errTestRepo)
		d.programRepo.AssertExpectations(t)
		d.enrollRepo.AssertExpectations(t)
	})

	t.Run("create repo error", func(t *testing.T) {
		d := newTestProgramService()
		d.programRepo.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{ID: programID, Status: ProgramStatusActive, TotalPaths: 3}, nil)
		d.enrollRepo.On("ListByProgram", mock.Anything, programID, tenantID, mock.Anything, 1000).Return([]ProgramEnrollment{}, nil)
		d.enrollRepo.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.ProgramEnrollment")).Return(errTestRepo)
		_, err := d.svc.EnrollInProgram(context.Background(), e)
		assert.ErrorIs(t, err, errTestRepo)
		d.programRepo.AssertExpectations(t)
		d.enrollRepo.AssertExpectations(t)
	})
}

func TestGetEnrollment_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	enrollmentID := uuid.Must(uuid.NewV7())

	d := newTestProgramService()
	d.enrollRepo.On("GetByID", mock.Anything, enrollmentID, tenantID).Return(nil, nil)

	_, err := d.svc.GetEnrollment(context.Background(), enrollmentID, tenantID)
	assert.ErrorIs(t, err, ErrNotFound)
	d.enrollRepo.AssertExpectations(t)
}

func TestMarkPathComplete_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	enrollmentID := uuid.Must(uuid.NewV7())
	programID := uuid.Must(uuid.NewV7())
	pathID := uuid.Must(uuid.NewV7())

	t.Run("enrollment not found", func(t *testing.T) {
		d := newTestProgramService()
		d.enrollRepo.On("GetByID", mock.Anything, enrollmentID, tenantID).Return(nil, nil)
		_, err := d.svc.MarkPathComplete(context.Background(), enrollmentID, tenantID, pathID)
		assert.ErrorIs(t, err, ErrNotFound)
		d.enrollRepo.AssertExpectations(t)
	})

	t.Run("program lookup error", func(t *testing.T) {
		d := newTestProgramService()
		d.enrollRepo.On("GetByID", mock.Anything, enrollmentID, tenantID).Return(&ProgramEnrollment{ID: enrollmentID, ProgramID: programID}, nil)
		d.programRepo.On("GetByID", mock.Anything, programID, tenantID).Return(nil, errTestRepo)
		_, err := d.svc.MarkPathComplete(context.Background(), enrollmentID, tenantID, pathID)
		assert.ErrorIs(t, err, errTestRepo)
		d.enrollRepo.AssertExpectations(t)
		d.programRepo.AssertExpectations(t)
	})

	t.Run("path not in program", func(t *testing.T) {
		d := newTestProgramService()
		d.enrollRepo.On("GetByID", mock.Anything, enrollmentID, tenantID).Return(&ProgramEnrollment{ID: enrollmentID, ProgramID: programID}, nil)
		d.programRepo.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{ID: programID, PathIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}}, nil)
		_, err := d.svc.MarkPathComplete(context.Background(), enrollmentID, tenantID, pathID)
		assert.ErrorIs(t, err, ErrPathNotInProgram)
		d.enrollRepo.AssertExpectations(t)
		d.programRepo.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestProgramService()
		d.enrollRepo.On("GetByID", mock.Anything, enrollmentID, tenantID).Return(&ProgramEnrollment{ID: enrollmentID, ProgramID: programID, TotalPaths: 2}, nil)
		d.programRepo.On("GetByID", mock.Anything, programID, tenantID).Return(&CertificateProgram{ID: programID, PathIDs: []uuid.UUID{pathID, uuid.Must(uuid.NewV7())}}, nil)
		d.enrollRepo.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.ProgramEnrollment")).Return(errTestRepo)
		_, err := d.svc.MarkPathComplete(context.Background(), enrollmentID, tenantID, pathID)
		assert.ErrorIs(t, err, errTestRepo)
		d.enrollRepo.AssertExpectations(t)
		d.programRepo.AssertExpectations(t)
	})
}
