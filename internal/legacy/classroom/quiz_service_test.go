package classroom

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// testQuizDeps holds all mocks wired into a QuizService.
type testQuizDeps struct {
	svc          *QuizService
	quizR        *mockQuizRepo
	participantR *mockParticipantRepo
	publisher    *mockEventPublisher
}

// newTestQuizService creates a QuizService with fresh mocks.
func newTestQuizService() testQuizDeps {
	qr := &mockQuizRepo{}
	pr := &mockParticipantRepo{}
	ep := &mockEventPublisher{}
	return testQuizDeps{
		svc:          NewQuizService(qr, pr, ep),
		quizR:        qr,
		participantR: pr,
		publisher:    ep,
	}
}

// ---------------------------------------------------------------------------
// TestCreateQuiz
// ---------------------------------------------------------------------------

func TestCreateQuiz(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	validQuestions := []QuizQuestion{
		{
			QuestionText:       "What is Go?",
			Options:            []string{"A language", "A game", "A framework", "A database"},
			CorrectOptionIndex: 0,
		},
		{
			QuestionText:       "Who created Go?",
			Options:            []string{"Google", "Microsoft"},
			CorrectOptionIndex: 0,
		},
	}

	tests := []struct {
		name         string
		input        *LiveQuizSession
		setupMocks   func(d testQuizDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LiveQuizSession)
	}{
		{
			name: "success: creates quiz with UUIDv7 and waiting status",
			input: &LiveQuizSession{
				TenantID:      tenantID,
				Title:         "Go Quiz",
				Questions:     validQuestions,
				CreatedByGCID: gcid,
			},
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
			},
			assertResult: func(t *testing.T, got *LiveQuizSession) {
				assert.NotEqual(t, uuid.Nil, got.ID, "should assign UUIDv7")
				assert.Equal(t, QuizStatusWaiting, got.Status, "should default to waiting")
				assert.Equal(t, -1, got.CurrentQuestionIndex, "should start at -1")
				assert.Equal(t, 0, got.ParticipantCount)
				assert.Equal(t, DefaultTimeLimitSeconds, got.TimeLimitSeconds)
				assert.Equal(t, "Go Quiz", got.Title)
				assert.Len(t, got.Questions, 2)
			},
		},
		{
			name: "success: custom time limit preserved",
			input: &LiveQuizSession{
				TenantID:         tenantID,
				Title:            "Timed Quiz",
				Questions:        validQuestions,
				TimeLimitSeconds: 60,
				CreatedByGCID:    gcid,
			},
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
			},
			assertResult: func(t *testing.T, got *LiveQuizSession) {
				assert.Equal(t, 60, got.TimeLimitSeconds)
			},
		},
		{
			name: "fails: empty title returns ErrValidationFailed",
			input: &LiveQuizSession{
				TenantID:      tenantID,
				Title:         "",
				Questions:     validQuestions,
				CreatedByGCID: gcid,
			},
			setupMocks: func(d testQuizDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: no questions returns ErrValidationFailed",
			input: &LiveQuizSession{
				TenantID:      tenantID,
				Title:         "Empty Quiz",
				Questions:     []QuizQuestion{},
				CreatedByGCID: gcid,
			},
			setupMocks: func(d testQuizDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: question with less than 2 options",
			input: &LiveQuizSession{
				TenantID: tenantID,
				Title:    "Bad Quiz",
				Questions: []QuizQuestion{
					{QuestionText: "Q?", Options: []string{"A"}, CorrectOptionIndex: 0},
				},
				CreatedByGCID: gcid,
			},
			setupMocks: func(d testQuizDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: correct_option_index out of range",
			input: &LiveQuizSession{
				TenantID: tenantID,
				Title:    "Bad Index Quiz",
				Questions: []QuizQuestion{
					{QuestionText: "Q?", Options: []string{"A", "B"}, CorrectOptionIndex: 5},
				},
				CreatedByGCID: gcid,
			},
			setupMocks: func(d testQuizDeps) {},
			wantErr:    ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestQuizService()
			tc.setupMocks(d)

			got, err := d.svc.CreateQuiz(context.Background(), tc.input)

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

			d.quizR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestStartQuiz
// ---------------------------------------------------------------------------

func TestStartQuiz(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testQuizDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LiveQuizSession)
	}{
		{
			name: "success: waiting to active with question index 0",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:                   quizID,
					TenantID:             tenantID,
					Title:                "Test Quiz",
					Status:               QuizStatusWaiting,
					Questions:            []QuizQuestion{{QuestionText: "Q1", Options: []string{"A", "B"}, CorrectOptionIndex: 0}},
					CurrentQuestionIndex: -1,
					CreatedByGCID:        gcid,
				}, nil)
				d.participantR.On("CountByQuiz", mock.Anything, quizID).Return(5, nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *LiveQuizSession) {
				assert.Equal(t, QuizStatusActive, got.Status)
				assert.Equal(t, 0, got.CurrentQuestionIndex)
				assert.Equal(t, 5, got.ParticipantCount)
			},
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
		{
			name: "fails: quiz already started",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusActive,
				}, nil)
			},
			wantErr: ErrQuizAlreadyStarted,
		},
		{
			name: "fails: quiz already ended",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusEnded,
				}, nil)
			},
			wantErr: ErrQuizAlreadyEnded,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestQuizService()
			tc.setupMocks(d)

			got, err := d.svc.StartQuiz(context.Background(), quizID, tenantID)

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

			d.quizR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAdvanceQuestion
