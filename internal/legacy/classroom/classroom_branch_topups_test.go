package classroom

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// Remaining branch top-ups: GetByID / Create repo errors
// ---------------------------------------------------------------------------

func TestQuizGetByID_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	teamID := uuid.Must(uuid.NewV7())
	participantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())

	repoError := func(d testQuizFullDeps, setup func(d testQuizFullDeps)) {
		setup(d)
	}

	// PauseQuiz
	t.Run("PauseQuiz: get error", func(t *testing.T) {
		d := newTestQuizFullService()
		repoError(d, func(d testQuizFullDeps) {
			d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errTestClassroom)
		})
		_, err := d.svc.PauseQuiz(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})

	// ResumeQuiz
	t.Run("ResumeQuiz: get error", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.ResumeQuiz(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})

	// ReplayQuestion
	t.Run("ReplayQuestion: get error", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.ReplayQuestion(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})

	// CreateTeam
	t.Run("CreateTeam: get error", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.CreateTeam(context.Background(), quizID, tenantID, "A", nil)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})

	// AddTeamMember
	t.Run("AddTeamMember: get error", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.AddTeamMember(context.Background(), quizID, tenantID, teamID, participantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})

	// GetTeamLeaderboard
	t.Run("GetTeamLeaderboard: get error", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.GetTeamLeaderboard(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})

	// EliminateParticipants
	t.Run("EliminateParticipants: get error", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.EliminateParticipants(context.Background(), quizID, tenantID, 25)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})

	// CreateEmbeddedPoll
	t.Run("CreateEmbeddedPoll: get error", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.CreateEmbeddedPoll(context.Background(), quizID, tenantID, pollID, 0)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})

	// ListEmbeddedPolls
	t.Run("ListEmbeddedPolls: get error", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.ListEmbeddedPolls(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})

	// GetQuizAnalytics
	t.Run("GetQuizAnalytics: get error", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.GetQuizAnalytics(context.Background(), quizID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})

	// GetQuestionAnalytics
	t.Run("GetQuestionAnalytics: get error", func(t *testing.T) {
		d := newTestQuizFullService()
		d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.GetQuestionAnalytics(context.Background(), quizID, tenantID, 0)
		assert.ErrorIs(t, err, errTestClassroom)
		d.quizR.AssertExpectations(t)
	})
}

func TestGetTeamLeaderboard_SwapSort(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	quizID := uuid.Must(uuid.NewV7())
	team1ID := uuid.Must(uuid.NewV7())
	team2ID := uuid.Must(uuid.NewV7())
	p1 := uuid.Must(uuid.NewV7())
	p2 := uuid.Must(uuid.NewV7())

	d := newTestQuizFullService()
	d.quizR.On("GetByID", mock.Anything, quizID, tenantID).Return(&LiveQuizSession{ID: quizID, TenantID: tenantID}, nil)
	d.teamR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizTeam{
		// Listed out of order: lower-scoring team first, so the swap branch runs.
		{ID: team2ID, QuizID: quizID, TeamName: "Low"},
		{ID: team1ID, QuizID: quizID, TeamName: "High"},
	}, nil)
	d.participantR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizParticipant{
		{ID: p1, TotalPoints: 500},
		{ID: p2, TotalPoints: 100},
	}, nil)
	d.teamMemberR.On("ListByQuiz", mock.Anything, quizID).Return([]QuizTeamMember{
		{TeamID: team1ID, ParticipantID: p1},
		{TeamID: team2ID, ParticipantID: p2},
	}, nil)

	got, err := d.svc.GetTeamLeaderboard(context.Background(), quizID, tenantID)
	assert.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, team1ID, got[0].TeamID)
	assert.Equal(t, 500, got[0].TotalPoints)
	assert.Equal(t, 1, got[0].Rank)
	assert.Equal(t, team2ID, got[1].TeamID)
	assert.Equal(t, 2, got[1].Rank)
	d.quizR.AssertExpectations(t)
	d.teamR.AssertExpectations(t)
	d.participantR.AssertExpectations(t)
	d.teamMemberR.AssertExpectations(t)
}

