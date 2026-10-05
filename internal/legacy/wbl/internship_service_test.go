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

// testInternshipDeps holds all mocks wired into an InternshipService.
type testInternshipDeps struct {
	svc       *InternshipService
	internR   *mockInternshipRepo
	appR      *mockApplicationRepo
	partnerR  *mockPartnerRepo
	publisher *mockEventPublisher
}

// newTestInternshipService creates an InternshipService with fresh mocks.
func newTestInternshipService() testInternshipDeps {
	ir := &mockInternshipRepo{}
	ar := &mockApplicationRepo{}
	pr := &mockPartnerRepo{}
	ep := &mockEventPublisher{}
	return testInternshipDeps{
		svc:       NewInternshipService(ir, ar, pr, ep),
		internR:   ir,
		appR:      ar,
		partnerR:  pr,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// TestCreateInternship
// ---------------------------------------------------------------------------

func TestCreateInternship(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	partnerID := uuid.Must(uuid.NewV7())
	creatorGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *Internship
		setupMocks   func(d testInternshipDeps)
		wantErr      error
		assertResult func(t *testing.T, got *Internship)
	}{
		{
			name: "success: creates internship with UUIDv7 and draft status",
			input: &Internship{
				TenantID:      tenantID,
				Title:         "Go Backend Intern",
				Description:   "Build microservices",
				PartnerID:     partnerID,
				MaxPositions:  3,
				CreatedByGCID: creatorGCID,
			},
			setupMocks: func(d testInternshipDeps) {
				d.internR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.Internship")).Return(nil)
			},
			assertResult: func(t *testing.T, got *Internship) {
				assert.NotEqual(t, uuid.Nil, got.ID, "should assign UUIDv7")
				assert.Equal(t, InternshipStatusDraft, got.Status, "should default to draft")
				assert.Equal(t, 0, got.FilledPositions)
				assert.Equal(t, "Go Backend Intern", got.Title)
			},
		},
		{
			name: "fails: empty title returns ErrValidationFailed",
			input: &Internship{
				TenantID:      tenantID,
				Title:         "",
				PartnerID:     partnerID,
				MaxPositions:  3,
				CreatedByGCID: creatorGCID,
			},
			setupMocks: func(d testInternshipDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: nil partner_id returns ErrValidationFailed",
			input: &Internship{
				TenantID:      tenantID,
				Title:         "Valid Title",
				PartnerID:     uuid.Nil,
				MaxPositions:  3,
				CreatedByGCID: creatorGCID,
			},
			setupMocks: func(d testInternshipDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: zero max_positions returns ErrValidationFailed",
			input: &Internship{
				TenantID:      tenantID,
				Title:         "Valid Title",
				PartnerID:     partnerID,
				MaxPositions:  0,
				CreatedByGCID: creatorGCID,
			},
			setupMocks: func(d testInternshipDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: repo error propagated",
			input: &Internship{
				TenantID:      tenantID,
				Title:         "Valid Title",
				PartnerID:     partnerID,
				MaxPositions:  3,
				CreatedByGCID: creatorGCID,
			},
			setupMocks: func(d testInternshipDeps) {
				d.internR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.Internship")).
					Return(errors.New("db connection lost"))
			},
			wantErr: errors.New("db connection lost"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestInternshipService()
			tc.setupMocks(d)

			got, err := d.svc.CreateInternship(context.Background(), tc.input)

			if tc.wantErr != nil {
				require.Error(t, err)
				if errors.Is(tc.wantErr, ErrValidationFailed) {
					assert.ErrorIs(t, err, ErrValidationFailed)
				}
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.NotNil(t, got)
				if tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.internR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetInternship
// ---------------------------------------------------------------------------

func TestGetInternship(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		tenantID   uuid.UUID
		setupMocks func(d testInternshipDeps)
		wantErr    error
	}{
		{
			name:     "success: returns internship",
			id:       internshipID,
			tenantID: tenantID,
			setupMocks: func(d testInternshipDeps) {
				d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(&Internship{
					ID:       internshipID,
					TenantID: tenantID,
					Title:    "Existing Internship",
					Status:   InternshipStatusOpen,
				}, nil)
			},
		},
		{
			name:     "fails: not found",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupMocks: func(d testInternshipDeps) {
				d.internR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrInternshipNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestInternshipService()
			tc.setupMocks(d)

			got, err := d.svc.GetInternship(context.Background(), tc.id, tc.tenantID)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.NotNil(t, got)
			}

			d.internR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestApplyForInternship
// ---------------------------------------------------------------------------

func TestApplyForInternship(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())
	applicantGCID := uuid.Must(uuid.NewV7())

	openInternship := &Internship{
		ID:              internshipID,
		TenantID:        tenantID,
		Title:           "Open Internship",
		Status:          InternshipStatusOpen,
		MaxPositions:    3,
		FilledPositions: 1,
	}

	tests := []struct {
		name         string
		input        *InternshipApplication
		setupMocks   func(d testInternshipDeps)
		wantErr      error
		assertResult func(t *testing.T, got *InternshipApplication)
	}{
		{
			name: "success: submits application",
			input: &InternshipApplication{
				TenantID:      tenantID,
				InternshipID:  internshipID,
				ApplicantGCID: applicantGCID,
				CoverLetter:   "I am very interested",
			},
			setupMocks: func(d testInternshipDeps) {
				d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(openInternship, nil)
				d.appR.On("ListByApplicant", mock.Anything, internshipID, tenantID, applicantGCID).Return([]InternshipApplication{}, nil)
				d.appR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.InternshipApplication")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *InternshipApplication) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, ApplicationStatusSubmitted, got.Status)
				assert.False(t, got.SubmittedAt.IsZero())
			},
		},
		{
			name: "fails: internship not open",
			input: &InternshipApplication{
				TenantID:      tenantID,
				InternshipID:  internshipID,
				ApplicantGCID: applicantGCID,
			},
			setupMocks: func(d testInternshipDeps) {
				closedInternship := &Internship{
					ID:       internshipID,
					TenantID: tenantID,
					Status:   InternshipStatusClosed,
				}
				d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(closedInternship, nil)
			},
			wantErr: ErrInternshipNotOpen,
		},
		{
			name: "fails: internship full",
			input: &InternshipApplication{
				TenantID:      tenantID,
				InternshipID:  internshipID,
				ApplicantGCID: applicantGCID,
			},
			setupMocks: func(d testInternshipDeps) {
				fullInternship := &Internship{
					ID:              internshipID,
					TenantID:        tenantID,
					Status:          InternshipStatusOpen,
					MaxPositions:    2,
					FilledPositions: 2,
				}
				d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(fullInternship, nil)
			},
			wantErr: ErrInternshipFull,
		},
		{
			name: "fails: duplicate application",
			input: &InternshipApplication{
				TenantID:      tenantID,
				InternshipID:  internshipID,
				ApplicantGCID: applicantGCID,
			},
			setupMocks: func(d testInternshipDeps) {
				d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(openInternship, nil)
				d.appR.On("ListByApplicant", mock.Anything, internshipID, tenantID, applicantGCID).Return([]InternshipApplication{
					{ID: uuid.Must(uuid.NewV7()), ApplicantGCID: applicantGCID},
				}, nil)
			},
			wantErr: ErrDuplicateApplication,
		},
		{
			name: "fails: nil internship_id",
			input: &InternshipApplication{
				TenantID:      tenantID,
				InternshipID:  uuid.Nil,
				ApplicantGCID: applicantGCID,
			},
			setupMocks: func(d testInternshipDeps) {},
			wantErr:    ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestInternshipService()
			tc.setupMocks(d)

			got, err := d.svc.ApplyForInternship(context.Background(), tc.input)

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

			d.internR.AssertExpectations(t)
			d.appR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestDecideApplication
// ---------------------------------------------------------------------------

func TestDecideApplication(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	appID := uuid.Must(uuid.NewV7())
	reviewerGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		approved   bool
		reason     *string
		setupMocks func(d testInternshipDeps)
		wantErr    error
		wantStatus ApplicationStatus
	}{
		{
			name:     "success: approves application",
			approved: true,
			setupMocks: func(d testInternshipDeps) {
				d.appR.On("GetByID", mock.Anything, appID, tenantID).Return(&InternshipApplication{
					ID:       appID,
					TenantID: tenantID,
					Status:   ApplicationStatusSubmitted,
				}, nil)
				d.appR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.InternshipApplication")).Return(nil)
			},
			wantStatus: ApplicationStatusApproved,
		},
		{
			name:     "success: rejects application with reason",
			approved: false,
			reason:   strPtr("insufficient qualifications"),
			setupMocks: func(d testInternshipDeps) {
				d.appR.On("GetByID", mock.Anything, appID, tenantID).Return(&InternshipApplication{
					ID:       appID,
					TenantID: tenantID,
					Status:   ApplicationStatusSubmitted,
				}, nil)
				d.appR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.InternshipApplication")).Return(nil)
			},
			wantStatus: ApplicationStatusRejected,
		},
		{
			name:     "fails: rejection without reason",
			approved: false,
			reason:   nil,
			setupMocks: func(d testInternshipDeps) {
				d.appR.On("GetByID", mock.Anything, appID, tenantID).Return(&InternshipApplication{
					ID:       appID,
					TenantID: tenantID,
					Status:   ApplicationStatusSubmitted,
				}, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:     "fails: already decided",
			approved: true,
			setupMocks: func(d testInternshipDeps) {
				d.appR.On("GetByID", mock.Anything, appID, tenantID).Return(&InternshipApplication{
					ID:       appID,
					TenantID: tenantID,
					Status:   ApplicationStatusApproved,
				}, nil)
			},
			wantErr: ErrApplicationNotReviewable,
		},
		{
			name:     "fails: not found",
			approved: true,
			setupMocks: func(d testInternshipDeps) {
				d.appR.On("GetByID", mock.Anything, appID, tenantID).Return(nil, nil)
			},
			wantErr: ErrNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestInternshipService()
			tc.setupMocks(d)

			got, err := d.svc.DecideApplication(context.Background(), appID, tenantID, tc.approved, reviewerGCID, tc.reason)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.NotNil(t, got)
				assert.Equal(t, tc.wantStatus, got.Status)
				assert.NotNil(t, got.ReviewedByGCID)
				assert.NotNil(t, got.ReviewedAt)
			}

			d.appR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListInternships
// ---------------------------------------------------------------------------

func TestListInternships(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	t.Run("success: delegates to repository", func(t *testing.T) {
		t.Parallel()
		d := newTestInternshipService()
		expected := []Internship{
			{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Title: "Internship A"},
			{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Title: "Internship B"},
		}
		d.internR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 20).Return(expected, nil)

		got, err := d.svc.ListInternships(context.Background(), tenantID, nil, 20)

		require.NoError(t, err)
		assert.Len(t, got, 2)
		d.internR.AssertExpectations(t)
	})

	t.Run("propagates repo error", func(t *testing.T) {
		t.Parallel()
		d := newTestInternshipService()
		d.internR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 20).
			Return(nil, errors.New("connection refused"))

		got, err := d.svc.ListInternships(context.Background(), tenantID, nil, 20)

		require.Error(t, err)
		assert.Nil(t, got)
		d.internR.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// TestListApplications
// ---------------------------------------------------------------------------

func TestListApplications(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())

	t.Run("success: delegates to repository", func(t *testing.T) {
		t.Parallel()
		d := newTestInternshipService()
		expected := []InternshipApplication{
			{ID: uuid.Must(uuid.NewV7()), InternshipID: internshipID},
		}
		d.appR.On("ListByInternship", mock.Anything, internshipID, tenantID, (*uuid.UUID)(nil), 20).
			Return(expected, nil)

		got, err := d.svc.ListApplications(context.Background(), internshipID, tenantID, nil, 20)

		require.NoError(t, err)
		assert.Len(t, got, 1)
		d.appR.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// TestListPartners
// ---------------------------------------------------------------------------

func TestListPartners(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	t.Run("success: delegates to repository", func(t *testing.T) {
		t.Parallel()
		d := newTestInternshipService()
		expected := []IndustryPartner{
			{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Name: "Partner Inc"},
		}
		d.partnerR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 10).
			Return(expected, nil)

		got, err := d.svc.ListPartners(context.Background(), tenantID, nil, 10)

		require.NoError(t, err)
		assert.Len(t, got, 1)
		d.partnerR.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// TestGetInternship_RepoError
// ---------------------------------------------------------------------------

func TestGetInternship_RepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())

	d := newTestInternshipService()
	d.internR.On("GetByID", mock.Anything, internshipID, tenantID).
		Return(nil, errors.New("db timeout"))

	got, err := d.svc.GetInternship(context.Background(), internshipID, tenantID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "db timeout")
	assert.Nil(t, got)
	d.internR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestApplyForInternship_InternshipNotFound
// ---------------------------------------------------------------------------

func TestApplyForInternship_InternshipNotFound(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())
	applicantGCID := uuid.Must(uuid.NewV7())

	d := newTestInternshipService()
	d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(nil, nil)

	got, err := d.svc.ApplyForInternship(context.Background(), &InternshipApplication{
		TenantID:      tenantID,
		InternshipID:  internshipID,
		ApplicantGCID: applicantGCID,
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInternshipNotFound)
	assert.Nil(t, got)
	d.internR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestApplyForInternship_EventPublishError
// ---------------------------------------------------------------------------

func TestApplyForInternship_EventPublishError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())
	applicantGCID := uuid.Must(uuid.NewV7())

	d := newTestInternshipService()
	openInternship := &Internship{
		ID:              internshipID,
		TenantID:        tenantID,
		Status:          InternshipStatusOpen,
		MaxPositions:    5,
		FilledPositions: 0,
	}
	d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(openInternship, nil)
	d.appR.On("ListByApplicant", mock.Anything, internshipID, tenantID, applicantGCID).
		Return([]InternshipApplication{}, nil)
	d.appR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.InternshipApplication")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).
		Return(errors.New("pubsub unavailable"))

	got, err := d.svc.ApplyForInternship(context.Background(), &InternshipApplication{
		TenantID:      tenantID,
		InternshipID:  internshipID,
		ApplicantGCID: applicantGCID,
		CoverLetter:   "Excited to apply",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish application.submitted")
	assert.Nil(t, got)
	d.internR.AssertExpectations(t)
	d.appR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestApplyForInternship_RepoLookupError
// ---------------------------------------------------------------------------

func TestApplyForInternship_RepoLookupError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())
	applicantGCID := uuid.Must(uuid.NewV7())

	d := newTestInternshipService()
	d.internR.On("GetByID", mock.Anything, internshipID, tenantID).
		Return(nil, errors.New("db error"))

	got, err := d.svc.ApplyForInternship(context.Background(), &InternshipApplication{
		TenantID:      tenantID,
		InternshipID:  internshipID,
		ApplicantGCID: applicantGCID,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lookup internship")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestApplyForInternship_DuplicateCheckError
// ---------------------------------------------------------------------------

func TestApplyForInternship_DuplicateCheckError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	internshipID := uuid.Must(uuid.NewV7())
	applicantGCID := uuid.Must(uuid.NewV7())

	d := newTestInternshipService()
	openInternship := &Internship{
		ID:              internshipID,
		TenantID:        tenantID,
		Status:          InternshipStatusOpen,
		MaxPositions:    5,
		FilledPositions: 0,
	}
	d.internR.On("GetByID", mock.Anything, internshipID, tenantID).Return(openInternship, nil)
	d.appR.On("ListByApplicant", mock.Anything, internshipID, tenantID, applicantGCID).
		Return(nil, errors.New("db error"))

	got, err := d.svc.ApplyForInternship(context.Background(), &InternshipApplication{
		TenantID:      tenantID,
		InternshipID:  internshipID,
		ApplicantGCID: applicantGCID,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "check existing applications")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestDecideApplication_UnderReviewStatus
// ---------------------------------------------------------------------------

func TestDecideApplication_UnderReviewStatus(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	appID := uuid.Must(uuid.NewV7())
	reviewerGCID := uuid.Must(uuid.NewV7())

	d := newTestInternshipService()
	d.appR.On("GetByID", mock.Anything, appID, tenantID).Return(&InternshipApplication{
		ID:       appID,
		TenantID: tenantID,
		Status:   ApplicationStatusUnderReview,
	}, nil)
	d.appR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.InternshipApplication")).Return(nil)

	got, err := d.svc.DecideApplication(context.Background(), appID, tenantID, true, reviewerGCID, nil)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, ApplicationStatusApproved, got.Status)
	d.appR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestDecideApplication_RepoError
// ---------------------------------------------------------------------------

func TestDecideApplication_RepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	appID := uuid.Must(uuid.NewV7())
	reviewerGCID := uuid.Must(uuid.NewV7())

	d := newTestInternshipService()
	d.appR.On("GetByID", mock.Anything, appID, tenantID).
		Return(nil, errors.New("db error"))

	got, err := d.svc.DecideApplication(context.Background(), appID, tenantID, true, reviewerGCID, nil)

	require.Error(t, err)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestDecideApplication_UpdateError
// ---------------------------------------------------------------------------

func TestDecideApplication_UpdateError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	appID := uuid.Must(uuid.NewV7())
	reviewerGCID := uuid.Must(uuid.NewV7())

	d := newTestInternshipService()
	d.appR.On("GetByID", mock.Anything, appID, tenantID).Return(&InternshipApplication{
		ID:       appID,
		TenantID: tenantID,
		Status:   ApplicationStatusSubmitted,
	}, nil)
	d.appR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.InternshipApplication")).
		Return(errors.New("constraint violation"))

	got, err := d.svc.DecideApplication(context.Background(), appID, tenantID, true, reviewerGCID, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "update application")
	assert.Nil(t, got)
}

// strPtr is a helper that returns a pointer to the given string.
func strPtr(s string) *string {
	return &s
}