// ---------------------------------------------------------------------------

func TestAdvanceQuestion(t *testing.T) {
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
			name: "success: advances from question 0 to 1",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusActive,
					Questions: []QuizQuestion{
						{QuestionText: "Q1", Options: []string{"A", "B"}, CorrectOptionIndex: 0},
						{QuestionText: "Q2", Options: []string{"C", "D"}, CorrectOptionIndex: 1},
						{QuestionText: "Q3", Options: []string{"E", "F"}, CorrectOptionIndex: 0},
					},
					CurrentQuestionIndex: 0,
				}, nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
			},
			assertResult: func(t *testing.T, got *LiveQuizSession) {
				assert.Equal(t, 1, got.CurrentQuestionIndex)
			},
		},
		{
			name: "fails: no more questions",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusActive,
					Questions: []QuizQuestion{
						{QuestionText: "Q1", Options: []string{"A", "B"}, CorrectOptionIndex: 0},
					},
					CurrentQuestionIndex: 0,
				}, nil)
			},
			wantErr: ErrNoMoreQuestions,
		},
		{
			name: "fails: quiz not active",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusWaiting,
				}, nil)
			},
			wantErr: ErrQuizNotActive,
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestQuizService()
			tc.setupMocks(d)

			got, err := d.svc.AdvanceQuestion(context.Background(), quizID, tenantID)

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

			d.quizR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestEndQuiz
// ---------------------------------------------------------------------------

func TestEndQuiz(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testQuizDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LiveQuizSession)
	}{
		{
			name: "success: active to ended",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:                   quizID,
					TenantID:             tenantID,
					Title:                "Quiz",
					Status:               QuizStatusActive,
					CurrentQuestionIndex: 2,
					CreatedByGCID:        gcid,
				}, nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *LiveQuizSession) {
				assert.Equal(t, QuizStatusEnded, got.Status)
			},
		},
		{
			name: "success: paused to ended",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:                   quizID,
					TenantID:             tenantID,
					Title:                "Paused Quiz",
					Status:               QuizStatusPaused,
					CurrentQuestionIndex: 1,
					CreatedByGCID:        gcid,
				}, nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *LiveQuizSession) {
				assert.Equal(t, QuizStatusEnded, got.Status)
			},
		},
		{
			name: "fails: quiz already ended",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusEnded,
				}, nil)
			},
			wantErr: ErrQuizAlreadyEnded,
		},
		{
			name: "fails: cannot end waiting quiz",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusWaiting,
				}, nil)
			},
			wantErr: ErrInvalidStateTransition,
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestQuizService()
			tc.setupMocks(d)

			got, err := d.svc.EndQuiz(context.Background(), quizID, tenantID)

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

			d.quizR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestJoinQuiz
