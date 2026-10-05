package classroom

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// testPollFullDeps wires a PollService created via NewPollServiceFull.
type testPollFullDeps struct {
	svc       *PollService
	pollR     *mockPollRepo
	voteR     *mockVoteRepo
	questionR *mockPollQuestionRepo
	liveVoteR *mockLivePollVoteRepo
	publisher *mockEventPublisher
}

func newTestPollFullService() testPollFullDeps {
	pr := &mockPollRepo{}
	vr := &mockVoteRepo{}
	qr := &mockPollQuestionRepo{}
	lvr := &mockLivePollVoteRepo{}
	ep := &mockEventPublisher{}
	return testPollFullDeps{
		svc:       NewPollServiceFull(pr, vr, qr, lvr, ep),
		pollR:     pr,
		voteR:     vr,
		questionR: qr,
		liveVoteR: lvr,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// PollService error-path top-ups
// ---------------------------------------------------------------------------

func TestCreatePoll_PublishError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	d := newTestPollFullService()
	d.pollR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.LivePollSession")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)

	_, err := d.svc.CreatePoll(context.Background(), &LivePollSession{
		TenantID: tenantID, Question: "Q?", Options: []string{"A", "B"}, PollType: PollTypeSingleChoice,
	})
	assert.ErrorIs(t, err, errTestClassroom)
	d.pollR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestCastVote_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	openPoll := &LivePollSession{ID: pollID, TenantID: tenantID, Options: []string{"A", "B", "C"}, PollType: PollTypeSingleChoice, Status: PollStatusOpen}

	t.Run("has voted check error", func(t *testing.T) {
		d := newTestPollFullService()
		d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
		d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, errTestClassroom)
		err := d.svc.CastVote(context.Background(), pollID, tenantID, gcid, []int{0})
		assert.ErrorIs(t, err, errTestClassroom)
		d.pollR.AssertExpectations(t)
		d.voteR.AssertExpectations(t)
	})

	t.Run("vote create error", func(t *testing.T) {
		d := newTestPollFullService()
		d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
		d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, nil)
		d.voteR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.PollVote")).Return(errTestClassroom)
		err := d.svc.CastVote(context.Background(), pollID, tenantID, gcid, []int{0})
		assert.ErrorIs(t, err, errTestClassroom)
		d.pollR.AssertExpectations(t)
		d.voteR.AssertExpectations(t)
	})

	t.Run("count votes error", func(t *testing.T) {
		d := newTestPollFullService()
		d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
		d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, nil)
		d.voteR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.PollVote")).Return(nil)
		d.voteR.On("CountByPoll", mock.Anything, pollID).Return(0, errTestClassroom)
		err := d.svc.CastVote(context.Background(), pollID, tenantID, gcid, []int{0})
		assert.ErrorIs(t, err, errTestClassroom)
		d.pollR.AssertExpectations(t)
		d.voteR.AssertExpectations(t)
	})

	t.Run("poll update error", func(t *testing.T) {
		d := newTestPollFullService()
		d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
		d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, nil)
		d.voteR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.PollVote")).Return(nil)
		d.voteR.On("CountByPoll", mock.Anything, pollID).Return(1, nil)
		d.pollR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LivePollSession")).Return(errTestClassroom)
		err := d.svc.CastVote(context.Background(), pollID, tenantID, gcid, []int{0})
		assert.ErrorIs(t, err, errTestClassroom)
		d.pollR.AssertExpectations(t)
		d.voteR.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestPollFullService()
		d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
		d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, nil)
		d.voteR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.PollVote")).Return(nil)
		d.voteR.On("CountByPoll", mock.Anything, pollID).Return(1, nil)
		d.pollR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LivePollSession")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
		err := d.svc.CastVote(context.Background(), pollID, tenantID, gcid, []int{0})
		assert.ErrorIs(t, err, errTestClassroom)
		d.pollR.AssertExpectations(t)
		d.voteR.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}

func TestGetPollResults_ListError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())

	d := newTestPollFullService()
	d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{ID: pollID, Options: []string{"A", "B"}}, nil)
	d.voteR.On("ListByPoll", mock.Anything, pollID).Return(nil, errTestClassroom)

	_, err := d.svc.GetPollResults(context.Background(), pollID, tenantID)
	assert.ErrorIs(t, err, errTestClassroom)
	d.pollR.AssertExpectations(t)
	d.voteR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestGetPollVisualization
