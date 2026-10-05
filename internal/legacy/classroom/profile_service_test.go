package classroom

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// testProfileDeps holds all mocks wired into a ProfileService.
type testProfileDeps struct {
	svc      *ProfileService
	profileR *mockProfileRepo
}

// newTestProfileService creates a ProfileService with fresh mocks.
func newTestProfileService() testProfileDeps {
	pr := &mockProfileRepo{}
	return testProfileDeps{
		svc:      NewProfileService(pr),
		profileR: pr,
	}
}

// ---------------------------------------------------------------------------
// TestGetProfile
// ---------------------------------------------------------------------------

func TestGetProfile(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testProfileDeps)
		wantErr      error
		assertResult func(t *testing.T, got *ClassProfile)
	}{
		{
			name: "success: returns existing profile",
			setupMocks: func(d testProfileDeps) {
				d.profileR.On("GetBySessionID", mock.Anything, sessionID, tenantID).Return(&ClassProfile{
					ID:                 uuid.Must(uuid.NewV7()),
					TenantID:           tenantID,
					TrainingSessionID:  sessionID,
					TotalParticipants:  25,
					QuizCount:          3,
					PollCount:          2,
					JamBoardCount:      1,
					AvgEngagementScore: 78.5,
					TopParticipants: []TopParticipant{
						{GCID: uuid.Must(uuid.NewV7()), DisplayName: "Alice", Score: 95.0},
					},
				}, nil)
			},
			assertResult: func(t *testing.T, got *ClassProfile) {
				assert.Equal(t, tenantID, got.TenantID)
				assert.Equal(t, sessionID, got.TrainingSessionID)
				assert.Equal(t, 25, got.TotalParticipants)
				assert.Equal(t, 3, got.QuizCount)
				assert.Equal(t, 2, got.PollCount)
				assert.Equal(t, 1, got.JamBoardCount)
				assert.Equal(t, 78.5, got.AvgEngagementScore)
				assert.Len(t, got.TopParticipants, 1)
				assert.Equal(t, "Alice", got.TopParticipants[0].DisplayName)
			},
		},
		{
			name: "fails: profile not found returns ErrProfileNotFound",
			setupMocks: func(d testProfileDeps) {
				d.profileR.On("GetBySessionID", mock.Anything, sessionID, tenantID).Return(nil, nil)
			},
			wantErr: ErrProfileNotFound,
		},
		{
			name: "fails: repository error propagated",
			setupMocks: func(d testProfileDeps) {
				d.profileR.On("GetBySessionID", mock.Anything, sessionID, tenantID).Return(nil, errors.New("db connection lost"))
			},
			wantErr: nil, // not a sentinel error but still an error
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestProfileService()
			tc.setupMocks(d)

			got, err := d.svc.GetProfile(context.Background(), sessionID, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else if tc.assertResult != nil {
				assert.NoError(t, err)
				if assert.NotNil(t, got) {
					tc.assertResult(t, got)
				}
			} else {
				// repository error case
				assert.Error(t, err)
				assert.Nil(t, got)
			}

			d.profileR.AssertExpectations(t)
		})
	}
}