// ---------------------------------------------------------------------------

func TestJoinQuiz(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testQuizDeps)
		wantErr      error
		assertResult func(t *testing.T, got *QuizParticipant)
	}{
		{
			name: "success: new participant joins",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusWaiting,
				}, nil)
				d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(nil, nil)
				d.participantR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(nil)
				d.participantR.On("CountByQuiz", mock.Anything, quizID).Return(1, nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
			},
			assertResult: func(t *testing.T, got *QuizParticipant) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, quizID, got.QuizSessionID)
				assert.Equal(t, gcid, got.GCID)
			},
		},
		{
			name: "success: already joined returns existing participant",
			setupMocks: func(d testQuizDeps) {
				existing := &QuizParticipant{
					ID:            uuid.Must(uuid.NewV7()),
					QuizSessionID: quizID,
					GCID:          gcid,
					DisplayName:   "Player1",
				}
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusWaiting,
				}, nil)
				d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(existing, nil)
			},
			assertResult: func(t *testing.T, got *QuizParticipant) {
				assert.Equal(t, "Player1", got.DisplayName)
			},
		},
		{
			name: "fails: quiz already ended",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusEnded,
				}, nil)
			},
			wantErr: ErrQuizAlreadyEnded,
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestQuizService()
			tc.setupMocks(d)

			got, err := d.svc.JoinQuiz(context.Background(), quizID, tenantID, gcid, "Player1")

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

			d.quizR.AssertExpectations(t)
			d.participantR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestSubmitAnswer
// ---------------------------------------------------------------------------

