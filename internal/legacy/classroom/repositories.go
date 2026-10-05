package classroom

import (
	"context"

	"github.com/google/uuid"
)

// LiveQuizSessionRepository defines the data access interface for LiveQuizSession entities.
type LiveQuizSessionRepository interface {
	// Create persists a new LiveQuizSession.
	Create(ctx context.Context, quiz *LiveQuizSession) error

	// GetByID retrieves a quiz session by ID and tenant. Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*LiveQuizSession, error)

	// Update persists changes to an existing LiveQuizSession.
	Update(ctx context.Context, quiz *LiveQuizSession) error
}

// QuizParticipantRepository defines the data access interface for QuizParticipant entities.
type QuizParticipantRepository interface {
	// Create persists a new QuizParticipant.
	Create(ctx context.Context, participant *QuizParticipant) error

	// GetByQuizAndGCID retrieves a participant by quiz session ID and GCID.
	// Returns nil if not found.
	GetByQuizAndGCID(ctx context.Context, quizID, gcid uuid.UUID) (*QuizParticipant, error)

	// ListByQuiz returns all participants for a quiz session, ordered by total_points descending.
	ListByQuiz(ctx context.Context, quizID uuid.UUID) ([]QuizParticipant, error)

	// Update persists changes to an existing QuizParticipant.
	Update(ctx context.Context, participant *QuizParticipant) error

	// HasAnswered checks whether a participant has already answered a specific question.
	HasAnswered(ctx context.Context, participantID uuid.UUID, questionIndex int) (bool, error)

	// RecordAnswer persists a quiz answer for idempotency tracking.
	RecordAnswer(ctx context.Context, answer *QuizAnswer) error

	// CountByQuiz returns the number of participants in a quiz session.
	CountByQuiz(ctx context.Context, quizID uuid.UUID) (int, error)
}

// LivePollSessionRepository defines the data access interface for LivePollSession entities.
type LivePollSessionRepository interface {
	// Create persists a new LivePollSession.
	Create(ctx context.Context, poll *LivePollSession) error

	// GetByID retrieves a poll session by ID and tenant. Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*LivePollSession, error)

	// Update persists changes to an existing LivePollSession.
	Update(ctx context.Context, poll *LivePollSession) error
}

// PollVoteRepository defines the data access interface for PollVote entities.
type PollVoteRepository interface {
	// Create persists a new PollVote.
	Create(ctx context.Context, vote *PollVote) error

	// HasVoted checks whether a GCID has already voted on a poll.
	HasVoted(ctx context.Context, pollID, gcid uuid.UUID) (bool, error)

	// ListByPoll returns all votes for a poll session.
	ListByPoll(ctx context.Context, pollID uuid.UUID) ([]PollVote, error)

	// CountByPoll returns the total number of votes for a poll session.
	CountByPoll(ctx context.Context, pollID uuid.UUID) (int, error)
}

// JamBoardRepository defines the data access interface for JamBoard entities.
type JamBoardRepository interface {
	// Create persists a new JamBoard.
	Create(ctx context.Context, board *JamBoard) error

	// GetByID retrieves a jamboard by ID and tenant. Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*JamBoard, error)

	// Update persists changes to an existing JamBoard.
	Update(ctx context.Context, board *JamBoard) error
}

// JamBoardEntryRepository defines the data access interface for JamBoardEntry entities.
type JamBoardEntryRepository interface {
	// Create persists a new JamBoardEntry.
	Create(ctx context.Context, entry *JamBoardEntry) error

	// ListByBoard returns all entries for a jamboard.
	ListByBoard(ctx context.Context, boardID uuid.UUID) ([]JamBoardEntry, error)
}

// ClassProfileRepository defines the data access interface for ClassProfile entities.
type ClassProfileRepository interface {
	// GetBySessionID retrieves a class profile by training session ID and tenant.
	// Returns nil if not found.
	GetBySessionID(ctx context.Context, sessionID, tenantID uuid.UUID) (*ClassProfile, error)

	// Save creates or updates a ClassProfile.
	Save(ctx context.Context, profile *ClassProfile) error
}

// ClassroomSessionRepository defines the data access interface for ClassroomSession entities.
type ClassroomSessionRepository interface {
	// Create persists a new ClassroomSession.
	Create(ctx context.Context, session *ClassroomSession) error

	// GetByID retrieves a session by ID and tenant. Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*ClassroomSession, error)

	// ListByTenant returns all non-deleted sessions for a tenant.
	ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]ClassroomSession, error)

	// Update persists changes to an existing ClassroomSession.
	Update(ctx context.Context, session *ClassroomSession) error
}

