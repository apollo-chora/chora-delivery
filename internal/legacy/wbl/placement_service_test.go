package wbl

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// testPlacementDeps holds all mocks wired into a PlacementService.
type testPlacementDeps struct {
	svc       *PlacementService
	placeR    *mockPlacementRepo
	workLogR  *mockWorkLogRepo
	endorseR  *mockEndorsementRepo
	internR   *mockInternshipRepo
	publisher *mockEventPublisher
}

func newTestPlacementService() testPlacementDeps {
	pr := &mockPlacementRepo{}
	wr := &mockWorkLogRepo{}
	er := &mockEndorsementRepo{}
	ir := &mockInternshipRepo{}
	ep := &mockEventPublisher{}
	return testPlacementDeps{
		svc:       NewPlacementService(pr, wr, er, ir, ep),
		placeR:    pr,
		workLogR:  wr,
		endorseR:  er,
		internR:   ir,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// TestCreatePlacement
// ---------------------------------------------------------------------------

func TestCreatePlacement(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())
	supervisorGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *Placement
		setupMocks   func(d testPlacementDeps)
		wantErr      error
		assertResult func(t *testing.T, got *Placement)
	}{
		{
			name: "success: creates placement with active status",
			input: &Placement{
				TenantID:       tenantID,
				InternshipID:   internshipID,
				LearnerGCID:    learnerGCID,
				SupervisorGCID: &supervisorGCID,
			},
			setupMocks: func(d testPlacementDeps) {
				d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(&Internship{
					ID:       internshipID,
					TenantID: tenantID,
					Title:    "Test Internship",
				}, nil)
				d.placeR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.Placement")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *Placement) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, PlacementStatusActive, got.Status)
				assert.Equal(t, float64(0), got.TotalHoursLogged)
			},
		},
		{
			name: "fails: nil internship_id",
			input: &Placement{
				TenantID:    tenantID,
				LearnerGCID: learnerGCID,
			},
			setupMocks: func(d testPlacementDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: nil learner_gcid",
			input: &Placement{
				TenantID:     tenantID,
				InternshipID: internshipID,
			},
			setupMocks: func(d testPlacementDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: internship not found",
			input: &Placement{
				TenantID:     tenantID,
				InternshipID: internshipID,
				LearnerGCID:  learnerGCID,
			},
			setupMocks: func(d testPlacementDeps) {
				d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(nil, nil)
			},
			wantErr: ErrInternshipNotFound,
		},
		{
			name: "fails: repo error propagated",
			input: &Placement{
				TenantID:     tenantID,
				InternshipID: internshipID,
				LearnerGCID:  learnerGCID,
			},
			setupMocks: func(d testPlacementDeps) {
				d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(&Internship{
					ID:       internshipID,
					TenantID: tenantID,
				}, nil)
				d.placeR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.Placement")).
					Return(errors.New("db error"))
			},
			wantErr: errors.New("db error"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestPlacementService()
			tc.setupMocks(d)

			got, err := d.svc.CreatePlacement(context.Background(), tc.input)

			if tc.wantErr != nil {
				require.Error(t, err)
				if errors.Is(tc.wantErr, ErrValidationFailed) ||
					errors.Is(tc.wantErr, ErrInternshipNotFound) {
					assert.ErrorIs(t, err, tc.wantErr)
				}
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.NotNil(t, got)
				if tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.placeR.AssertExpectations(t)
			d.internR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestCompletePlacement
// ---------------------------------------------------------------------------

func TestCompletePlacement(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testPlacementDeps)
		wantErr    error
	}{
		{
			name: "success: active to completed",
			setupMocks: func(d testPlacementDeps) {
				d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
					ID:          placementID,
					TenantID:    tenantID,
					LearnerGCID: learnerGCID,
					Status:      PlacementStatusActive,
				}, nil)
				d.placeR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.Placement")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)
			},
		},
		{
			name: "fails: not active",
			setupMocks: func(d testPlacementDeps) {
				d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
					ID:       placementID,
					TenantID: tenantID,
					Status:   PlacementStatusCompleted,
				}, nil)
			},
			wantErr: ErrPlacementNotActive,
		},
		{
			name: "fails: not found",
			setupMocks: func(d testPlacementDeps) {
				d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(nil, nil)
			},
			wantErr: ErrPlacementNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestPlacementService()
			tc.setupMocks(d)

			got, err := d.svc.CompletePlacement(context.Background(), placementID, tenantID)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.NotNil(t, got)
				assert.Equal(t, PlacementStatusCompleted, got.Status)
			}

			d.placeR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAddWorkLogEntry
// ---------------------------------------------------------------------------

func TestAddWorkLogEntry(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())

	activePlacement := &Placement{
		ID:               placementID,
		TenantID:         tenantID,
		Status:           PlacementStatusActive,
		TotalHoursLogged: 10.0,
	}

	tests := []struct {
		name         string
		input        *WorkLogEntry
		setupMocks   func(d testPlacementDeps)
		wantErr      error
		assertResult func(t *testing.T, got *WorkLogEntry)
	}{
		{
			name: "success: adds entry and updates placement hours",
			input: &WorkLogEntry{
				TenantID:    tenantID,
				PlacementID: placementID,
				Hours:       4.5,
				Description: "Worked on API endpoints",
			},
			setupMocks: func(d testPlacementDeps) {
				d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(activePlacement, nil)
				d.workLogR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.WorkLogEntry")).Return(nil)
				d.placeR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.Placement")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *WorkLogEntry) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, 4.5, got.Hours)
				assert.False(t, got.SupervisorApproved)
			},
		},
		{
			name: "fails: zero hours",
			input: &WorkLogEntry{
				TenantID:    tenantID,
				PlacementID: placementID,
				Hours:       0,
				Description: "No work",
			},
			setupMocks: func(d testPlacementDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: empty description",
			input: &WorkLogEntry{
				TenantID:    tenantID,
				PlacementID: placementID,
				Hours:       2.0,
				Description: "",
			},
			setupMocks: func(d testPlacementDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: placement not active",
			input: &WorkLogEntry{
				TenantID:    tenantID,
				PlacementID: placementID,
				Hours:       2.0,
				Description: "Completed placement",
			},
			setupMocks: func(d testPlacementDeps) {
				d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
					ID:       placementID,
					TenantID: tenantID,
					Status:   PlacementStatusCompleted,
				}, nil)
			},
			wantErr: ErrPlacementNotActive,
		},
		{
			name: "fails: placement not found",
			input: &WorkLogEntry{
				TenantID:    tenantID,
				PlacementID: placementID,
				Hours:       2.0,
				Description: "Some work",
			},
			setupMocks: func(d testPlacementDeps) {
				d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(nil, nil)
			},
			wantErr: ErrPlacementNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestPlacementService()
			tc.setupMocks(d)

			got, err := d.svc.AddWorkLogEntry(context.Background(), tc.input)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.NotNil(t, got)
				if tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.placeR.AssertExpectations(t)
			d.workLogR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestEndorseSkill
// ---------------------------------------------------------------------------

func TestEndorseSkill(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())
	endorserGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *SkillEndorsement
		setupMocks   func(d testPlacementDeps)
		wantErr      error
		assertResult func(t *testing.T, got *SkillEndorsement)
	}{
		{
			name: "success: endorses skill",
			input: &SkillEndorsement{
				TenantID:     tenantID,
				PlacementID:  placementID,
				EndorserGCID: endorserGCID,
				SkillName:    "Go Programming",
				Level:        EndorsementLevelAdvanced,
			},
			setupMocks: func(d testPlacementDeps) {
				d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
					ID:       placementID,
					TenantID: tenantID,
					Status:   PlacementStatusActive,
				}, nil)
				d.endorseR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.SkillEndorsement")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *SkillEndorsement) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, EndorsementLevelAdvanced, got.Level)
				assert.Equal(t, "Go Programming", got.SkillName)
				assert.False(t, got.EndorsedAt.IsZero())
			},
		},
		{
			name: "fails: empty skill_name",
			input: &SkillEndorsement{
				TenantID:     tenantID,
				PlacementID:  placementID,
				EndorserGCID: endorserGCID,
				SkillName:    "",
				Level:        EndorsementLevelBeginner,
			},
			setupMocks: func(d testPlacementDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: invalid level",
			input: &SkillEndorsement{
				TenantID:     tenantID,
				PlacementID:  placementID,
				EndorserGCID: endorserGCID,
				SkillName:    "Go",
				Level:        EndorsementLevel("master"),
			},
			setupMocks: func(d testPlacementDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: placement not active",
			input: &SkillEndorsement{
				TenantID:     tenantID,
				PlacementID:  placementID,
				EndorserGCID: endorserGCID,
				SkillName:    "Go",
				Level:        EndorsementLevelBeginner,
			},
			setupMocks: func(d testPlacementDeps) {
				d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
					ID:       placementID,
					TenantID: tenantID,
					Status:   PlacementStatusWithdrawn,
				}, nil)
			},
			wantErr: ErrPlacementNotActive,
		},
		{
			name: "fails: placement not found for endorsement",
			input: &SkillEndorsement{
				TenantID:     tenantID,
				PlacementID:  placementID,
				EndorserGCID: endorserGCID,
				SkillName:    "Go",
				Level:        EndorsementLevelBeginner,
			},
			setupMocks: func(d testPlacementDeps) {
				d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(nil, nil)
			},
			wantErr: ErrPlacementNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestPlacementService()
			tc.setupMocks(d)

			got, err := d.svc.EndorseSkill(context.Background(), tc.input)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.NotNil(t, got)
				if tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.placeR.AssertExpectations(t)
			d.endorseR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListPlacements
// ---------------------------------------------------------------------------

func TestListPlacements(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	t.Run("success: delegates to repository", func(t *testing.T) {
		t.Parallel()
		d := newTestPlacementService()
		expected := []Placement{
			{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Status: PlacementStatusActive},
		}
		d.placeR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 20).Return(expected, nil)

		got, err := d.svc.ListPlacements(context.Background(), tenantID, nil, 20)

		require.NoError(t, err)
		assert.Len(t, got, 1)
		d.placeR.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// TestListWorkLogEntries
// ---------------------------------------------------------------------------

func TestListWorkLogEntries(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())

	t.Run("success: delegates to repository", func(t *testing.T) {
		t.Parallel()
		d := newTestPlacementService()
		expected := []WorkLogEntry{
			{ID: uuid.Must(uuid.NewV7()), PlacementID: placementID, Hours: 3.0},
		}
		d.workLogR.On("ListByPlacement", mock.Anything, placementID, tenantID, (*uuid.UUID)(nil), 50).
			Return(expected, nil)

		got, err := d.svc.ListWorkLogEntries(context.Background(), placementID, tenantID, nil, 50)

		require.NoError(t, err)
		assert.Len(t, got, 1)
		d.workLogR.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// TestCompletePlacement_RepoGetError
// ---------------------------------------------------------------------------

func TestCompletePlacement_RepoGetError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.placeR.On("GetByID", mock.Anything, placementID, tenantID).
		Return(nil, errors.New("db error"))

	got, err := d.svc.CompletePlacement(context.Background(), placementID, tenantID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "db error")
	assert.Nil(t, got)
	d.placeR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestCompletePlacement_UpdateError
// ---------------------------------------------------------------------------

func TestCompletePlacement_UpdateError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
		ID:          placementID,
		TenantID:    tenantID,
		LearnerGCID: learnerGCID,
		Status:      PlacementStatusActive,
	}, nil)
	d.placeR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.Placement")).
		Return(errors.New("write conflict"))

	got, err := d.svc.CompletePlacement(context.Background(), placementID, tenantID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "write conflict")
	assert.Nil(t, got)
	d.placeR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestCompletePlacement_EventPublishError
// ---------------------------------------------------------------------------

func TestCompletePlacement_EventPublishError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
		ID:          placementID,
		TenantID:    tenantID,
		LearnerGCID: learnerGCID,
		Status:      PlacementStatusActive,
	}, nil)
	d.placeR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.Placement")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).
		Return(errors.New("pubsub down"))

	got, err := d.svc.CompletePlacement(context.Background(), placementID, tenantID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish placement.completed")
	assert.Nil(t, got)
	d.placeR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestCreatePlacement_WithoutSupervisor
// ---------------------------------------------------------------------------

func TestCreatePlacement_WithoutSupervisor(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(&Internship{
		ID:       internshipID,
		TenantID: tenantID,
	}, nil)
	d.placeR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.Placement")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)

	got, err := d.svc.CreatePlacement(context.Background(), &Placement{
		TenantID:       tenantID,
		InternshipID:   internshipID,
		LearnerGCID:    learnerGCID,
		SupervisorGCID: nil, // no supervisor
	})

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, PlacementStatusActive, got.Status)
	assert.Nil(t, got.SupervisorGCID)
	d.placeR.AssertExpectations(t)
	d.internR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestCreatePlacement_EventPublishError
// ---------------------------------------------------------------------------

func TestCreatePlacement_EventPublishError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(&Internship{
		ID:       internshipID,
		TenantID: tenantID,
	}, nil)
	d.placeR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.Placement")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).
		Return(errors.New("pubsub error"))

	got, err := d.svc.CreatePlacement(context.Background(), &Placement{
		TenantID:     tenantID,
		InternshipID: internshipID,
		LearnerGCID:  learnerGCID,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish placement.started")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestCreatePlacement_InternshipLookupError
// ---------------------------------------------------------------------------

func TestCreatePlacement_InternshipLookupError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.internR.On("GetByID", mock.Anything, internshipID, tenantID).
		Return(nil, errors.New("db timeout"))

	got, err := d.svc.CreatePlacement(context.Background(), &Placement{
		TenantID:     tenantID,
		InternshipID: internshipID,
		LearnerGCID:  learnerGCID,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lookup internship")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestAddWorkLogEntry_WorkLogCreateError
// ---------------------------------------------------------------------------

func TestAddWorkLogEntry_WorkLogCreateError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
		ID:               placementID,
		TenantID:         tenantID,
		Status:           PlacementStatusActive,
		TotalHoursLogged: 5.0,
	}, nil)
	d.workLogR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.WorkLogEntry")).
		Return(errors.New("insert failed"))

	got, err := d.svc.AddWorkLogEntry(context.Background(), &WorkLogEntry{
		TenantID:    tenantID,
		PlacementID: placementID,
		Hours:       2.0,
		Description: "Some work",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "insert failed")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestAddWorkLogEntry_PlacementUpdateError
// ---------------------------------------------------------------------------

func TestAddWorkLogEntry_PlacementUpdateError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
		ID:               placementID,
		TenantID:         tenantID,
		Status:           PlacementStatusActive,
		TotalHoursLogged: 5.0,
	}, nil)
	d.workLogR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.WorkLogEntry")).Return(nil)
	d.placeR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.Placement")).
		Return(errors.New("optimistic lock"))

	got, err := d.svc.AddWorkLogEntry(context.Background(), &WorkLogEntry{
		TenantID:    tenantID,
		PlacementID: placementID,
		Hours:       2.0,
		Description: "Some work",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "update placement hours")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestAddWorkLogEntry_EventPublishError
// ---------------------------------------------------------------------------

func TestAddWorkLogEntry_EventPublishError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
		ID:               placementID,
		TenantID:         tenantID,
		Status:           PlacementStatusActive,
		TotalHoursLogged: 5.0,
	}, nil)
	d.workLogR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.WorkLogEntry")).Return(nil)
	d.placeR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.Placement")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).
		Return(errors.New("pubsub error"))

	got, err := d.svc.AddWorkLogEntry(context.Background(), &WorkLogEntry{
		TenantID:    tenantID,
		PlacementID: placementID,
		Hours:       2.0,
		Description: "Some work",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish worklog.added")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestAddWorkLogEntry_NegativeHours
// ---------------------------------------------------------------------------

func TestAddWorkLogEntry_NegativeHours(t *testing.T) {
	t.Parallel()

	d := newTestPlacementService()

	got, err := d.svc.AddWorkLogEntry(context.Background(), &WorkLogEntry{
		TenantID:    uuid.Must(uuid.NewV7()),
		PlacementID: uuid.Must(uuid.NewV7()),
		Hours:       -1.0,
		Description: "Negative hours",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidationFailed)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestAddWorkLogEntry_PlacementLookupError
// ---------------------------------------------------------------------------

func TestAddWorkLogEntry_PlacementLookupError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.placeR.On("GetByID", mock.Anything, placementID, tenantID).
		Return(nil, errors.New("db error"))

	got, err := d.svc.AddWorkLogEntry(context.Background(), &WorkLogEntry{
		TenantID:    tenantID,
		PlacementID: placementID,
		Hours:       2.0,
		Description: "Some work",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lookup placement")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestEndorseSkill_RepoLookupError
// ---------------------------------------------------------------------------

func TestEndorseSkill_RepoLookupError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.placeR.On("GetByID", mock.Anything, placementID, tenantID).
		Return(nil, errors.New("connection reset"))

	got, err := d.svc.EndorseSkill(context.Background(), &SkillEndorsement{
		TenantID:     tenantID,
		PlacementID:  placementID,
		EndorserGCID: uuid.Must(uuid.NewV7()),
		SkillName:    "Go",
		Level:        EndorsementLevelAdvanced,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lookup placement")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestEndorseSkill_CreateError
// ---------------------------------------------------------------------------

func TestEndorseSkill_CreateError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
		ID:       placementID,
		TenantID: tenantID,
		Status:   PlacementStatusActive,
	}, nil)
	d.endorseR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.SkillEndorsement")).
		Return(errors.New("unique constraint"))

	got, err := d.svc.EndorseSkill(context.Background(), &SkillEndorsement{
		TenantID:     tenantID,
		PlacementID:  placementID,
		EndorserGCID: uuid.Must(uuid.NewV7()),
		SkillName:    "Go",
		Level:        EndorsementLevelAdvanced,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unique constraint")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestEndorseSkill_EventPublishError
// ---------------------------------------------------------------------------

func TestEndorseSkill_EventPublishError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	placementID := uuid.Must(uuid.NewV7())

	d := newTestPlacementService()
	d.placeR.On("GetByID", mock.Anything, placementID, tenantID).Return(&Placement{
		ID:       placementID,
		TenantID: tenantID,
		Status:   PlacementStatusActive,
	}, nil)
	d.endorseR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.SkillEndorsement")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).
		Return(errors.New("pubsub down"))

	got, err := d.svc.EndorseSkill(context.Background(), &SkillEndorsement{
		TenantID:     tenantID,
		PlacementID:  placementID,
		EndorserGCID: uuid.Must(uuid.NewV7()),
		SkillName:    "Go",
		Level:        EndorsementLevelAdvanced,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish skill.endorsed")
	assert.Nil(t, got)
}