func TestSubmitAnswer(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	participantID := uuid.Must(uuid.NewV7())

	// SubmitAnswer writes the participant's running stats (TotalPoints,
	// AnsweredCount, CorrectCount, LastAnswerCorrect, AvgTimeMs, UpdatedAt)
	// THROUGH the pointer the repo hands back, so a fixture shared across
	// t.Parallel() sub-tests is written by one while another reads it (directly,
	// or inside testify's Called argument capture). setupMocks runs once per
	// sub-test, in that sub-test's goroutine, so building here gives each
	// sub-test its own instances.
	newActiveQuiz := func() *LiveQuizSession {
		return &LiveQuizSession{
			ID:       quizID,
			TenantID: tenantID,
			Status:   QuizStatusActive,
			Questions: []QuizQuestion{
				{QuestionText: "Q1?", Options: []string{"A", "B", "C", "D"}, CorrectOptionIndex: 0},
			},
			CurrentQuestionIndex: 0,
			TimeLimitSeconds:     30,
		}
	}

	newParticipant := func() *QuizParticipant {
		return &QuizParticipant{
			ID:            participantID,
			QuizSessionID: quizID,
			GCID:          gcid,
			TotalPoints:   0,
			CorrectCount:  0,
			AnsweredCount: 0,
		}
	}

	tests := []struct {
		name                string
		selectedOptionIndex int
		timeTakenMs         int
		setupMocks          func(d testQuizDeps)
		wantErr             error
		assertResult        func(t *testing.T, got *AnswerResult)
	}{
		{
			name:                "success: correct answer earns points",
			selectedOptionIndex: 0,
			timeTakenMs:         5000,
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(), nil)
				d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(newParticipant(), nil)
				d.participantR.On("HasAnswered", mock.Anything, participantID, 0).Return(false, nil)
				d.participantR.On("RecordAnswer", mock.Anything, mock.AnythingOfType("*classroom.QuizAnswer")).Return(nil)
				d.participantR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *AnswerResult) {
				assert.True(t, got.Correct)
				assert.Greater(t, got.PointsEarned, 0, "correct answer should earn points")
				assert.Equal(t, 5000, got.TimeTakenMs)
			},
		},
		{
			name:                "success: incorrect answer earns 0 points",
			selectedOptionIndex: 2,
			timeTakenMs:         10000,
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(), nil)
				d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(newParticipant(), nil)
				d.participantR.On("HasAnswered", mock.Anything, participantID, 0).Return(false, nil)
				d.participantR.On("RecordAnswer", mock.Anything, mock.AnythingOfType("*classroom.QuizAnswer")).Return(nil)
				d.participantR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *AnswerResult) {
				assert.False(t, got.Correct)
				assert.Equal(t, 0, got.PointsEarned)
			},
		},
		{
			name:                "fails: answer too late",
			selectedOptionIndex: 0,
			timeTakenMs:         31000, // exceeds 30s limit
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(), nil)
			},
			wantErr: ErrAnswerTooLate,
		},
		{
			name:                "fails: already answered",
			selectedOptionIndex: 0,
			timeTakenMs:         5000,
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(), nil)
				d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(newParticipant(), nil)
				d.participantR.On("HasAnswered", mock.Anything, participantID, 0).Return(true, nil)
			},
			wantErr: ErrAlreadyAnswered,
		},
		{
			name:                "fails: quiz not active",
			selectedOptionIndex: 0,
			timeTakenMs:         5000,
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusWaiting,
				}, nil)
			},
			wantErr: ErrQuizNotActive,
		},
		{
			name:                "fails: participant not found",
			selectedOptionIndex: 0,
			timeTakenMs:         5000,
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(), nil)
				d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(nil, nil)
			},
			wantErr: ErrParticipantNotFound,
		},
		{
			name:                "fails: invalid option index",
			selectedOptionIndex: 10,
			timeTakenMs:         5000,
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(), nil)
				d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(newParticipant(), nil)
				d.participantR.On("HasAnswered", mock.Anything, participantID, 0).Return(false, nil)
			},
			wantErr: ErrInvalidOption,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestQuizService()
			tc.setupMocks(d)

			got, err := d.svc.SubmitAnswer(context.Background(), quizID, tenantID, gcid, tc.selectedOptionIndex, tc.timeTakenMs)

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

			d.quizR.AssertExpectations(t)
			d.participantR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetLeaderboard
// ---------------------------------------------------------------------------

func TestGetLeaderboard(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testQuizDeps)
		wantErr      error
		assertResult func(t *testing.T, got []LeaderboardEntry)
	}{
		{
			name: "success: returns ranked participants",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusActive,
				}, nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{
					{GCID: uuid.Must(uuid.NewV7()), DisplayName: "Alice", TotalPoints: 300, CorrectCount: 3, AvgTimeMs: 2000},
					{GCID: uuid.Must(uuid.NewV7()), DisplayName: "Bob", TotalPoints: 200, CorrectCount: 2, AvgTimeMs: 3000},
					{GCID: uuid.Must(uuid.NewV7()), DisplayName: "Charlie", TotalPoints: 150, CorrectCount: 1, AvgTimeMs: 5000},
				}, nil)
			},
			assertResult: func(t *testing.T, got []LeaderboardEntry) {
				assert.Len(t, got, 3)
				assert.Equal(t, 1, got[0].Rank)
				assert.Equal(t, "Alice", got[0].DisplayName)
				assert.Equal(t, 300, got[0].TotalPoints)
				assert.Equal(t, 2, got[1].Rank)
				assert.Equal(t, 3, got[2].Rank)
			},
		},
		{
			name: "success: empty leaderboard",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID:       quizID,
					TenantID: tenantID,
					Status:   QuizStatusWaiting,
				}, nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{}, nil)
			},
			assertResult: func(t *testing.T, got []LeaderboardEntry) {
				assert.Empty(t, got)
			},
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestQuizService()
			tc.setupMocks(d)

			got, err := d.svc.GetLeaderboard(context.Background(), quizID, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.quizR.AssertExpectations(t)
			d.participantR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// Error-path top-ups for partially covered quiz functions
// ---------------------------------------------------------------------------

func TestStartQuiz_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("count participants error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
			ID: quizID, TenantID: tenantID, Status: QuizStatusWaiting, CreatedByGCID: gcid,
		}, nil)
		d.participantR.On("CountByQuiz", mock.Anything, quizID).Return(0, errTestClassroom)
		_, err := d.svc.StartQuiz(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
			ID: quizID, TenantID: tenantID, Status: QuizStatusWaiting, CreatedByGCID: gcid,
		}, nil)
		d.participantR.On("CountByQuiz", mock.Anything, quizID).Return(0, nil)
		d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(errTestClassroom)
		_, err := d.svc.StartQuiz(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
			ID: quizID, TenantID: tenantID, Title: "Q", Status: QuizStatusWaiting, CreatedByGCID: gcid,
		}, nil)
		d.participantR.On("CountByQuiz", mock.Anything, quizID).Return(1, nil)
		d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
		_, err := d.svc.StartQuiz(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}

