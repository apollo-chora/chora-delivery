package classroom

import (
	"context"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
)

// Compile-time interface checks.
var (
	_ LiveQuizSessionRepository     = (*mockQuizRepo)(nil)
	_ QuizParticipantRepository     = (*mockParticipantRepo)(nil)
	_ LivePollSessionRepository     = (*mockPollRepo)(nil)
	_ PollVoteRepository            = (*mockVoteRepo)(nil)
	_ JamBoardRepository            = (*mockBoardRepo)(nil)
	_ JamBoardEntryRepository       = (*mockEntryRepo)(nil)
	_ ClassProfileRepository        = (*mockProfileRepo)(nil)
	_ ClassroomSessionRepository    = (*mockSessionRepo)(nil)
	_ DiscussionThreadRepository    = (*mockDiscussionRepo)(nil)
	_ KioskConfigRepository         = (*mockKioskRepo)(nil)
	_ LearnerClassProfileRepository = (*mockLearnerProfileRepo)(nil)
	_ SessionTransactionRepository  = (*mockTransactionRepo)(nil)
	_ QuizTeamRepository            = (*mockTeamRepo)(nil)
	_ QuizTeamMemberRepository      = (*mockTeamMemberRepo)(nil)
	_ LivePollQuestionRepository    = (*mockPollQuestionRepo)(nil)
	_ LivePollVoteRepository        = (*mockLivePollVoteRepo)(nil)
	_ EmbeddedPollRepository        = (*mockEmbeddedPollRepo)(nil)
	_ QuizAnalyticsRepository       = (*mockAnalyticsRepo)(nil)
	_ EventPublisher                = (*mockEventPublisher)(nil)
)

// ---------------------------------------------------------------------------
// mockQuizRepo — LiveQuizSessionRepository
// ---------------------------------------------------------------------------

type mockQuizRepo struct{ mock.Mock }

func (m *mockQuizRepo) Create(ctx context.Context, quiz *LiveQuizSession) error {
	return m.Called(ctx, quiz).Error(0)
}

func (m *mockQuizRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*LiveQuizSession, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*LiveQuizSession), args.Error(1)
}

func (m *mockQuizRepo) Update(ctx context.Context, quiz *LiveQuizSession) error {
	return m.Called(ctx, quiz).Error(0)
}

// ---------------------------------------------------------------------------
// mockParticipantRepo — QuizParticipantRepository
// ---------------------------------------------------------------------------

type mockParticipantRepo struct{ mock.Mock }

func (m *mockParticipantRepo) Create(ctx context.Context, participant *QuizParticipant) error {
	return m.Called(ctx, participant).Error(0)
}

func (m *mockParticipantRepo) GetByQuizAndGCID(ctx context.Context, quizID, gcid uuid.UUID) (*QuizParticipant, error) {
	args := m.Called(ctx, quizID, gcid)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*QuizParticipant), args.Error(1)
}

func (m *mockParticipantRepo) ListByQuiz(ctx context.Context, quizID uuid.UUID) ([]QuizParticipant, error) {
	args := m.Called(ctx, quizID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]QuizParticipant), args.Error(1)
}

func (m *mockParticipantRepo) Update(ctx context.Context, participant *QuizParticipant) error {
	return m.Called(ctx, participant).Error(0)
}

func (m *mockParticipantRepo) HasAnswered(ctx context.Context, participantID uuid.UUID, questionIndex int) (bool, error) {
	args := m.Called(ctx, participantID, questionIndex)
	return args.Bool(0), args.Error(1)
}

func (m *mockParticipantRepo) RecordAnswer(ctx context.Context, answer *QuizAnswer) error {
	return m.Called(ctx, answer).Error(0)
}

func (m *mockParticipantRepo) CountByQuiz(ctx context.Context, quizID uuid.UUID) (int, error) {
	args := m.Called(ctx, quizID)
	return args.Int(0), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockPollRepo — LivePollSessionRepository
// ---------------------------------------------------------------------------

type mockPollRepo struct{ mock.Mock }

func (m *mockPollRepo) Create(ctx context.Context, poll *LivePollSession) error {
	return m.Called(ctx, poll).Error(0)
}

func (m *mockPollRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*LivePollSession, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*LivePollSession), args.Error(1)
}

func (m *mockPollRepo) Update(ctx context.Context, poll *LivePollSession) error {
	return m.Called(ctx, poll).Error(0)
}

// ---------------------------------------------------------------------------
// mockVoteRepo — PollVoteRepository
// ---------------------------------------------------------------------------

type mockVoteRepo struct{ mock.Mock }

func (m *mockVoteRepo) Create(ctx context.Context, vote *PollVote) error {
	return m.Called(ctx, vote).Error(0)
}

func (m *mockVoteRepo) HasVoted(ctx context.Context, pollID, gcid uuid.UUID) (bool, error) {
	args := m.Called(ctx, pollID, gcid)
	return args.Bool(0), args.Error(1)
}

