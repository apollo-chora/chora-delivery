package classroom

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// errTestClassroom is a shared sentinel error used to assert wrapped repo errors.
var errTestClassroom = errors.New("test classroom error")

// timeNow returns the current UTC time.
func timeNow() time.Time { return time.Now().UTC() }

// testQuizFullDeps holds all mocks wired into a QuizService created via
// NewQuizServiceFull.
type testQuizFullDeps struct {
	svc           *QuizService
	quizR         *mockQuizRepo
	participantR  *mockParticipantRepo
	teamR         *mockTeamRepo
	teamMemberR   *mockTeamMemberRepo
	embeddedPollR *mockEmbeddedPollRepo
	analyticsR    *mockAnalyticsRepo
	publisher     *mockEventPublisher
}

// newTestQuizFullService creates a QuizService with all repositories via
// NewQuizServiceFull.
func newTestQuizFullService() testQuizFullDeps {
	qr := &mockQuizRepo{}
	pr := &mockParticipantRepo{}
	tr := &mockTeamRepo{}
	tmr := &mockTeamMemberRepo{}
	epr := &mockEmbeddedPollRepo{}
	ar := &mockAnalyticsRepo{}
	ep := &mockEventPublisher{}
	return testQuizFullDeps{
		svc:           NewQuizServiceFull(qr, pr, tr, tmr, epr, ar, ep),
		quizR:         qr,
		participantR:  pr,
		teamR:         tr,
		teamMemberR:   tmr,
		embeddedPollR: epr,
		analyticsR:    ar,
		publisher:     ep,
	}
}

func newActiveQuiz(id, tenantID uuid.UUID, gcid uuid.UUID) *LiveQuizSession {
	return &LiveQuizSession{
		ID:                   id,
		TenantID:             tenantID,
		Title:                "Quiz",
		Status:               QuizStatusActive,
		Questions:            []QuizQuestion{{QuestionText: "Q1", Options: []string{"A", "B"}, CorrectOptionIndex: 0}},
		CurrentQuestionIndex: 0,
		CreatedByGCID:        gcid,
	}
}

// ---------------------------------------------------------------------------
// TestPauseQuiz
// ---------------------------------------------------------------------------

func TestPauseQuiz(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testQuizFullDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LiveQuizSession)
	}{
		{
			name: "success: active to paused publishes event",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, gcid), nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *LiveQuizSession) {
				assert.Equal(t, QuizStatusPaused, got.Status)
				assert.NotNil(t, got.PausedAt)
			},
		},
		{
			name: "fails: quiz not active",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, Status: QuizStatusWaiting}, nil)
			},
			wantErr: ErrQuizNotActive,
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
		{
			name: "fails: update repo error",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, gcid), nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
		{
			name: "fails: publish error",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, gcid), nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestQuizFullService()
			tc.setupMocks(d)

			got, err := d.svc.PauseQuiz(context.Background(), quizID, tenantID)

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
// TestResumeQuiz
// ---------------------------------------------------------------------------

func TestResumeQuiz(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testQuizFullDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LiveQuizSession)
	}{
		{
			name: "success: paused to active publishes event",
			setupMocks: func(d testQuizFullDeps) {
				now := timeNow()
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID: quizID, TenantID: tenantID, Status: QuizStatusPaused, PausedAt: &now, CreatedByGCID: gcid,
				}, nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *LiveQuizSession) {
				assert.Equal(t, QuizStatusActive, got.Status)
				assert.Nil(t, got.PausedAt)
			},
		},
		{
			name: "fails: quiz not paused",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, Status: QuizStatusActive}, nil)
			},
			wantErr: ErrQuizNotPaused,
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
		{
			name: "fails: update repo error",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, Status: QuizStatusPaused}, nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
		{
			name: "fails: publish error",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID, Status: QuizStatusPaused, CreatedByGCID: gcid}, nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestQuizFullService()
			tc.setupMocks(d)

			got, err := d.svc.ResumeQuiz(context.Background(), quizID, tenantID)

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
// TestSkipQuestion
// ---------------------------------------------------------------------------