func TestAdvanceQuestion_UpdateError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	d := newTestQuizService()
	d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
		ID: quizID, Status: QuizStatusActive,
		Questions: []QuizQuestion{
			{QuestionText: "Q1", Options: []string{"A", "B"}, CorrectOptionIndex: 0},
			{QuestionText: "Q2", Options: []string{"C", "D"}, CorrectOptionIndex: 1},
		},
		CurrentQuestionIndex: 0,
	}, nil)
	d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(errTestClassroom)

	_, err := d.svc.AdvanceQuestion(context.Background(), quizID, tenantID)
	assert.ErrorIs(t, err, errTestClassroom)
	d.quizR.AssertExpectations(t)
}

func TestEndQuiz_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	t.Run("update repo error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
			ID: quizID, Status: QuizStatusActive,
		}, nil)
		d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(errTestClassroom)
		_, err := d.svc.EndQuiz(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
			ID: quizID, TenantID: tenantID, Title: "Q", Status: QuizStatusActive,
			CurrentQuestionIndex: 1, CreatedByGCID: uuid.Must(uuid.NewV7()),
		}, nil)
		d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
		_, err := d.svc.EndQuiz(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}

func TestJoinQuiz_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	t.Run("existing participant lookup error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
			ID: quizID, TenantID: tenantID, Status: QuizStatusWaiting,
		}, nil)
		d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(nil, errTestClassroom)
		_, err := d.svc.JoinQuiz(context.Background(), quizID, tenantID, gcid, "P")
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
	})

	t.Run("participant create error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
			ID: quizID, TenantID: tenantID, Status: QuizStatusWaiting,
		}, nil)
		d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(nil, nil)
		d.participantR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(errTestClassroom)
		_, err := d.svc.JoinQuiz(context.Background(), quizID, tenantID, gcid, "P")
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
	})

	t.Run("count participants error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
			ID: quizID, TenantID: tenantID, Status: QuizStatusWaiting,
		}, nil)
		d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(nil, nil)
		d.participantR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(nil)
		d.participantR.On("CountByQuiz", mock.Anything, quizID).Return(0, errTestClassroom)
		_, err := d.svc.JoinQuiz(context.Background(), quizID, tenantID, gcid, "P")
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
	})

	t.Run("quiz update error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
			ID: quizID, TenantID: tenantID, Status: QuizStatusWaiting,
		}, nil)
		d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(nil, nil)
		d.participantR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(nil)
		d.participantR.On("CountByQuiz", mock.Anything, quizID).Return(1, nil)
		d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(errTestClassroom)
		_, err := d.svc.JoinQuiz(context.Background(), quizID, tenantID, gcid, "P")
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
	})
}

