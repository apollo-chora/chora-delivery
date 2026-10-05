package classroom

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// TestGetQuiz — covers quiz_service.go:73 GetQuiz (0% -> 100%)
// ---------------------------------------------------------------------------

func TestGetQuiz(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testQuizDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LiveQuizSession)
	}{
		{
			name: "success: returns existing quiz",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Title:    "Found Quiz",
					Status:   QuizStatusWaiting,
				}, nil)
			},
			assertResult: func(t *testing.T, got *LiveQuizSession) {
				assert.Equal(t, quizID, got.ID)
				assert.Equal(t, "Found Quiz", got.Title)
				assert.Equal(t, QuizStatusWaiting, got.Status)
			},
		},
		{
			name: "fails: quiz not found returns ErrQuizNotFound",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
		{
			name: "fails: repository error propagated",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errors.New("db error"))
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestQuizService()
			tc.setupMocks(d)

			got, err := d.svc.GetQuiz(context.Background(), quizID, tenantID)

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
				// generic error case
				assert.Error(t, err)
				assert.Nil(t, got)
			}

			d.quizR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetJamBoard — covers jamboard_service.go:56 GetJamBoard (0% -> 100%)
// ---------------------------------------------------------------------------

func TestGetJamBoard(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	boardID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testJamBoardDeps)
		wantErr      error
		assertResult func(t *testing.T, got *JamBoardWithEntries)
	}{
		{
			name: "success: returns board with entries",
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(&JamBoard{
					ID:         boardID,
					TenantID:   tenantID,
					Title:      "Test Board",
					Status:     BoardStatusOpen,
					EntryCount: 2,
				}, nil)
				d.entryR.On("ListByBoard", mock.Anything, boardID).Return([]JamBoardEntry{
					{ID: uuid.Must(uuid.NewV7()), BoardID: boardID, EntryType: EntryTypeStickyNote, Content: "Note 1", CreatedByGCID: gcid},
					{ID: uuid.Must(uuid.NewV7()), BoardID: boardID, EntryType: EntryTypeLink, Content: "https://example.com", CreatedByGCID: gcid},
				}, nil)
			},
			assertResult: func(t *testing.T, got *JamBoardWithEntries) {
				assert.Equal(t, boardID, got.ID)
				assert.Equal(t, "Test Board", got.Title)
				assert.Equal(t, BoardStatusOpen, got.Status)
				assert.Len(t, got.Entries, 2)
				assert.Equal(t, EntryTypeStickyNote, got.Entries[0].EntryType)
				assert.Equal(t, EntryTypeLink, got.Entries[1].EntryType)
			},
		},
		{
			name: "success: returns board with no entries",
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(&JamBoard{
					ID:         boardID,
					TenantID:   tenantID,
					Title:      "Empty Board",
					Status:     BoardStatusOpen,
					EntryCount: 0,
				}, nil)
				d.entryR.On("ListByBoard", mock.Anything, boardID).Return([]JamBoardEntry{}, nil)
			},
			assertResult: func(t *testing.T, got *JamBoardWithEntries) {
				assert.Equal(t, "Empty Board", got.Title)
				assert.Empty(t, got.Entries)
			},
		},
		{
			name: "fails: board not found returns ErrBoardNotFound",
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(nil, nil)
			},
			wantErr: ErrBoardNotFound,
		},
		{
			name: "fails: repository error on GetByID propagated",
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(nil, errors.New("db error"))
			},
		},
		{
			name: "fails: entry list error propagated",
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(&JamBoard{
					ID:       boardID,
					TenantID: tenantID,
					Title:    "Board",
					Status:   BoardStatusOpen,
				}, nil)
				d.entryR.On("ListByBoard", mock.Anything, boardID).Return(nil, errors.New("entry list error"))
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestJamBoardService()
			tc.setupMocks(d)

			got, err := d.svc.GetJamBoard(context.Background(), boardID, tenantID)

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
				assert.Error(t, err)
				assert.Nil(t, got)
			}

			d.boardR.AssertExpectations(t)
			d.entryR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestCalculatePoints — covers models.go:296 (85.7% -> 100%)
