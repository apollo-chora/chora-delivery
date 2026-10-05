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

// testCapstoneDeps holds all mocks wired into a CapstoneService.
type testCapstoneDeps struct {
	svc       *CapstoneService
	capstoneR *mockCapstoneRepo
	submitR   *mockSubmissionRepo
	publisher *mockEventPublisher
}

func newTestCapstoneService() testCapstoneDeps {
	cr := &mockCapstoneRepo{}
	sr := &mockSubmissionRepo{}
	ep := &mockEventPublisher{}
	return testCapstoneDeps{
		svc:       NewCapstoneService(cr, sr, ep),
		capstoneR: cr,
		submitR:   sr,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// TestCreateCapstone
// ---------------------------------------------------------------------------

func TestCreateCapstone(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	creatorGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *CapstoneProject
		setupMocks   func(d testCapstoneDeps)
		wantErr      error
		assertResult func(t *testing.T, got *CapstoneProject)
	}{
		{
			name: "success: creates capstone with draft status",
			input: &CapstoneProject{
				TenantID:      tenantID,
				Title:         "E-Commerce API",
				Description:   "Build a full e-commerce API",
				CreatedByGCID: creatorGCID,
			},
			setupMocks: func(d testCapstoneDeps) {
				d.capstoneR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.CapstoneProject")).Return(nil)
			},
			assertResult: func(t *testing.T, got *CapstoneProject) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, CapstoneStatusDraft, got.Status)
				assert.Equal(t, "E-Commerce API", got.Title)
			},
		},
		{
			name: "fails: empty title",
			input: &CapstoneProject{
				TenantID:      tenantID,
				Title:         "",
				CreatedByGCID: creatorGCID,
			},
			setupMocks: func(d testCapstoneDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: repo error",
			input: &CapstoneProject{
				TenantID:      tenantID,
				Title:         "Valid Title",
				CreatedByGCID: creatorGCID,
			},
			setupMocks: func(d testCapstoneDeps) {
				d.capstoneR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.CapstoneProject")).
					Return(errors.New("db error"))
			},
			wantErr: errors.New("db error"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestCapstoneService()
			tc.setupMocks(d)

			got, err := d.svc.CreateCapstone(context.Background(), tc.input)

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

			d.capstoneR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetCapstone
// ---------------------------------------------------------------------------

func TestGetCapstone(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	capstoneID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		setupMocks func(d testCapstoneDeps)
		wantErr    error
	}{
		{
			name: "success: returns capstone",
			id:   capstoneID,
			setupMocks: func(d testCapstoneDeps) {
				d.capstoneR.On("GetByID", mock.Anything, capstoneID, tenantID).Return(&CapstoneProject{
					ID:       capstoneID,
					TenantID: tenantID,
					Title:    "Test Capstone",
				}, nil)
			},
		},
		{
			name: "fails: not found",
			id:   uuid.Must(uuid.NewV7()),
			setupMocks: func(d testCapstoneDeps) {
				d.capstoneR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrCapstoneNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestCapstoneService()
			tc.setupMocks(d)

			got, err := d.svc.GetCapstone(context.Background(), tc.id, tenantID)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.NotNil(t, got)
			}

			d.capstoneR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestSubmitCapstone
// ---------------------------------------------------------------------------

func TestSubmitCapstone(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	capstoneID := uuid.Must(uuid.NewV7())
	submitterGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *CapstoneSubmission
		setupMocks   func(d testCapstoneDeps)
		wantErr      error
		assertResult func(t *testing.T, got *CapstoneSubmission)
	}{
		{
			name: "success: submits capstone and transitions to active",
			input: &CapstoneSubmission{
				TenantID:      tenantID,
				CapstoneID:    capstoneID,
				SubmitterGCID: submitterGCID,
				SubmissionURL: "https://github.com/example/capstone",
				Notes:         "First submission",
			},
			setupMocks: func(d testCapstoneDeps) {
				d.capstoneR.On("GetByID", mock.Anything, capstoneID, tenantID).Return(&CapstoneProject{
					ID:       capstoneID,
					TenantID: tenantID,
					Status:   CapstoneStatusDraft,
				}, nil)
				d.submitR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.CapstoneSubmission")).Return(nil)
				d.capstoneR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.CapstoneProject")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *CapstoneSubmission) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, "https://github.com/example/capstone", got.SubmissionURL)
				assert.False(t, got.SubmittedAt.IsZero())
			},
		},
		{
			name: "success: submits to active capstone (no status change)",
			input: &CapstoneSubmission{
				TenantID:      tenantID,
				CapstoneID:    capstoneID,
				SubmitterGCID: submitterGCID,
				SubmissionURL: "https://github.com/example/v2",
			},
			setupMocks: func(d testCapstoneDeps) {
				d.capstoneR.On("GetByID", mock.Anything, capstoneID, tenantID).Return(&CapstoneProject{
					ID:       capstoneID,
					TenantID: tenantID,
					Status:   CapstoneStatusActive,
				}, nil)
				d.submitR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.CapstoneSubmission")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *CapstoneSubmission) {
				assert.NotEqual(t, uuid.Nil, got.ID)
			},
		},
		{
			name: "fails: empty submission_url",
			input: &CapstoneSubmission{
				TenantID:      tenantID,
				CapstoneID:    capstoneID,
				SubmitterGCID: submitterGCID,
				SubmissionURL: "",
			},
			setupMocks: func(d testCapstoneDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: capstone not found",
			input: &CapstoneSubmission{
				TenantID:      tenantID,
				CapstoneID:    uuid.Must(uuid.NewV7()),
				SubmitterGCID: submitterGCID,
				SubmissionURL: "https://example.com",
			},
			setupMocks: func(d testCapstoneDeps) {
				d.capstoneR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrCapstoneNotFound,
		},
		{
			name: "fails: capstone completed (not submittable)",
			input: &CapstoneSubmission{
				TenantID:      tenantID,
				CapstoneID:    capstoneID,
				SubmitterGCID: submitterGCID,
				SubmissionURL: "https://example.com",
			},
			setupMocks: func(d testCapstoneDeps) {
				d.capstoneR.On("GetByID", mock.Anything, capstoneID, tenantID).Return(&CapstoneProject{
					ID:       capstoneID,
					TenantID: tenantID,
					Status:   CapstoneStatusCompleted,
				}, nil)
			},
			wantErr: ErrCapstoneNotSubmittable,
		},
		{
			name: "fails: capstone archived (not submittable)",
			input: &CapstoneSubmission{
				TenantID:      tenantID,
				CapstoneID:    capstoneID,
				SubmitterGCID: submitterGCID,
				SubmissionURL: "https://example.com",
			},
			setupMocks: func(d testCapstoneDeps) {
				d.capstoneR.On("GetByID", mock.Anything, capstoneID, tenantID).Return(&CapstoneProject{
					ID:       capstoneID,
					TenantID: tenantID,
					Status:   CapstoneStatusArchived,
				}, nil)
			},
			wantErr: ErrCapstoneNotSubmittable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestCapstoneService()
			tc.setupMocks(d)

			got, err := d.svc.SubmitCapstone(context.Background(), tc.input)

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

			d.capstoneR.AssertExpectations(t)
			d.submitR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListCapstones
// ---------------------------------------------------------------------------

func TestListCapstones(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	t.Run("success: delegates to repository", func(t *testing.T) {
		t.Parallel()
		d := newTestCapstoneService()
		expected := []CapstoneProject{
			{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Title: "Project Alpha"},
			{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Title: "Project Beta"},
		}
		d.capstoneR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 25).Return(expected, nil)

		got, err := d.svc.ListCapstones(context.Background(), tenantID, nil, 25)

		require.NoError(t, err)
		assert.Len(t, got, 2)
		d.capstoneR.AssertExpectations(t)
	})

	t.Run("propagates repo error", func(t *testing.T) {
		t.Parallel()
		d := newTestCapstoneService()
		d.capstoneR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 25).
			Return(nil, errors.New("db unavailable"))

		got, err := d.svc.ListCapstones(context.Background(), tenantID, nil, 25)

		require.Error(t, err)
		assert.Nil(t, got)
		d.capstoneR.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// TestGetCapstone_RepoError
// ---------------------------------------------------------------------------

func TestGetCapstone_RepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	capstoneID := uuid.Must(uuid.NewV7())

	d := newTestCapstoneService()
	d.capstoneR.On("GetByID", mock.Anything, capstoneID, tenantID).
		Return(nil, errors.New("db timeout"))

	got, err := d.svc.GetCapstone(context.Background(), capstoneID, tenantID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "db timeout")
	assert.Nil(t, got)
	d.capstoneR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestSubmitCapstone_LookupError
// ---------------------------------------------------------------------------

func TestSubmitCapstone_LookupError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	capstoneID := uuid.Must(uuid.NewV7())

	d := newTestCapstoneService()
	d.capstoneR.On("GetByID", mock.Anything, capstoneID, tenantID).
		Return(nil, errors.New("db error"))

	got, err := d.svc.SubmitCapstone(context.Background(), &CapstoneSubmission{
		TenantID:      tenantID,
		CapstoneID:    capstoneID,
		SubmitterGCID: uuid.Must(uuid.NewV7()),
		SubmissionURL: "https://example.com",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lookup capstone")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestSubmitCapstone_SubmissionCreateError
// ---------------------------------------------------------------------------

func TestSubmitCapstone_SubmissionCreateError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	capstoneID := uuid.Must(uuid.NewV7())

	d := newTestCapstoneService()
	d.capstoneR.On("GetByID", mock.Anything, capstoneID, tenantID).Return(&CapstoneProject{
		ID:       capstoneID,
		TenantID: tenantID,
		Status:   CapstoneStatusActive,
	}, nil)
	d.submitR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.CapstoneSubmission")).
		Return(errors.New("insert failed"))

	got, err := d.svc.SubmitCapstone(context.Background(), &CapstoneSubmission{
		TenantID:      tenantID,
		CapstoneID:    capstoneID,
		SubmitterGCID: uuid.Must(uuid.NewV7()),
		SubmissionURL: "https://example.com",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "insert failed")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestSubmitCapstone_DraftToActiveUpdateError
// ---------------------------------------------------------------------------

func TestSubmitCapstone_DraftToActiveUpdateError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	capstoneID := uuid.Must(uuid.NewV7())

	d := newTestCapstoneService()
	d.capstoneR.On("GetByID", mock.Anything, capstoneID, tenantID).Return(&CapstoneProject{
		ID:       capstoneID,
		TenantID: tenantID,
		Status:   CapstoneStatusDraft,
	}, nil)
	d.submitR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.CapstoneSubmission")).Return(nil)
	d.capstoneR.On("Update", mock.Anything, mock.AnythingOfType("*wbl.CapstoneProject")).
		Return(errors.New("update conflict"))

	got, err := d.svc.SubmitCapstone(context.Background(), &CapstoneSubmission{
		TenantID:      tenantID,
		CapstoneID:    capstoneID,
		SubmitterGCID: uuid.Must(uuid.NewV7()),
		SubmissionURL: "https://example.com",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "update capstone status")
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestSubmitCapstone_EventPublishError
// ---------------------------------------------------------------------------

func TestSubmitCapstone_EventPublishError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	capstoneID := uuid.Must(uuid.NewV7())

	d := newTestCapstoneService()
	d.capstoneR.On("GetByID", mock.Anything, capstoneID, tenantID).Return(&CapstoneProject{
		ID:       capstoneID,
		TenantID: tenantID,
		Status:   CapstoneStatusActive,
	}, nil)
	d.submitR.On("Create", mock.Anything, mock.AnythingOfType("*wbl.CapstoneSubmission")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).
		Return(errors.New("pubsub unreachable"))

	got, err := d.svc.SubmitCapstone(context.Background(), &CapstoneSubmission{
		TenantID:      tenantID,
		CapstoneID:    capstoneID,
		SubmitterGCID: uuid.Must(uuid.NewV7()),
		SubmissionURL: "https://example.com",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish capstone.submitted")
	assert.Nil(t, got)
}
