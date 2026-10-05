package classroom

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// testPollDeps holds all mocks wired into a PollService.
type testPollDeps struct {
	svc       *PollService
	pollR     *mockPollRepo
	voteR     *mockVoteRepo
	publisher *mockEventPublisher
}

// newTestPollService creates a PollService with fresh mocks.
func newTestPollService() testPollDeps {
	pr := &mockPollRepo{}
	vr := &mockVoteRepo{}
	ep := &mockEventPublisher{}
	return testPollDeps{
		svc:       NewPollService(pr, vr, ep),
		pollR:     pr,
		voteR:     vr,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// TestCreatePoll
// ---------------------------------------------------------------------------

func TestCreatePoll(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *LivePollSession
		setupMocks   func(d testPollDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LivePollSession)
	}{
		{
			name: "success: creates poll with UUIDv7 and open status",
			input: &LivePollSession{
				TenantID:      tenantID,
				Question:      "What is your favorite language?",
				Options:       []string{"Go", "Rust", "Python", "TypeScript"},
				PollType:      PollTypeSingleChoice,
				CreatedByGCID: gcid,
			},
			setupMocks: func(d testPollDeps) {
				d.pollR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.LivePollSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *LivePollSession) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, PollStatusOpen, got.Status)
				assert.Equal(t, 0, got.VoteCount)
				assert.Equal(t, "What is your favorite language?", got.Question)
				assert.Len(t, got.Options, 4)
			},
		},
		{
			name: "fails: empty question returns ErrValidationFailed",
			input: &LivePollSession{
				TenantID:      tenantID,
				Question:      "",
				Options:       []string{"A", "B"},
				PollType:      PollTypeSingleChoice,
				CreatedByGCID: gcid,
			},
			setupMocks: func(d testPollDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: fewer than 2 options",
			input: &LivePollSession{
				TenantID:      tenantID,
				Question:      "Valid?",
				Options:       []string{"Only one"},
				PollType:      PollTypeSingleChoice,
				CreatedByGCID: gcid,
			},
			setupMocks: func(d testPollDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: invalid poll type",
			input: &LivePollSession{
				TenantID:      tenantID,
				Question:      "Valid?",
				Options:       []string{"A", "B"},
				PollType:      PollType("invalid_type"),
				CreatedByGCID: gcid,
			},
			setupMocks: func(d testPollDeps) {},
			wantErr:    ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestPollService()
			tc.setupMocks(d)

			got, err := d.svc.CreatePoll(context.Background(), tc.input)

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

			d.pollR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestCastVote
// ---------------------------------------------------------------------------

func TestCastVote(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	openPoll := &LivePollSession{
		ID:       pollID,
		TenantID: tenantID,
		Question: "Pick one",
		Options:  []string{"A", "B", "C"},
		PollType: PollTypeSingleChoice,
		Status:   PollStatusOpen,
	}

	tests := []struct {
		name                  string
		selectedOptionIndices []int
		setupMocks            func(d testPollDeps)
		wantErr               error
	}{
		{
			name:                  "success: valid single choice vote",
			selectedOptionIndices: []int{1},
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
				d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, nil)
				d.voteR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.PollVote")).Return(nil)
				d.voteR.On("CountByPoll", mock.Anything, pollID).Return(1, nil)
				d.pollR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LivePollSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
		},
		{
			name:                  "fails: already voted",
			selectedOptionIndices: []int{0},
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
				d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(true, nil)
			},
			wantErr: ErrAlreadyVoted,
		},
		{
			name:                  "fails: poll closed",
			selectedOptionIndices: []int{0},
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{
					ID:       pollID,
					TenantID: tenantID,
					Status:   PollStatusClosed,
				}, nil)
			},
			wantErr: ErrPollClosed,
		},
		{
			name:                  "fails: poll not found",
			selectedOptionIndices: []int{0},
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(nil, nil)
			},
			wantErr: ErrPollNotFound,
		},
		{
			name:                  "fails: invalid option index",
			selectedOptionIndices: []int{99},
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
				d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, nil)
			},
			wantErr: ErrInvalidOption,
		},
		{
			name:                  "fails: single_choice with multiple selections",
			selectedOptionIndices: []int{0, 1},
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
				d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, nil)
			},
			wantErr: ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestPollService()
			tc.setupMocks(d)

			err := d.svc.CastVote(context.Background(), pollID, tenantID, gcid, tc.selectedOptionIndices)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}

			d.pollR.AssertExpectations(t)
			d.voteR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetPollResults
// ---------------------------------------------------------------------------

func TestGetPollResults(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testPollDeps)
		wantErr      error
		assertResult func(t *testing.T, got *PollResults)
	}{
		{
			name: "success: returns percentage breakdown",
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{
					ID:       pollID,
					TenantID: tenantID,
					Question: "Favorite?",
					Options:  []string{"Go", "Rust", "Python"},
					Status:   PollStatusOpen,
				}, nil)
				d.voteR.On("ListByPoll", mock.Anything, pollID).Return([]PollVote{
					{GCID: uuid.Must(uuid.NewV7()), SelectedOptionIndices: []int{0}},
					{GCID: uuid.Must(uuid.NewV7()), SelectedOptionIndices: []int{0}},
					{GCID: uuid.Must(uuid.NewV7()), SelectedOptionIndices: []int{1}},
					{GCID: uuid.Must(uuid.NewV7()), SelectedOptionIndices: []int{2}},
				}, nil)
			},
			assertResult: func(t *testing.T, got *PollResults) {
				assert.Equal(t, pollID, got.PollID)
				assert.Equal(t, 4, got.TotalVotes)
				assert.Len(t, got.OptionResults, 3)
				assert.Equal(t, "Go", got.OptionResults[0].OptionText)
				assert.Equal(t, 2, got.OptionResults[0].VoteCount)
				assert.Equal(t, 50.0, got.OptionResults[0].Percentage)
				assert.Equal(t, 1, got.OptionResults[1].VoteCount)
				assert.Equal(t, 25.0, got.OptionResults[1].Percentage)
				assert.Equal(t, 1, got.OptionResults[2].VoteCount)
				assert.Equal(t, 25.0, got.OptionResults[2].Percentage)
			},
		},
		{
			name: "success: zero votes returns all zeros",
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{
					ID:       pollID,
					TenantID: tenantID,
					Question: "No votes?",
					Options:  []string{"A", "B"},
					Status:   PollStatusOpen,
				}, nil)
				d.voteR.On("ListByPoll", mock.Anything, pollID).Return([]PollVote{}, nil)
			},
			assertResult: func(t *testing.T, got *PollResults) {
				assert.Equal(t, 0, got.TotalVotes)
				assert.Equal(t, 0.0, got.OptionResults[0].Percentage)
				assert.Equal(t, 0.0, got.OptionResults[1].Percentage)
			},
		},
		{
			name: "fails: poll not found",
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(nil, nil)
			},
			wantErr: ErrPollNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestPollService()
			tc.setupMocks(d)

			got, err := d.svc.GetPollResults(context.Background(), pollID, tenantID)

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

			d.pollR.AssertExpectations(t)
			d.voteR.AssertExpectations(t)
		})
	}
}