func TestSkipQuestion(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	t.Run("success: delegates to AdvanceQuestion", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
			ID: quizID, TenantID: tenantID, Status: QuizStatusActive,
			Questions: []QuizQuestion{
				{QuestionText: "Q1", Options: []string{"A", "B"}, CorrectOptionIndex: 0},
				{QuestionText: "Q2", Options: []string{"C", "D"}, CorrectOptionIndex: 1},
			},
			CurrentQuestionIndex: 0,
		}, nil)
		d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)

		got, err := d.svc.SkipQuestion(context.Background(), quizID, tenantID)
		assert.NoError(t, err)
		assert.Equal(t, 1, got.CurrentQuestionIndex)
		d.quizR.AssertExpectations(t)
	})

	t.Run("fails: quiz not found", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
		_, err := d.svc.SkipQuestion(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, ErrQuizNotFound)
		d.quizR.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// TestReplayQuestion
// ---------------------------------------------------------------------------

func TestReplayQuestion(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testQuizFullDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LiveQuizSession)
	}{
		{
			name: "success: active quiz stays on same question",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, uuid.Must(uuid.NewV7())), nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
			},
			assertResult: func(t *testing.T, got *LiveQuizSession) {
				assert.Equal(t, QuizStatusActive, got.Status)
				assert.Equal(t, 0, got.CurrentQuestionIndex)
			},
		},
		{
			name: "success: paused quiz resumes on same question",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{
					ID: quizID, Status: QuizStatusPaused, CurrentQuestionIndex: 3,
				}, nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(nil)
			},
			assertResult: func(t *testing.T, got *LiveQuizSession) {
				assert.Equal(t, QuizStatusActive, got.Status)
				assert.Nil(t, got.PausedAt)
				assert.Equal(t, 3, got.CurrentQuestionIndex)
			},
		},
		{
			name: "fails: quiz not active or paused",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, Status: QuizStatusWaiting}, nil)
			},
			wantErr: ErrQuizNotActive,
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
		{
			name: "fails: update repo error",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, uuid.Must(uuid.NewV7())), nil)
				d.quizR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.LiveQuizSession")).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestQuizFullService()
			tc.setupMocks(d)

			got, err := d.svc.ReplayQuestion(context.Background(), quizID, tenantID)

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
// TestCreateTeam
// ---------------------------------------------------------------------------