// ---------------------------------------------------------------------------

func TestCalculatePoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		correct          bool
		timeTakenMs      int
		timeLimitSeconds int
		wantPoints       int
	}{
		{
			name:             "incorrect answer always returns 0",
			correct:          false,
			timeTakenMs:      1000,
			timeLimitSeconds: 30,
			wantPoints:       0,
		},
		{
			name:             "correct answer at exactly time limit returns base points only",
			correct:          true,
			timeTakenMs:      30000,
			timeLimitSeconds: 30,
			wantPoints:       BasePointsCorrect, // 100, no bonus
		},
		{
			name:             "correct answer over time limit returns base points only",
			correct:          true,
			timeTakenMs:      35000,
			timeLimitSeconds: 30,
			wantPoints:       BasePointsCorrect, // 100
		},
		{
			name:             "correct answer instantly returns base + max bonus",
			correct:          true,
			timeTakenMs:      0,
			timeLimitSeconds: 30,
			wantPoints:       BasePointsCorrect + TimeBonusMax, // 150
		},
		{
			name:             "correct answer at half time returns base + half bonus",
			correct:          true,
			timeTakenMs:      15000,
			timeLimitSeconds: 30,
			wantPoints:       BasePointsCorrect + TimeBonusMax/2, // 125
		},
		{
			name:             "correct answer with custom time limit",
			correct:          true,
			timeTakenMs:      0,
			timeLimitSeconds: 60,
			wantPoints:       BasePointsCorrect + TimeBonusMax, // 150
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := CalculatePoints(tc.correct, tc.timeTakenMs, tc.timeLimitSeconds)
			assert.Equal(t, tc.wantPoints, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestCastVoteMultipleChoice — covers poll_service.go CastVote multiple_choice path
// ---------------------------------------------------------------------------

func TestCastVoteMultipleChoice(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name                  string
		selectedOptionIndices []int
		setupMocks            func(d testPollDeps)
		wantErr               error
	}{
		{
			name:                  "success: multiple_choice poll allows multiple selections",
			selectedOptionIndices: []int{0, 2},
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{
					ID:       pollID,
					TenantID: tenantID,
					Question: "Pick many",
					Options:  []string{"A", "B", "C"},
					PollType: PollTypeMultipleChoice,
					Status:   PollStatusOpen,
				}, nil)
				d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, nil)
				d.voteR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.PollVote")).Return(nil)
				d.voteR.On("CountByPoll", mock.Anything, pollID).Return(1, nil)
				d.pollR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LivePollSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
		},
		{
			name:                  "success: word_cloud poll allows single selection",
			selectedOptionIndices: []int{1},
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{
					ID:       pollID,
					TenantID: tenantID,
					Question: "Word cloud",
					Options:  []string{"Word1", "Word2", "Word3"},
					PollType: PollTypeWordCloud,
					Status:   PollStatusOpen,
				}, nil)
				d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, nil)
				d.voteR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.PollVote")).Return(nil)
				d.voteR.On("CountByPoll", mock.Anything, pollID).Return(1, nil)
				d.pollR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LivePollSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
		},
		{
			name:                  "fails: negative option index",
			selectedOptionIndices: []int{-1},
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{
					ID:       pollID,
					TenantID: tenantID,
					Question: "Pick",
					Options:  []string{"A", "B"},
					PollType: PollTypeMultipleChoice,
					Status:   PollStatusOpen,
				}, nil)
				d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, nil)
			},
			wantErr: ErrInvalidOption,
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
// TestGetPollResultsMultipleChoiceAggregation — covers multi-select aggregation
// ---------------------------------------------------------------------------