func TestSessionGetByID_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	learnerID := uuid.Must(uuid.NewV7())
	threadID := uuid.Must(uuid.NewV7())
	authorID := uuid.Must(uuid.NewV7())

	// CreateSession
	t.Run("CreateSession: create error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("Create", mock.Anything, mock.AnythingOfType("*classroom.ClassroomSession")).Return(errTestClassroom)
		_, err := d.svc.CreateSession(context.Background(), &ClassroomSession{Title: "T", SessionType: SessionTypeLecture})
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
	})

	// StartSession
	t.Run("StartSession: get error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.StartSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
	})
	t.Run("StartSession: not found", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)
		_, err := d.svc.StartSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, ErrSessionNotFound)
		d.sessions.AssertExpectations(t)
	})

	// EndSession
	t.Run("EndSession: get error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.EndSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
	})
	t.Run("EndSession: not found", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, nil)
		_, err := d.svc.EndSession(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, ErrSessionNotFound)
		d.sessions.AssertExpectations(t)
	})

	// CreateSessionQuiz
	t.Run("CreateSessionQuiz: get error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.CreateSessionQuiz(context.Background(), sessionID, tenantID, uuid.Must(uuid.NewV7()))
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
	})

	// CreateDiscussion
	t.Run("CreateDiscussion: get error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.CreateDiscussion(context.Background(), sessionID, tenantID, authorID, "x")
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
	})

	// ListDiscussions
	t.Run("ListDiscussions: get error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.ListDiscussions(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
	})

	// PinDiscussion
	t.Run("PinDiscussion: get error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.discussions.On("GetByID", mock.Anything, threadID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.PinDiscussion(context.Background(), sessionID, tenantID, threadID, true)
		assert.ErrorIs(t, err, errTestClassroom)
		d.discussions.AssertExpectations(t)
	})

	// UpsertKioskConfig
	t.Run("UpsertKioskConfig: get error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.UpsertKioskConfig(context.Background(), sessionID, tenantID, "https://x", true, nil, nil)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
	})

	// CreateLearnerProfile
	t.Run("CreateLearnerProfile: get error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.CreateLearnerProfile(context.Background(), sessionID, tenantID, learnerID, "Alice", nil, nil)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
	})

	// UpdateLearnerProfile
	t.Run("UpdateLearnerProfile: lookup error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.learnerProfs.On("GetBySessionAndLearner", mock.Anything, sessionID, learnerID).Return(nil, errTestClassroom)
		_, err := d.svc.UpdateLearnerProfile(context.Background(), sessionID, tenantID, learnerID, "Bob", nil, nil)
		assert.ErrorIs(t, err, errTestClassroom)
		d.learnerProfs.AssertExpectations(t)
	})

	// ListTransactions
	t.Run("ListTransactions: get error", func(t *testing.T) {
		d := newTestSessionSvc()
		d.sessions.On("GetByID", mock.Anything, sessionID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.ListTransactions(context.Background(), sessionID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.sessions.AssertExpectations(t)
	})
}

func TestPollGetByID_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	pollID := uuid.Must(uuid.NewV7())
	questionID := uuid.Must(uuid.NewV7())

	t.Run("GetPollVisualization: get error", func(t *testing.T) {
		d := newTestPollFullService()
		d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.GetPollVisualization(context.Background(), pollID, tenantID)
		assert.ErrorIs(t, err, errTestClassroom)
		d.pollR.AssertExpectations(t)
	})

	t.Run("CastAnonymousVote: get error", func(t *testing.T) {
		d := newTestPollFullService()
		d.pollR.On("GetByID", mock.Anything, pollID, tenantID).Return(nil, errTestClassroom)
		_, err := d.svc.CastAnonymousVote(context.Background(), pollID, tenantID, questionID, nil, "h")
		assert.ErrorIs(t, err, errTestClassroom)
		d.pollR.AssertExpectations(t)
	})
}

func TestAddEntry_GetByIDError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	boardID := uuid.Must(uuid.NewV7())

	d := newTestJamBoardService()
	d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(nil, errTestClassroom)

	_, err := d.svc.AddEntry(context.Background(), boardID, tenantID, uuid.Must(uuid.NewV7()), &JamBoardEntry{})
	assert.ErrorIs(t, err, errTestClassroom)
	d.boardR.AssertExpectations(t)
}