func TestCreateTeam(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		teamName     string
		setupMocks   func(d testQuizFullDeps)
		wantErr      error
		assertResult func(t *testing.T, got *QuizTeam)
	}{
		{
			name:     "success: creates team",
			teamName: "Alpha",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
				d.teamR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.QuizTeam")).Return(nil)
			},
			assertResult: func(t *testing.T, got *QuizTeam) {
				assert.Equal(t, quizID, got.QuizID)
				assert.Equal(t, "Alpha", got.TeamName)
				assert.NotEqual(t, uuid.Nil, got.ID)
			},
		},
		{
			name:     "fails: empty team name",
			teamName: "",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:     "fails: quiz not found",
			teamName: "Alpha",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
		{
			name:     "fails: team create error",
			teamName: "Alpha",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
				d.teamR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.QuizTeam")).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestQuizFullService()
			tc.setupMocks(d)

			got, err := d.svc.CreateTeam(context.Background(), quizID, tenantID, tc.teamName, nil)

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
			d.teamR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAddTeamMember
// ---------------------------------------------------------------------------

func TestAddTeamMember(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	teamID := uuid.Must(uuid.NewV7())
	participantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testQuizFullDeps)
		wantErr      error
		assertResult func(t *testing.T, got *QuizTeamMember)
	}{
		{
			name: "success: adds member to team",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
				d.teamR.On("GetByID", mock.Anything, teamID).Return(&QuizTeam{ID: teamID, QuizID: quizID}, nil)
				d.teamMemberR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.QuizTeamMember")).Return(nil)
			},
			assertResult: func(t *testing.T, got *QuizTeamMember) {
				assert.Equal(t, teamID, got.TeamID)
				assert.Equal(t, participantID, got.ParticipantID)
			},
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
		{
			name: "fails: team not found",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
				d.teamR.On("GetByID", mock.Anything, teamID).Return(nil, nil)
			},
			wantErr: ErrTeamNotFound,
		},
		{
			name: "fails: team lookup error",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
				d.teamR.On("GetByID", mock.Anything, teamID).Return(nil, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
		{
			name: "fails: member create error",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
				d.teamR.On("GetByID", mock.Anything, teamID).Return(&QuizTeam{ID: teamID}, nil)
				d.teamMemberR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.QuizTeamMember")).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestQuizFullService()
			tc.setupMocks(d)

			got, err := d.svc.AddTeamMember(context.Background(), quizID, tenantID, teamID, participantID)

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
			d.teamR.AssertExpectations(t)
			d.teamMemberR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetTeamLeaderboard
// ---------------------------------------------------------------------------

func TestGetTeamLeaderboard(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	team1ID := uuid.Must(uuid.NewV7())
	team2ID := uuid.Must(uuid.NewV7())
	participantA := uuid.Must(uuid.NewV7())
	participantB := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testQuizFullDeps)
		wantErr      error
		assertResult func(t *testing.T, got []TeamLeaderboardEntry)
	}{
		{
			name: "success: aggregates and ranks teams",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
				d.teamR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizTeam{
					{ID: team1ID, QuizID: quizID, TeamName: "Team 1"},
					{ID: team2ID, QuizID: quizID, TeamName: "Team 2"},
				}, nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{
					{ID: participantA, TotalPoints: 300},
					{ID: participantB, TotalPoints: 100},
				}, nil)
				d.teamMemberR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizTeamMember{
					{TeamID: team1ID, ParticipantID: participantA},
					{TeamID: team1ID, ParticipantID: participantB},
					{TeamID: team2ID, ParticipantID: participantB},
				}, nil)
			},
			assertResult: func(t *testing.T, got []TeamLeaderboardEntry) {
				assert.Len(t, got, 2)
				assert.Equal(t, team1ID, got[0].TeamID)
				assert.Equal(t, 400, got[0].TotalPoints)
				assert.Equal(t, 2, got[0].MemberCount)
				assert.Equal(t, 1, got[0].Rank)
				assert.Equal(t, team2ID, got[1].TeamID)
				assert.Equal(t, 100, got[1].TotalPoints)
				assert.Equal(t, 2, got[1].Rank)
			},
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
		{
			name: "fails: list teams error",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID}, nil)
				d.teamR.On("ListByQuiz", mock.Anything, quizID).Return(nil, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
		{
			name: "fails: list participants error",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID}, nil)
				d.teamR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizTeam{}, nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return(nil, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
		{
			name: "fails: list team members error",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID}, nil)
				d.teamR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizTeam{}, nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{}, nil)
				d.teamMemberR.On("ListByQuiz", mock.Anything, quizID).Return(nil, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestQuizFullService()
			tc.setupMocks(d)

			got, err := d.svc.GetTeamLeaderboard(context.Background(), quizID, tenantID)

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
			d.teamR.AssertExpectations(t)
			d.participantR.AssertExpectations(t)
			d.teamMemberR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestEliminateParticipants
// ---------------------------------------------------------------------------

func TestEliminateParticipants(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	eliminatedBefore := timeNow()
	p1 := uuid.Must(uuid.NewV7())
	p2 := uuid.Must(uuid.NewV7())
	p3 := uuid.Must(uuid.NewV7())
	p4 := uuid.Must(uuid.NewV7())

	tests := []struct {
		name               string
		eliminationPercent int
		setupMocks         func(d testQuizFullDeps)
		wantErr            error
		assertResult       func(t *testing.T, got *EliminationResult)
	}{
		{
			name:               "success: eliminates bottom percentage",
			eliminationPercent: 25,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, gcid), nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{
					{ID: p1, TotalPoints: 400},
					{ID: p2, TotalPoints: 300},
					{ID: p3, TotalPoints: 200},
					{ID: p4, TotalPoints: 100},
				}, nil)
				d.participantR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *EliminationResult) {
				assert.Equal(t, 1, got.EliminatedCount)
				assert.Equal(t, 3, got.RemainingCount)
				assert.Equal(t, p4, got.EliminatedParticipants[0])
			},
		},
		{
			name:               "success: skips already eliminated participants",
			eliminationPercent: 50,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, gcid), nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{
					{ID: p1, TotalPoints: 400},
					{ID: p2, TotalPoints: 300, EliminatedAt: &eliminatedBefore},
					{ID: p3, TotalPoints: 200},
					{ID: p4, TotalPoints: 100},
				}, nil)
				d.participantR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(nil).Once()
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *EliminationResult) {
				// active = 3 (p2 skipped); 3*50/100 = 1 eliminated.
				assert.Equal(t, 1, got.EliminatedCount)
				assert.Equal(t, 2, got.RemainingCount)
			},
		},
		{
			name:               "success: keeps at least one participant",
			eliminationPercent: 50,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, gcid), nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{
					{ID: p1, TotalPoints: 100},
				}, nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *EliminationResult) {
				assert.Equal(t, 0, got.EliminatedCount)
				assert.Equal(t, 1, got.RemainingCount)
			},
		},
		{
			name:               "fails: quiz not active or paused",
			eliminationPercent: 25,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, Status: QuizStatusWaiting}, nil)
			},
			wantErr: ErrQuizNotActive,
		},
		{
			name:               "fails: percent below 1",
			eliminationPercent: 0,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, gcid), nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:               "fails: percent above 50",
			eliminationPercent: 51,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, gcid), nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:               "fails: quiz not found",
			eliminationPercent: 25,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
		{
			name:               "fails: list participants error",
			eliminationPercent: 25,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, gcid), nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return(nil, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
		{
			name:               "fails: eliminate update error",
			eliminationPercent: 50,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, gcid), nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{
					{ID: p1, TotalPoints: 400},
					{ID: p2, TotalPoints: 100},
				}, nil)
				d.participantR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
		{
			name:               "fails: publish error",
			eliminationPercent: 50,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(newActiveQuiz(quizID, tenantID, gcid), nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{
					{ID: p1, TotalPoints: 400},
					{ID: p2, TotalPoints: 100},
				}, nil)
				d.participantR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.QuizParticipant")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestQuizFullService()
			tc.setupMocks(d)

			got, err := d.svc.EliminateParticipants(context.Background(), quizID, tenantID, tc.eliminationPercent)

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
// TestCreateEmbeddedPoll
// ---------------------------------------------------------------------------

func TestCreateEmbeddedPoll(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())

	quizWithQuestions := &LiveQuizSession{
		ID: quizID, TenantID: tenantID,
		Questions: []QuizQuestion{
			{QuestionText: "Q1", Options: []string{"A", "B"}, CorrectOptionIndex: 0},
			{QuestionText: "Q2", Options: []string{"C", "D"}, CorrectOptionIndex: 1},
		},
	}

	tests := []struct {
		name         string
		triggerAfter int
		setupMocks   func(d testQuizFullDeps)
		wantErr      error
		assertResult func(t *testing.T, got *EmbeddedPoll)
	}{
		{
			name:         "success: creates embedded poll",
			triggerAfter: 1,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(quizWithQuestions, nil)
				d.embeddedPollR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.EmbeddedPoll")).Return(nil)
			},
			assertResult: func(t *testing.T, got *EmbeddedPoll) {
				assert.Equal(t, quizID, got.QuizID)
				assert.Equal(t, pollID, got.PollID)
				assert.Equal(t, 1, got.TriggerAfterQuestion)
			},
		},
		{
			name:         "fails: trigger out of range (negative)",
			triggerAfter: -1,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(quizWithQuestions, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:         "fails: trigger out of range (too high)",
			triggerAfter: 2,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(quizWithQuestions, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:         "fails: quiz not found",
			triggerAfter: 0,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
		{
			name:         "fails: create error",
			triggerAfter: 0,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(quizWithQuestions, nil)
				d.embeddedPollR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.EmbeddedPoll")).Return(errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestQuizFullService()
			tc.setupMocks(d)

			got, err := d.svc.CreateEmbeddedPoll(context.Background(), quizID, tenantID, pollID, tc.triggerAfter)

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
			d.embeddedPollR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListEmbeddedPolls
// ---------------------------------------------------------------------------

func TestListEmbeddedPolls(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testQuizFullDeps)
		wantErr    error
		wantLen    int
	}{
		{
			name: "success: lists embedded polls",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
				d.embeddedPollR.On("ListByQuiz", mock.Anything, quizID).Return([]EmbeddedPoll{
					{ID: uuid.Must(uuid.NewV7()), QuizID: quizID},
				}, nil)
			},
			wantLen: 1,
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestQuizFullService()
			tc.setupMocks(d)

			got, err := d.svc.ListEmbeddedPolls(context.Background(), quizID, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.Len(t, got, tc.wantLen)
			}
			d.quizR.AssertExpectations(t)
			d.embeddedPollR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetQuizAnalytics
// ---------------------------------------------------------------------------

func TestGetQuizAnalytics(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testQuizFullDeps)
		wantErr      error
		assertResult func(t *testing.T, got *QuizAnalytics)
	}{
		{
			name: "success: returns averages",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{
					{TotalPoints: 300, AvgTimeMs: 2000},
					{TotalPoints: 100, AvgTimeMs: 4000},
				}, nil)
			},
			assertResult: func(t *testing.T, got *QuizAnalytics) {
				assert.Equal(t, quizID, got.QuizID)
				assert.Equal(t, 2, got.TotalParticipants)
				assert.Equal(t, 200.0, got.AvgScore)
				assert.Equal(t, 3000, got.AvgResponseTimeMs)
			},
		},
		{
			name: "success: zero participants",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{}, nil)
			},
			assertResult: func(t *testing.T, got *QuizAnalytics) {
				assert.Equal(t, quizID, got.QuizID)
				assert.Equal(t, 0, got.TotalParticipants)
			},
		},
		{
			name: "fails: quiz not found",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
		{
			name: "fails: list participants error",
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID}, nil)
				d.participantR.On("ListByQuiz", mock.Anything, quizID).Return(nil, errTestClassroom)
			},
			wantErr: errTestClassroom,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestQuizFullService()
			tc.setupMocks(d)

			got, err := d.svc.GetQuizAnalytics(context.Background(), quizID, tenantID)

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
// TestGetQuestionAnalytics
// ---------------------------------------------------------------------------