// DiscussionThreadRepository defines the data access interface for DiscussionThread entities.
type DiscussionThreadRepository interface {
	// Create persists a new DiscussionThread.
	Create(ctx context.Context, thread *DiscussionThread) error

	// GetByID retrieves a discussion thread by ID and tenant. Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*DiscussionThread, error)

	// ListBySession returns all non-deleted threads for a session.
	ListBySession(ctx context.Context, sessionID uuid.UUID) ([]DiscussionThread, error)

	// Update persists changes to an existing DiscussionThread.
	Update(ctx context.Context, thread *DiscussionThread) error
}

// KioskConfigRepository defines the data access interface for KioskConfig entities.
type KioskConfigRepository interface {
	// GetBySessionID retrieves a kiosk config by session ID and tenant. Returns nil if not found.
	GetBySessionID(ctx context.Context, sessionID, tenantID uuid.UUID) (*KioskConfig, error)

	// Save creates or updates a KioskConfig.
	Save(ctx context.Context, config *KioskConfig) error
}

// LearnerClassProfileRepository defines the data access interface for LearnerClassProfile entities.
type LearnerClassProfileRepository interface {
	// GetBySessionAndLearner retrieves a learner profile by session and learner. Returns nil if not found.
	GetBySessionAndLearner(ctx context.Context, sessionID, learnerID uuid.UUID) (*LearnerClassProfile, error)

	// Create persists a new LearnerClassProfile.
	Create(ctx context.Context, profile *LearnerClassProfile) error

	// Update persists changes to an existing LearnerClassProfile.
	Update(ctx context.Context, profile *LearnerClassProfile) error
}

// SessionTransactionRepository defines the data access interface for SessionTransaction entities.
type SessionTransactionRepository interface {
	// Create persists a new SessionTransaction (append-only).
	Create(ctx context.Context, tx *SessionTransaction) error

	// ListBySession returns all transactions for a session.
	ListBySession(ctx context.Context, sessionID uuid.UUID) ([]SessionTransaction, error)
}

// QuizTeamRepository defines the data access interface for QuizTeam entities.
type QuizTeamRepository interface {
	// Create persists a new QuizTeam.
	Create(ctx context.Context, team *QuizTeam) error

	// GetByID retrieves a team by ID. Returns nil if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*QuizTeam, error)

	// ListByQuiz returns all teams for a quiz.
	ListByQuiz(ctx context.Context, quizID uuid.UUID) ([]QuizTeam, error)
}

// QuizTeamMemberRepository defines the data access interface for QuizTeamMember entities.
type QuizTeamMemberRepository interface {
	// Create persists a new QuizTeamMember.
	Create(ctx context.Context, member *QuizTeamMember) error

	// ListByTeam returns all members for a team.
	ListByTeam(ctx context.Context, teamID uuid.UUID) ([]QuizTeamMember, error)

	// ListByQuiz returns all team members across all teams for a quiz.
	ListByQuiz(ctx context.Context, quizID uuid.UUID) ([]QuizTeamMember, error)
}

// LivePollQuestionRepository defines the data access interface for LivePollQuestion entities.
type LivePollQuestionRepository interface {
	// Create persists a new LivePollQuestion.
	Create(ctx context.Context, question *LivePollQuestion) error

	// ListByPoll returns all questions for a poll.
	ListByPoll(ctx context.Context, pollID uuid.UUID) ([]LivePollQuestion, error)
}

// LivePollVoteRepository defines the data access interface for LivePollVote entities.
type LivePollVoteRepository interface {
	// Create persists a new LivePollVote (immutable).
	Create(ctx context.Context, vote *LivePollVote) error

	// ExistsByHash checks if a vote with the given hash already exists.
	ExistsByHash(ctx context.Context, voteHash string) (bool, error)

	// ListByQuestion returns all votes for a question.
	ListByQuestion(ctx context.Context, questionID uuid.UUID) ([]LivePollVote, error)
}

// EmbeddedPollRepository defines the data access interface for EmbeddedPoll entities.
type EmbeddedPollRepository interface {
	// Create persists a new EmbeddedPoll.
	Create(ctx context.Context, ep *EmbeddedPoll) error

	// ListByQuiz returns all embedded polls for a quiz.
	ListByQuiz(ctx context.Context, quizID uuid.UUID) ([]EmbeddedPoll, error)
}

// QuizAnalyticsRepository defines the data access interface for QuizAnalytics entities.
type QuizAnalyticsRepository interface {
	// GetByQuizID retrieves analytics by quiz ID. Returns nil if not found.
	GetByQuizID(ctx context.Context, quizID uuid.UUID) (*QuizAnalytics, error)

	// Save creates or updates QuizAnalytics.
	Save(ctx context.Context, analytics *QuizAnalytics) error
}

// EventPublisher abstracts the event bus (Cloud Pub/Sub in production,
// in-memory or emulator in tests/local dev).
type EventPublisher interface {
	// Publish sends a domain event to the specified topic.
	Publish(ctx context.Context, topic string, event interface{}) error

	// Close releases any resources held by the publisher.
	Close() error
}