func TestSubmitAnswer_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	participantID := uuid.Must(uuid.NewV7())

	activeQuiz := func() *LiveQuizSession {
		return &LiveQuizSession{
			ID: quizID, TenantID: tenantID, Status: QuizStatusActive,
			Questions:            []QuizQuestion{{QuestionText: "Q1?", Options: []string{"A", "B"}, CorrectOptionIndex: 0}},
			CurrentQuestionIndex: 0,
			TimeLimitSeconds:     30,
		}
	}
	// Fresh per sub-test for the same reason as TestSubmitAnswer above. These
	// sub-tests are sequential today, so this is not a race, but SubmitAnswer
	// mutates the participant it is handed, so a single shared instance would
	// otherwise accumulate stats across sub-tests and become a race the moment
	// anyone adds t.Parallel().
	newParticipant := func() *QuizParticipant {
		return &QuizParticipant{ID: participantID, QuizSessionID: quizID, GCID: gcid}
	}

	t.Run("quiz not found", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
		_, err := d.svc.SubmitAnswer(context.Background(), quizID, tenantID, gcid, 0, 1000)
		assert.ErrorIs(t, err, ErrQuizNotFound)
		d.quizR.AssertExpectations(t)
	})

	t.Run("find participant error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(activeQuiz(), nil)
		d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(nil, errTestClassroom)
		_, err := d.svc.SubmitAnswer(context.Background(), quizID, tenantID, gcid, 0, 1000)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
	})

	t.Run("has answered check error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(activeQuiz(), nil)
		d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(newParticipant(), nil)
		d.participantR.On("HasAnswered", mock.Anything, participantID, 0).Return(false, errTestClassroom)
		_, err := d.svc.SubmitAnswer(context.Background(), quizID, tenantID, gcid, 0, 1000)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
	})

	t.Run("record answer error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(activeQuiz(), nil)
		d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(newParticipant(), nil)
		d.participantR.On("HasAnswered", mock.Anything, participantID, 0).Return(false, nil)
		d.participantR.On("RecordAnswer", mock.Anything, mock.AnythingOfType("*classroom.QuizAnswer")).Return(errTestClassroom)
		_, err := d.svc.SubmitAnswer(context.Background(), quizID, tenantID, gcid, 0, 1000)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
	})

	t.Run("update participant error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(activeQuiz(), nil)
		d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(newParticipant(), nil)
		d.participantR.On("HasAnswered", mock.Anything, participantID, 0).Return(false, nil)
		d.participantR.On("RecordAnswer", mock.Anything, mock.AnythingOfType("*classroom.QuizAnswer")).Return(nil)
		d.participantR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(errTestClassroom)
		_, err := d.svc.SubmitAnswer(context.Background(), quizID, tenantID, gcid, 0, 1000)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
	})

	t.Run("publish error", func(t *testing.T) {
		d := newTestQuizService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(activeQuiz(), nil)
		d.participantR.On("GetByQuizAndGCID", mock.Anything, quizID, gcid).Return(newParticipant(), nil)
		d.participantR.On("HasAnswered", mock.Anything, participantID, 0).Return(false, nil)
		d.participantR.On("RecordAnswer", mock.Anything, mock.AnythingOfType("*classroom.QuizAnswer")).Return(nil)
		d.participantR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
		_, err := d.svc.SubmitAnswer(context.Background(), quizID, tenantID, gcid, 0, 1000)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
		d.participantR.AssertExpectations(t)
		d.publisher.AssertExpectations(t)
	})
}

func TestGetLeaderboard_ListError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	d := newTestQuizService()
	d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
		ID: quizID, Status: QuizStatusActive,
	}, nil)
	d.participantR.On("ListByQuiz", mock.Anything, quizID).Return(nil, errTestClassroom)

	_, err := d.svc.GetLeaderboard(context.Background(), quizID, tenantID)
	assert.ErrorIs(t, err, errTestClassroom)
	d.quizR.AssertExpectations(t)
	d.participantR.AssertExpectations(t)
}