func TestGetQuestionAnalytics(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())

	quiz := &LiveQuizSession{
		ID: quizID, TenantID: tenantID,
		Questions: []QuizQuestion{
			{QuestionText: "Q1", Options: []string{"A", "B", "C"}, CorrectOptionIndex: 0},
		},
	}

	tests := []struct {
		name          string
		questionIndex int
		setupMocks    func(d testQuizFullDeps)
		wantErr       error
		assertResult  func(t *testing.T, got *QuestionAnalytics)
	}{
		{
			name:          "success: returns question metadata",
			questionIndex: 0,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(quiz, nil)
			},
			assertResult: func(t *testing.T, got *QuestionAnalytics) {
				assert.Equal(t, 0, got.QuestionIndex)
				assert.Equal(t, "Q1", got.QuestionText)
				assert.Equal(t, 0, got.TotalAnswers)
				assert.Len(t, got.OptionDistribution, 3)
			},
		},
		{
			name:          "fails: negative index",
			questionIndex: -1,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(quiz, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:          "fails: index out of range",
			questionIndex: 1,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(quiz, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:          "fails: quiz not found",
			questionIndex: 0,
			setupMocks: func(d testQuizFullDeps) {
				d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, nil)
			},
			wantErr: ErrQuizNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestQuizFullService()
			tc.setupMocks(d)

			got, err := d.svc.GetQuestionAnalytics(context.Background(), quizID, tenantID, tc.questionIndex)

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