func (m *mockVoteRepo) ListByPoll(ctx context.Context, pollID uuid.UUID) ([]PollVote, error) {
	args := m.Called(ctx, pollID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]PollVote), args.Error(1)
}

func (m *mockVoteRepo) CountByPoll(ctx context.Context, pollID uuid.UUID) (int, error) {
	args := m.Called(ctx, pollID)
	return args.Int(0), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockBoardRepo — JamBoardRepository
// ---------------------------------------------------------------------------

type mockBoardRepo struct{ mock.Mock }

func (m *mockBoardRepo) Create(ctx context.Context, board *JamBoard) error {
	return m.Called(ctx, board).Error(0)
}

func (m *mockBoardRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*JamBoard, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*JamBoard), args.Error(1)
}

func (m *mockBoardRepo) Update(ctx context.Context, board *JamBoard) error {
	return m.Called(ctx, board).Error(0)
}

// ---------------------------------------------------------------------------
// mockEntryRepo — JamBoardEntryRepository
// ---------------------------------------------------------------------------

type mockEntryRepo struct{ mock.Mock }

func (m *mockEntryRepo) Create(ctx context.Context, entry *JamBoardEntry) error {
	return m.Called(ctx, entry).Error(0)
}

func (m *mockEntryRepo) ListByBoard(ctx context.Context, boardID uuid.UUID) ([]JamBoardEntry, error) {
	args := m.Called(ctx, boardID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]JamBoardEntry), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockProfileRepo — ClassProfileRepository
// ---------------------------------------------------------------------------

type mockProfileRepo struct{ mock.Mock }

func (m *mockProfileRepo) GetBySessionID(ctx context.Context, sessionID, tenantID uuid.UUID) (*ClassProfile, error) {
	args := m.Called(ctx, sessionID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ClassProfile), args.Error(1)
}

func (m *mockProfileRepo) Save(ctx context.Context, profile *ClassProfile) error {
	return m.Called(ctx, profile).Error(0)
}

// ---------------------------------------------------------------------------
// mockEventPublisher — EventPublisher
// ---------------------------------------------------------------------------

type mockEventPublisher struct{ mock.Mock }

func (m *mockEventPublisher) Publish(ctx context.Context, topic string, event interface{}) error {
	return m.Called(ctx, topic, event).Error(0)
}

func (m *mockEventPublisher) Close() error {
	return m.Called().Error(0)
}

// ---------------------------------------------------------------------------
// mockSessionRepo — ClassroomSessionRepository
// ---------------------------------------------------------------------------

type mockSessionRepo struct{ mock.Mock }

func (m *mockSessionRepo) Create(ctx context.Context, session *ClassroomSession) error {
	return m.Called(ctx, session).Error(0)
}

func (m *mockSessionRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*ClassroomSession, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ClassroomSession), args.Error(1)
}

func (m *mockSessionRepo) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]ClassroomSession, error) {
	args := m.Called(ctx, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]ClassroomSession), args.Error(1)
}

func (m *mockSessionRepo) Update(ctx context.Context, session *ClassroomSession) error {
	return m.Called(ctx, session).Error(0)
}

// ---------------------------------------------------------------------------
// mockDiscussionRepo — DiscussionThreadRepository
// ---------------------------------------------------------------------------

type mockDiscussionRepo struct{ mock.Mock }

func (m *mockDiscussionRepo) Create(ctx context.Context, thread *DiscussionThread) error {
	return m.Called(ctx, thread).Error(0)
}

func (m *mockDiscussionRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*DiscussionThread, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*DiscussionThread), args.Error(1)
}

func (m *mockDiscussionRepo) ListBySession(ctx context.Context, sessionID uuid.UUID) ([]DiscussionThread, error) {
	args := m.Called(ctx, sessionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]DiscussionThread), args.Error(1)
}

func (m *mockDiscussionRepo) Update(ctx context.Context, thread *DiscussionThread) error {
	return m.Called(ctx, thread).Error(0)
}

// ---------------------------------------------------------------------------
// mockKioskRepo — KioskConfigRepository
// ---------------------------------------------------------------------------

type mockKioskRepo struct{ mock.Mock }

func (m *mockKioskRepo) GetBySessionID(ctx context.Context, sessionID, tenantID uuid.UUID) (*KioskConfig, error) {
	args := m.Called(ctx, sessionID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*KioskConfig), args.Error(1)
}

func (m *mockKioskRepo) Save(ctx context.Context, config *KioskConfig) error {
	return m.Called(ctx, config).Error(0)
}

// ---------------------------------------------------------------------------
// mockLearnerProfileRepo — LearnerClassProfileRepository
// ---------------------------------------------------------------------------

type mockLearnerProfileRepo struct{ mock.Mock }