func TestGetPollResultsMultipleChoiceAggregation(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())

	d := newTestPollService()

	d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{
		ID:       pollID,
		TenantID: tenantID,
		Question: "Select all that apply",
		Options:  []string{"Go", "Rust", "Python"},
		PollType: PollTypeMultipleChoice,
		Status:   PollStatusClosed,
	}, nil)

	// Two voters: voter1 picks Go+Python, voter2 picks Go+Rust
	d.voteR.On("ListByPoll", mock.Anything, pollID).Return([]PollVote{
		{GCID: uuid.Must(uuid.NewV7()), SelectedOptionIndices: []int{0, 2}},
		{GCID: uuid.Must(uuid.NewV7()), SelectedOptionIndices: []int{0, 1}},
	}, nil)

	got, err := d.svc.GetPollResults(context.Background(), pollID, tenantID)

	assert.NoError(t, err)
	if assert.NotNil(t, got) {
		assert.Equal(t, 2, got.TotalVotes)
		assert.Len(t, got.OptionResults, 3)
		// Go picked by both voters
		assert.Equal(t, "Go", got.OptionResults[0].OptionText)
		assert.Equal(t, 2, got.OptionResults[0].VoteCount)
		assert.Equal(t, 100.0, got.OptionResults[0].Percentage)
		// Rust picked by 1 voter
		assert.Equal(t, "Rust", got.OptionResults[1].OptionText)
		assert.Equal(t, 1, got.OptionResults[1].VoteCount)
		assert.Equal(t, 50.0, got.OptionResults[1].Percentage)
		// Python picked by 1 voter
		assert.Equal(t, "Python", got.OptionResults[2].OptionText)
		assert.Equal(t, 1, got.OptionResults[2].VoteCount)
		assert.Equal(t, 50.0, got.OptionResults[2].Percentage)
	}

	d.pollR.AssertExpectations(t)
	d.voteR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestSubmitAnswerNegativeOptionIndex — covers the negative index branch
// ---------------------------------------------------------------------------

func TestSubmitAnswerNegativeOptionIndex(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	participantID := uuid.Must(uuid.NewV7())

	d := newTestQuizService()

	activeQuiz := &LiveQuizSession{
		ID:       quizID,
		TenantID: tenantID,
		Status:   QuizStatusActive,
		Questions: []QuizQuestion{
			{QuestionText: "Q1?", Options: []string{"A", "B", "C", "D"}, CorrectOptionIndex: 0},
		},
		CurrentQuestionIndex: 0,
		TimeLimitSeconds:     30,
	}

	participant := &QuizParticipant{
		ID:            participantID,
		QuizSessionID: quizID,
		GCID:          gcid,
	}

	d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(activeQuiz, nil)
	d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(participant, nil)
	d.participantR.On("HasAnswered", mock.Anything, participantID, 0).Return(false, nil)

	got, err := d.svc.SubmitAnswer(context.Background(), quizID, tenantID, gcid, -1, 5000)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidOption)
	assert.Nil(t, got)

	d.quizR.AssertExpectations(t)
	d.participantR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestCreateQuizQuestionTextEmpty — covers the empty question text validation
// ---------------------------------------------------------------------------

func TestCreateQuizQuestionTextEmpty(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d := newTestQuizService()

	quiz := &LiveQuizSession{
		TenantID: tenantID,
		Title:    "Quiz with blank question",
		Questions: []QuizQuestion{
			{QuestionText: "", Options: []string{"A", "B"}, CorrectOptionIndex: 0},
		},
		CreatedByGCID: gcid,
	}

	got, err := d.svc.CreateQuiz(context.Background(), quiz)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrValidationFailed)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestCreateQuizNegativeCorrectOptionIndex — covers negative index validation
// ---------------------------------------------------------------------------