// ---------------------------------------------------------------------------

func TestGetPollVisualization(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())

	votes := []PollVote{
		{SelectedOptionIndices: []int{0}},
		{SelectedOptionIndices: []int{0}},
		{SelectedOptionIndices: []int{1}},
	}

	tests := []struct {
		name         string
		pollType     PollType
		setupMocks   func(d testPollFullDeps)
		wantErr      error
		assertResult func(t *testing.T, got *PollVisualization)
	}{
		{
			name:     "success: word cloud chart type",
			pollType: PollTypeWordCloud,
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{ID: pollID, Question: "Q", Options: []string{"A", "B"}, PollType: PollTypeWordCloud}, nil)
				d.voteR.On("ListByPoll", mock.Anything, pollID).Return(votes, nil)
			},
			assertResult: func(t *testing.T, got *PollVisualization) {
				assert.Equal(t, "word_cloud", got.ChartType)
				assert.Equal(t, 3, got.TotalVotes)
				assert.Len(t, got.DataPoints, 2)
				assert.Equal(t, 2.0, got.DataPoints[0].Value)
				assert.InDelta(t, 66.67, got.DataPoints[0].Percentage, 0.01)
			},
		},
		{
			name:     "success: multiple choice uses bar chart",
			pollType: PollTypeMultipleChoice,
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{ID: pollID, Options: []string{"A", "B"}, PollType: PollTypeMultipleChoice}, nil)
				d.voteR.On("ListByPoll", mock.Anything, pollID).Return(votes, nil)
			},
			assertResult: func(t *testing.T, got *PollVisualization) {
				assert.Equal(t, "bar", got.ChartType)
			},
		},
		{
			name:     "success: single choice defaults to pie",
			pollType: PollTypeSingleChoice,
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{ID: pollID, Options: []string{"A", "B"}, PollType: PollTypeSingleChoice}, nil)
				d.voteR.On("ListByPoll", mock.Anything, pollID).Return(votes, nil)
			},
			assertResult: func(t *testing.T, got *PollVisualization) {
				assert.Equal(t, "pie", got.ChartType)
			},
		},
		{
			name:     "success: zero votes",
			pollType: PollTypeRatingScale,
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{ID: pollID, Options: []string{"A", "B"}, PollType: PollTypeRatingScale}, nil)
				d.voteR.On("ListByPoll", mock.Anything, pollID).Return([]PollVote{}, nil)
			},
			assertResult: func(t *testing.T, got *PollVisualization) {
				assert.Equal(t, "pie", got.ChartType)
				assert.Equal(t, 0, got.TotalVotes)
				assert.Equal(t, 0.0, got.DataPoints[0].Value)
			},
		},
		{
			name: "fails: poll not found",
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(nil, nil)
			},
			wantErr: ErrPollNotFound,
		},
		{
			name: "fails: list votes error",
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{ID: pollID, Options: []string{"A"}}, nil)
				d.voteR.On("ListByPoll", mock.Anything, pollID).Return(nil, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestPollFullService()
			tc.setupMocks(d)

			got, err := d.svc.GetPollVisualization(context.Background(), pollID, tenantID)

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

// ---------------------------------------------------------------------------
// TestCastAnonymousVote
// ---------------------------------------------------------------------------

func TestCastAnonymousVote(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())
	questionID := uuid.Must(uuid.NewV7())

	openPoll := &LivePollSession{ID: pollID, TenantID: tenantID, Status: PollStatusOpen}

	tests := []struct {
		name         string
		voteHash     string
		useFull      bool
		setupMocks   func(d testPollFullDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LivePollVote)
	}{
		{
			name:     "success: full service records vote",
			voteHash: "hash-1",
			useFull:  true,
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
				d.liveVoteR.On("ExistsByHash", mock.Anything, "hash-1").Return(false, nil)
				d.liveVoteR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.LivePollVote")).Return(nil)
			},
			assertResult: func(t *testing.T, got *LivePollVote) {
				assert.True(t, got.Anonymous)
				assert.Equal(t, questionID, got.QuestionID)
				assert.NotNil(t, got.VoteHash)
			},
		},
		{
			name:     "fails: duplicate vote hash",
			voteHash: "hash-dup",
			useFull:  true,
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
				d.liveVoteR.On("ExistsByHash", mock.Anything, "hash-dup").Return(true, nil)
			},
			wantErr: ErrDuplicateVote,
		},
		{
			name:     "fails: exists check error",
			voteHash: "hash-3",
			useFull:  true,
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
				d.liveVoteR.On("ExistsByHash", mock.Anything, "hash-3").Return(false, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
		{
			name:     "fails: vote create error",
			voteHash: "hash-4",
			useFull:  true,
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
				d.liveVoteR.On("ExistsByHash", mock.Anything, "hash-4").Return(false, nil)
				d.liveVoteR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.LivePollVote")).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
		{
			name:     "fails: poll closed",
			voteHash: "hash-5",
			useFull:  true,
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{ID: pollID, Status: PollStatusClosed}, nil)
			},
			wantErr: ErrPollClosed,
		},
		{
			name:     "fails: poll not found",
			voteHash: "hash-6",
			useFull:  true,
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(nil, nil)
			},
			wantErr: ErrPollNotFound,
		},
		{
			name:     "success: basic service skips vote persistence",
			voteHash: "",
			useFull:  false,
			setupMocks: func(d testPollFullDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(openPoll, nil)
			},
			assertResult: func(t *testing.T, got *LivePollVote) {
				assert.True(t, got.Anonymous)
				assert.NotNil(t, got.VoteHash)
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var d testPollFullDeps
			if tc.useFull {
				d = newTestPollFullService()
			} else {
				pr := &mockPollRepo{}
				vr := &mockVoteRepo{}
				ep := &mockEventPublisher{}
				d = testPollFullDeps{
					svc: NewPollService(pr, vr, ep), pollR: pr, voteR: vr, publisher: ep,
				}
			}
			tc.setupMocks(d)

			opts := map[string]interface{}{"option": "A"}
			got, err := d.svc.CastAnonymousVote(context.Background(), pollID, tenantID, questionID, opts, tc.voteHash)

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
			if tc.useFull {
				d.liveVoteR.AssertExpectations(t)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// JamBoard AddEntry error-path top-ups
// ---------------------------------------------------------------------------

func TestAddEntry_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	boardID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	openBoard := &JamBoard{ID: boardID, TenantID: tenantID, Status: BoardStatusOpen, EntryCount: 0}
	entry := &JamBoardEntry{EntryType: EntryTypeStickyNote, Content: "Hello"}

	t.Run("board not found", func(t *testing.T) {
		d := newTestJamBoardService()
		d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(nil, nil)
		_, err := d.svc.AddEntry(context.Background(), boardID, tenantID, gcid, entry)
		assert.ErrorIs(t, err, ErrBoardNotFound)
		d.boardR.AssertExpectations(t)
	})

	t.Run("entry create error", func(t *testing.T) {
		d := newTestJamBoardService()
		d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(openBoard, nil)
		d.entryR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.JamBoardEntry")).Return(errTestClassroom)
		_, err := d.svc.AddEntry(context.Background(), boardID, tenantID, gcid, entry)
		assert.ErrorIs(t, err, errTestClassroom)
		d.boardR.AssertExpectations(t)
		d.entryR.AssertExpectations(t)
	})

	t.Run("board update error", func(t *testing.T) {
		d := newTestJamBoardService()
		d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(openBoard, nil)
		d.entryR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.JamBoardEntry")).Return(nil)
		d.boardR.On("Update", mock.Anything, mock.Anything).Return(errTestClassroom)
		_, err := d.svc.AddEntry(context.Background(), boardID, tenantID, gcid, entry)
		assert.ErrorIs(t, err, errTestClassroom)
		d.boardR.AssertExpectations(t)
		d.entryR.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestJamBoardService()
		d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(openBoard, nil)
		d.entryR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.JamBoardEntry")).Return(nil)
		d.boardR.On("Update", mock.Anything, mock.Anything).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
		_, err := d.svc.AddEntry(context.Background(), boardID, tenantID, gcid, entry)
		assert.ErrorIs(t, err, errTestClassroom)
		d.boardR.AssertExpectations(t)
		d.entryR.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}