func (m *mockLearnerProfileRepo) GetBySessionAndLearner(ctx context.Context, sessionID, learnerID uuid.UUID) (*LearnerClassProfile, error) {
	args := m.Called(ctx, sessionID, learnerID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*LearnerClassProfile), args.Error(1)
}

func (m *mockLearnerProfileRepo) Create(ctx context.Context, profile *LearnerClassProfile) error {
	return m.Called(ctx, profile).Error(0)
}

func (m *mockLearnerProfileRepo) Update(ctx context.Context, profile *LearnerClassProfile) error {
	return m.Called(ctx, profile).Error(0)
}

// ---------------------------------------------------------------------------
// mockTransactionRepo — SessionTransactionRepository
// ---------------------------------------------------------------------------

type mockTransactionRepo struct{ mock.Mock }

func (m *mockTransactionRepo) Create(ctx context.Context, tx *SessionTransaction) error {
	return m.Called(ctx, tx).Error(0)
}

func (m *mockTransactionRepo) ListBySession(ctx context.Context, sessionID uuid.UUID) ([]SessionTransaction, error) {
	args := m.Called(ctx, sessionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SessionTransaction), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockTeamRepo — QuizTeamRepository
// ---------------------------------------------------------------------------

type mockTeamRepo struct{ mock.Mock }

func (m *mockTeamRepo) Create(ctx context.Context, team *QuizTeam) error {
	return m.Called(ctx, team).Error(0)
}

func (m *mockTeamRepo) GetByID(ctx context.Context, id uuid.UUID) (*QuizTeam, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*QuizTeam), args.Error(1)
}

func (m *mockTeamRepo) ListByQuiz(ctx context.Context, quizID uuid.UUID) ([]QuizTeam, error) {
	args := m.Called(ctx, quizID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]QuizTeam), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockTeamMemberRepo — QuizTeamMemberRepository
// ---------------------------------------------------------------------------

type mockTeamMemberRepo struct{ mock.Mock }

func (m *mockTeamMemberRepo) Create(ctx context.Context, member *QuizTeamMember) error {
	return m.Called(ctx, member).Error(0)
}

func (m *mockTeamMemberRepo) ListByTeam(ctx context.Context, teamID uuid.UUID) ([]QuizTeamMember, error) {
	args := m.Called(ctx, teamID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]QuizTeamMember), args.Error(1)
}

func (m *mockTeamMemberRepo) ListByQuiz(ctx context.Context, quizID uuid.UUID) ([]QuizTeamMember, error) {
	args := m.Called(ctx, quizID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]QuizTeamMember), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockPollQuestionRepo — LivePollQuestionRepository
// ---------------------------------------------------------------------------

type mockPollQuestionRepo struct{ mock.Mock }

func (m *mockPollQuestionRepo) Create(ctx context.Context, question *LivePollQuestion) error {
	return m.Called(ctx, question).Error(0)
}

func (m *mockPollQuestionRepo) ListByPoll(ctx context.Context, pollID uuid.UUID) ([]LivePollQuestion, error) {
	args := m.Called(ctx, pollID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]LivePollQuestion), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockLivePollVoteRepo — LivePollVoteRepository
// ---------------------------------------------------------------------------

type mockLivePollVoteRepo struct{ mock.Mock }

func (m *mockLivePollVoteRepo) Create(ctx context.Context, vote *LivePollVote) error {
	return m.Called(ctx, vote).Error(0)
}

func (m *mockLivePollVoteRepo) ExistsByHash(ctx context.Context, voteHash string) (bool, error) {
	args := m.Called(ctx, voteHash)
	return args.Bool(0), args.Error(1)
}

func (m *mockLivePollVoteRepo) ListByQuestion(ctx context.Context, questionID uuid.UUID) ([]LivePollVote, error) {
	args := m.Called(ctx, questionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]LivePollVote), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockEmbeddedPollRepo — EmbeddedPollRepository
// ---------------------------------------------------------------------------

type mockEmbeddedPollRepo struct{ mock.Mock }

func (m *mockEmbeddedPollRepo) Create(ctx context.Context, ep *EmbeddedPoll) error {
	return m.Called(ctx, ep).Error(0)
}

func (m *mockEmbeddedPollRepo) ListByQuiz(ctx context.Context, quizID uuid.UUID) ([]EmbeddedPoll, error) {
	args := m.Called(ctx, quizID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]EmbeddedPoll), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockAnalyticsRepo — QuizAnalyticsRepository
// ---------------------------------------------------------------------------

type mockAnalyticsRepo struct{ mock.Mock }

func (m *mockAnalyticsRepo) GetByQuizID(ctx context.Context, quizID uuid.UUID) (*QuizAnalytics, error) {
	args := m.Called(ctx, quizID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*QuizAnalytics), args.Error(1)
}

func (m *mockAnalyticsRepo) Save(ctx context.Context, analytics *QuizAnalytics) error {
	return m.Called(ctx, analytics).Error(0)
}