func TestCreateQuizNegativeCorrectOptionIndex(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d := newTestQuizService()

	quiz := &LiveQuizSession{
		TenantID: tenantID,
		Title:    "Quiz with negative index",
		Questions: []QuizQuestion{
			{QuestionText: "Q?", Options: []string{"A", "B"}, CorrectOptionIndex: -1},
		},
		CreatedByGCID: gcid,
	}

	got, err := d.svc.CreateQuiz(context.Background(), quiz)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrValidationFailed)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// TestStartQuizRepoError — covers StartQuiz when GetByID returns error
// ---------------------------------------------------------------------------

func TestStartQuizRepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	d := newTestQuizService()
	d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errors.New("db timeout"))

	got, err := d.svc.StartQuiz(context.Background(), quizID, tenantID)

	assert.Error(t, err)
	assert.Nil(t, got)
	d.quizR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestEndQuizRepoError — covers EndQuiz when GetByID returns error
// ---------------------------------------------------------------------------

func TestEndQuizRepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	d := newTestQuizService()
	d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errors.New("db timeout"))

	got, err := d.svc.EndQuiz(context.Background(), quizID, tenantID)

	assert.Error(t, err)
	assert.Nil(t, got)
	d.quizR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestAdvanceQuestionRepoError — covers AdvanceQuestion when GetByID returns error
// ---------------------------------------------------------------------------

func TestAdvanceQuestionRepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	d := newTestQuizService()
	d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errors.New("db timeout"))

	got, err := d.svc.AdvanceQuestion(context.Background(), quizID, tenantID)

	assert.Error(t, err)
	assert.Nil(t, got)
	d.quizR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestGetLeaderboardRepoError — covers GetLeaderboard when GetByID returns error
// ---------------------------------------------------------------------------

func TestGetLeaderboardRepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	d := newTestQuizService()
	d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errors.New("db timeout"))

	got, err := d.svc.GetLeaderboard(context.Background(), quizID, tenantID)

	assert.Error(t, err)
	assert.Nil(t, got)
	d.quizR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestJoinQuizRepoError — covers JoinQuiz when GetByID returns error
// ---------------------------------------------------------------------------

func TestJoinQuizRepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d := newTestQuizService()
	d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errors.New("db timeout"))

	got, err := d.svc.JoinQuiz(context.Background(), quizID, tenantID, gcid, "Player")

	assert.Error(t, err)
	assert.Nil(t, got)
	d.quizR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestSubmitAnswerRepoError — covers SubmitAnswer when GetByID returns error
// ---------------------------------------------------------------------------

func TestSubmitAnswerRepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d := newTestQuizService()
	d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errors.New("db timeout"))

	got, err := d.svc.SubmitAnswer(context.Background(), quizID, tenantID, gcid, 0, 5000)

	assert.Error(t, err)
	assert.Nil(t, got)
	d.quizR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestCastVoteRepoErrors — covers CastVote repository error paths
// ---------------------------------------------------------------------------

func TestCastVoteRepoErrors(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testPollDeps)
	}{
		{
			name: "GetByID returns error",
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(nil, errors.New("db error"))
			},
		},
		{
			name: "HasVoted returns error",
			setupMocks: func(d testPollDeps) {
				d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(&LivePollSession{
					ID:       pollID,
					TenantID: tenantID,
					Options:  []string{"A", "B"},
					PollType: PollTypeSingleChoice,
					Status:   PollStatusOpen,
				}, nil)
				d.voteR.On("HasVoted", mock.Anything, pollID, gcid).Return(false, errors.New("vote check error"))
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestPollService()
			tc.setupMocks(d)

			err := d.svc.CastVote(context.Background(), pollID, tenantID, gcid, []int{0})

			assert.Error(t, err)
			d.pollR.AssertExpectations(t)
			d.voteR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetPollResultsRepoError — covers GetPollResults when GetByID returns error
// ---------------------------------------------------------------------------

func TestGetPollResultsRepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())

	d := newTestPollService()
	d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(nil, errors.New("db error"))

	got, err := d.svc.GetPollResults(context.Background(), pollID, tenantID)

	assert.Error(t, err)
	assert.Nil(t, got)
	d.pollR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestCreateJamBoardRepoError — covers CreateJamBoard when Create returns error
// ---------------------------------------------------------------------------

func TestCreateJamBoardRepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d := newTestJamBoardService()
	d.boardR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.JamBoard")).Return(errors.New("db error"))

	got, err := d.svc.CreateJamBoard(context.Background(), &JamBoard{
		TenantID:      tenantID,
		Title:         "Board",
		CreatedByGCID: gcid,
	})

	assert.Error(t, err)
	assert.Nil(t, got)
	d.boardR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestAddEntryDrawingType — covers drawing entry type path in AddEntry
// ---------------------------------------------------------------------------

func TestAddEntryDrawingType(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	boardID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d := newTestJamBoardService()

	d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(&JamBoard{
		ID:         boardID,
		TenantID:   tenantID,
		Title:      "Drawing Board",
		Status:     BoardStatusOpen,
		EntryCount: 0,
	}, nil)
	d.entryR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.JamBoardEntry")).Return(nil)
	d.boardR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.JamBoard")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)

	color := "#FF0000"
	posX := 100.0
	posY := 200.0

	got, err := d.svc.AddEntry(context.Background(), boardID, tenantID, gcid, &JamBoardEntry{
		EntryType: EntryTypeDrawing,
		Content:   "base64-drawing-data",
		Color:     &color,
		PositionX: &posX,
		PositionY: &posY,
	})

	assert.NoError(t, err)
	if assert.NotNil(t, got) {
		assert.Equal(t, EntryTypeDrawing, got.EntryType)
		assert.Equal(t, "base64-drawing-data", got.Content)
		assert.Equal(t, &color, got.Color)
		assert.Equal(t, &posX, got.PositionX)
		assert.Equal(t, &posY, got.PositionY)
	}

	d.boardR.AssertExpectations(t)
	d.entryR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestCreateQuizRepoError — covers CreateQuiz when Create returns error
// ---------------------------------------------------------------------------

func TestCreateQuizRepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d := newTestQuizService()
	d.quizR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(errors.New("db error"))

	got, err := d.svc.CreateQuiz(context.Background(), &LiveQuizSession{
		TenantID: tenantID,
		Title:    "Quiz",
		Questions: []QuizQuestion{
			{QuestionText: "Q?", Options: []string{"A", "B"}, CorrectOptionIndex: 0},
		},
		CreatedByGCID: gcid,
	})

	assert.Error(t, err)
	assert.Nil(t, got)
	d.quizR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestCreatePollRepoError — covers CreatePoll when Create returns error
// ---------------------------------------------------------------------------

func TestCreatePollRepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d := newTestPollService()
	d.pollR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.LivePollSession")).Return(errors.New("db error"))

	got, err := d.svc.CreatePoll(context.Background(), &LivePollSession{
		TenantID:      tenantID,
		Question:      "Pick?",
		Options:       []string{"A", "B"},
		PollType:      PollTypeSingleChoice,
		CreatedByGCID: gcid,
	})

	assert.Error(t, err)
	assert.Nil(t, got)
	d.pollR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestJoinQuizActiveStatus — covers joining a quiz that is already active
// ---------------------------------------------------------------------------

func TestJoinQuizActiveStatus(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d := newTestQuizService()

	d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
		ID:       quizID,
		TenantID: tenantID,
		Status:   QuizStatusActive,
	}, nil)
	d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(nil, nil)
	d.participantR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(nil)
	d.participantR.On("CountByQuiz", mock.Anything, quizID).Return(3, nil)
	d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)

	got, err := d.svc.JoinQuiz(context.Background(), quizID, tenantID, gcid, "LateJoiner")

	assert.NoError(t, err)
	if assert.NotNil(t, got) {
		assert.Equal(t, gcid, got.GCID)
		assert.Equal(t, "LateJoiner", got.DisplayName)
	}

	d.quizR.AssertExpectations(t)
	d.participantR.AssertExpectations(t)
}
