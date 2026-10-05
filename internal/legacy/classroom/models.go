package classroom

import (
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// QuizStatus represents the lifecycle state of a live quiz session.
type QuizStatus string

const (
	QuizStatusWaiting QuizStatus = "waiting"
	QuizStatusActive  QuizStatus = "active"
	QuizStatusPaused  QuizStatus = "paused"
	QuizStatusEnded   QuizStatus = "ended"
)

// SessionType represents the type of classroom session.
type SessionType string

const (
	SessionTypeLecture  SessionType = "lecture"
	SessionTypeWorkshop SessionType = "workshop"
	SessionTypeSeminar  SessionType = "seminar"
	SessionTypeLab      SessionType = "lab"
)

// IsValid returns true if the SessionType is a recognized value.
func (st SessionType) IsValid() bool {
	switch st {
	case SessionTypeLecture, SessionTypeWorkshop, SessionTypeSeminar, SessionTypeLab:
		return true
	default:
		return false
	}
}

// SessionStatus represents the lifecycle state of a classroom session.
type SessionStatus string

const (
	SessionStatusScheduled SessionStatus = "scheduled"
	SessionStatusLive      SessionStatus = "live"
	SessionStatusEnded     SessionStatus = "ended"
)

// TransactionType represents the type of session transaction.
type TransactionType string

const (
	TransactionTypeJoin       TransactionType = "join"
	TransactionTypeLeave      TransactionType = "leave"
	TransactionTypeQuizStart  TransactionType = "quiz_start"
	TransactionTypeQuizEnd    TransactionType = "quiz_end"
	TransactionTypePollStart  TransactionType = "poll_start"
	TransactionTypePollEnd    TransactionType = "poll_end"
	TransactionTypeBoardEntry TransactionType = "board_entry"
	TransactionTypeMessage    TransactionType = "message"
	TransactionTypePin        TransactionType = "pin"
	TransactionTypeKick       TransactionType = "kick"
)

// DeliveryMode represents the quiz delivery mode.
type DeliveryMode string

const (
	DeliveryModeIndividual  DeliveryMode = "individual"
	DeliveryModeTeam        DeliveryMode = "team"
	DeliveryModeElimination DeliveryMode = "elimination"
	DeliveryModeSpeedRound  DeliveryMode = "speed_round"
)

// TimerMode represents the quiz timer mode.
type TimerMode string

const (
	TimerModePerQuestion TimerMode = "per_question"
	TimerModeTotal       TimerMode = "total"
	TimerModeNone        TimerMode = "none"
)

// PollQuestionType represents the type of a poll question.
type PollQuestionType string

const (
	PollQuestionTypeSingleChoice   PollQuestionType = "single_choice"
	PollQuestionTypeMultipleChoice PollQuestionType = "multiple_choice"
	PollQuestionTypeWordCloud      PollQuestionType = "word_cloud"
	PollQuestionTypeRatingScale    PollQuestionType = "rating_scale"
)

// PollType represents the type of poll.
type PollType string

const (
	PollTypeSingleChoice   PollType = "single_choice"
	PollTypeMultipleChoice PollType = "multiple_choice"
	PollTypeWordCloud      PollType = "word_cloud"
	PollTypeRatingScale    PollType = "rating_scale"
)

// IsValid returns true if the PollType is a recognized value.
func (pt PollType) IsValid() bool {
	switch pt {
	case PollTypeSingleChoice, PollTypeMultipleChoice, PollTypeWordCloud, PollTypeRatingScale:
		return true
	default:
		return false
	}
}

// PollStatus represents the lifecycle state of a poll.
type PollStatus string

const (
	PollStatusOpen   PollStatus = "open"
	PollStatusClosed PollStatus = "closed"
)

// EntryType represents the type of jamboard entry.
type EntryType string

const (
	EntryTypeStickyNote EntryType = "sticky_note"
	EntryTypeDrawing    EntryType = "drawing"
	EntryTypeLink       EntryType = "link"
)

// IsValid returns true if the EntryType is a recognized value.
func (et EntryType) IsValid() bool {
	switch et {
	case EntryTypeStickyNote, EntryTypeDrawing, EntryTypeLink:
		return true
	default:
		return false
	}
}

// BoardStatus represents the lifecycle state of a jamboard.
type BoardStatus string

const (
	BoardStatusOpen   BoardStatus = "open"
	BoardStatusClosed BoardStatus = "closed"
)

// ---------------------------------------------------------------------------
// Quiz Constants
// ---------------------------------------------------------------------------

const (
	// BasePointsCorrect is the base points for a correct answer.
	BasePointsCorrect = 100

	// TimeBonusMax is the maximum time bonus points.
	TimeBonusMax = 50

	// DefaultTimeLimitSeconds is the default time limit per question.
	DefaultTimeLimitSeconds = 30
)

// ---------------------------------------------------------------------------
// Domain Entities
// ---------------------------------------------------------------------------

// QuizQuestion represents a single question within a live quiz session.
type QuizQuestion struct {
	QuestionText       string     `json:"question_text"`
	Options            []string   `json:"options"`
	CorrectOptionIndex int        `json:"correct_option_index"`
	AtomID             *uuid.UUID `json:"atom_id,omitempty"`
}

// LiveQuizSession is the aggregate root for real-time quiz interactions.
type LiveQuizSession struct {
	ID                   uuid.UUID      `json:"id"`
	TenantID             uuid.UUID      `json:"tenant_id"`
	TrainingSessionID    *uuid.UUID     `json:"training_session_id,omitempty"`
	Title                string         `json:"title"`
	Status               QuizStatus     `json:"status"`
	Questions            []QuizQuestion `json:"questions"`
	CurrentQuestionIndex int            `json:"current_question_index"`
	TotalQuestions       int            `json:"total_questions"`
	ParticipantCount     int            `json:"participant_count"`
	TimeLimitSeconds     int            `json:"time_limit_seconds"`
	DeliveryMode         DeliveryMode   `json:"delivery_mode"`
	TimerMode            TimerMode      `json:"timer_mode"`
	PausedAt             *time.Time     `json:"paused_at,omitempty"`
	CreatedByGCID        uuid.UUID      `json:"created_by_gcid"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

// QuizParticipant tracks a learner's participation and score in a quiz session.
type QuizParticipant struct {
	ID                uuid.UUID  `json:"id"`
	QuizSessionID     uuid.UUID  `json:"quiz_session_id"`
	TenantID          uuid.UUID  `json:"tenant_id"`
	GCID              uuid.UUID  `json:"gcid"`
	DisplayName       string     `json:"display_name"`
	TotalPoints       int        `json:"total_points"`
	CorrectCount      int        `json:"correct_count"`
	AnsweredCount     int        `json:"answered_count"`
	AvgTimeMs         int        `json:"avg_time_ms"`
	LastAnswerCorrect bool       `json:"last_answer_correct"`
	EliminatedAt      *time.Time `json:"eliminated_at,omitempty"`
	JoinedAt          time.Time  `json:"joined_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// QuizAnswer records a single answer submission by a participant.
type QuizAnswer struct {
	ParticipantID       uuid.UUID `json:"participant_id"`
	QuestionIndex       int       `json:"question_index"`
	SelectedOptionIndex int       `json:"selected_option_index"`
	Correct             bool      `json:"correct"`
	PointsEarned        int       `json:"points_earned"`
	TimeTakenMs         int       `json:"time_taken_ms"`
	SubmittedAt         time.Time `json:"submitted_at"`
}

// AnswerResult is the response returned after submitting a quiz answer.
type AnswerResult struct {
	Correct      bool `json:"correct"`
	PointsEarned int  `json:"points_earned"`
	TimeTakenMs  int  `json:"time_taken_ms"`
}

// LeaderboardEntry represents a single entry in the quiz leaderboard.
type LeaderboardEntry struct {
	Rank         int       `json:"rank"`
	GCID         uuid.UUID `json:"gcid"`
	DisplayName  string    `json:"display_name"`
	TotalPoints  int       `json:"total_points"`
	CorrectCount int       `json:"correct_count"`
	AvgTimeMs    int       `json:"avg_time_ms"`
}

// ---------------------------------------------------------------------------
// Poll Entities
// ---------------------------------------------------------------------------

// LivePollSession is the aggregate root for live poll interactions.
type LivePollSession struct {
	ID                uuid.UUID  `json:"id"`
	TenantID          uuid.UUID  `json:"tenant_id"`
	TrainingSessionID *uuid.UUID `json:"training_session_id,omitempty"`
	Question          string     `json:"question"`
	Options           []string   `json:"options"`
	PollType          PollType   `json:"poll_type"`
	Status            PollStatus `json:"status"`
	Anonymous         bool       `json:"anonymous"`
	VoteCount         int        `json:"vote_count"`
	CreatedByGCID     uuid.UUID  `json:"created_by_gcid"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// PollVote records a single vote cast by a learner.
type PollVote struct {
	ID                    uuid.UUID `json:"id"`
	PollSessionID         uuid.UUID `json:"poll_session_id"`
	TenantID              uuid.UUID `json:"tenant_id"`
	GCID                  uuid.UUID `json:"gcid"`
	SelectedOptionIndices []int     `json:"selected_option_indices"`
	CastAt                time.Time `json:"cast_at"`
}

// OptionResult represents the result for a single poll option.
type OptionResult struct {
	OptionText string  `json:"option_text"`
	VoteCount  int     `json:"vote_count"`
	Percentage float64 `json:"percentage"`
}

// PollResults aggregates the results for a poll session.
type PollResults struct {
	PollID        uuid.UUID      `json:"poll_id"`
	Question      string         `json:"question"`
	TotalVotes    int            `json:"total_votes"`
	OptionResults []OptionResult `json:"option_results"`
}

// ---------------------------------------------------------------------------
// JamBoard Entities
// ---------------------------------------------------------------------------

// JamBoard is the aggregate root for collaborative whiteboard interactions.
type JamBoard struct {
	ID                uuid.UUID   `json:"id"`
	TenantID          uuid.UUID   `json:"tenant_id"`
	TrainingSessionID *uuid.UUID  `json:"training_session_id,omitempty"`
	Title             string      `json:"title"`
	Description       string      `json:"description,omitempty"`
	Status            BoardStatus `json:"status"`
	EntryCount        int         `json:"entry_count"`
	CreatedByGCID     uuid.UUID   `json:"created_by_gcid"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

// JamBoardEntry represents a single entry on a jamboard.
type JamBoardEntry struct {
	ID            uuid.UUID `json:"id"`
	BoardID       uuid.UUID `json:"board_id"`
	TenantID      uuid.UUID `json:"tenant_id"`
	EntryType     EntryType `json:"entry_type"`
	Content       string    `json:"content"`
	Color         *string   `json:"color,omitempty"`
	PositionX     *float64  `json:"position_x,omitempty"`
	PositionY     *float64  `json:"position_y,omitempty"`
	CreatedByGCID uuid.UUID `json:"created_by_gcid"`
	CreatedAt     time.Time `json:"created_at"`
}

// JamBoardWithEntries combines a JamBoard with its entries.
type JamBoardWithEntries struct {
	JamBoard
	Entries []JamBoardEntry `json:"entries"`
}

// ---------------------------------------------------------------------------
// Class Profile
// ---------------------------------------------------------------------------

// ClassProfile represents the engagement profile for a training session.
type ClassProfile struct {
	ID                 uuid.UUID        `json:"id"`
	TenantID           uuid.UUID        `json:"tenant_id"`
	TrainingSessionID  uuid.UUID        `json:"training_session_id"`
	TotalParticipants  int              `json:"total_participants"`
	QuizCount          int              `json:"quiz_count"`
	PollCount          int              `json:"poll_count"`
	JamBoardCount      int              `json:"jamboard_count"`
	AvgEngagementScore float64          `json:"avg_engagement_score"`
	TopParticipants    []TopParticipant `json:"top_participants,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
}

// TopParticipant represents a top-performing participant in a class session.
type TopParticipant struct {
	GCID        uuid.UUID `json:"gcid"`
	DisplayName string    `json:"display_name"`
	Score       float64   `json:"score"`
}

// ---------------------------------------------------------------------------
// Display Signage
// ---------------------------------------------------------------------------

// DisplaySignage holds configuration for classroom display screens.
type DisplaySignage struct {
	ID                uuid.UUID  `json:"id"`
	TenantID          uuid.UUID  `json:"tenant_id"`
	TrainingSessionID *uuid.UUID `json:"training_session_id,omitempty"`
	ContentType       string     `json:"content_type"`
	ContentJSON       string     `json:"content_json"`
	Active            bool       `json:"active"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Classroom Session (51.2.1)
// ---------------------------------------------------------------------------

// ClassroomSession represents a live classroom session (lecture, workshop, etc.).
type ClassroomSession struct {
	ID              uuid.UUID     `json:"id"`
	TenantID        uuid.UUID     `json:"tenant_id"`
	Title           string        `json:"title"`
	InstructorID    uuid.UUID     `json:"instructor_id"`
	SessionType     SessionType   `json:"session_type"`
	Status          SessionStatus `json:"status"`
	ScheduledAt     *time.Time    `json:"scheduled_at,omitempty"`
	StartedAt       *time.Time    `json:"started_at,omitempty"`
	EndedAt         *time.Time    `json:"ended_at,omitempty"`
	MaxParticipants *int          `json:"max_participants,omitempty"`
	JamBoardID      *uuid.UUID    `json:"jam_board_id,omitempty"`
	QuizSessionID   *uuid.UUID    `json:"quiz_session_id,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	DeletedAt       *time.Time    `json:"deleted_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Discussion Thread (51.2.3)
// ---------------------------------------------------------------------------

// DiscussionThread represents a threaded discussion within a classroom session.
type DiscussionThread struct {
	ID        uuid.UUID  `json:"id"`
	TenantID  uuid.UUID  `json:"tenant_id"`
	SessionID uuid.UUID  `json:"session_id"`
	AuthorID  uuid.UUID  `json:"author_id"`
	Content   string     `json:"content"`
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	Pinned    bool       `json:"pinned"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Kiosk Config (51.2.4)
// ---------------------------------------------------------------------------

// KioskConfig holds kiosk mode configuration for a classroom session.
type KioskConfig struct {
	ID                     uuid.UUID              `json:"id"`
	TenantID               uuid.UUID              `json:"tenant_id"`
	SessionID              uuid.UUID              `json:"session_id"`
	DisplayURL             string                 `json:"display_url"`
	AutoAdvance            bool                   `json:"auto_advance"`
	AdvanceIntervalSeconds *int                   `json:"advance_interval_seconds,omitempty"`
	ContentFilter          map[string]interface{} `json:"content_filter,omitempty"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Learner Class Profile (51.2.5)
// ---------------------------------------------------------------------------

// LearnerClassProfile represents a learner's social identity within a classroom session.
type LearnerClassProfile struct {
	ID          uuid.UUID              `json:"id"`
	TenantID    uuid.UUID              `json:"tenant_id"`
	LearnerID   uuid.UUID              `json:"learner_id"`
	SessionID   uuid.UUID              `json:"session_id"`
	DisplayName string                 `json:"display_name"`
	AvatarURL   *string                `json:"avatar_url,omitempty"`
	Bio         *string                `json:"bio,omitempty"`
	Badges      map[string]interface{} `json:"badges,omitempty"`
	JoinedAt    time.Time              `json:"joined_at"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	DeletedAt   *time.Time             `json:"deleted_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Session Transaction (51.2.6)
// ---------------------------------------------------------------------------

// SessionTransaction is an immutable audit log entry for classroom session events.
// No updated_at, no deleted_at — append-only by design.
type SessionTransaction struct {
	ID              uuid.UUID              `json:"id"`
	TenantID        uuid.UUID              `json:"tenant_id"`
	SessionID       uuid.UUID              `json:"session_id"`
	ActorID         uuid.UUID              `json:"actor_id"`
	TransactionType TransactionType        `json:"transaction_type"`
	Payload         map[string]interface{} `json:"payload,omitempty"`
	CreatedAt       time.Time              `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Quiz Teams (51.3.10)
// ---------------------------------------------------------------------------

// QuizTeam represents a team within a team-based quiz session.
type QuizTeam struct {
	ID        uuid.UUID `json:"id"`
	QuizID    uuid.UUID `json:"quiz_id"`
	TeamName  string    `json:"team_name"`
	TeamColor *string   `json:"team_color,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// QuizTeamMember represents a participant assigned to a quiz team.
type QuizTeamMember struct {
	ID            uuid.UUID `json:"id"`
	TeamID        uuid.UUID `json:"team_id"`
	ParticipantID uuid.UUID `json:"participant_id"`
	CreatedAt     time.Time `json:"created_at"`
}

// TeamLeaderboardEntry represents a team's aggregate score in the leaderboard.
type TeamLeaderboardEntry struct {
	Rank        int       `json:"rank"`
	TeamID      uuid.UUID `json:"team_id"`
	TeamName    string    `json:"team_name"`
	TeamColor   *string   `json:"team_color,omitempty"`
	TotalPoints int       `json:"total_points"`
	MemberCount int       `json:"member_count"`
}

// ---------------------------------------------------------------------------
// Elimination Result (51.3.11)
// ---------------------------------------------------------------------------

// EliminationResult holds the outcome of an elimination round.
type EliminationResult struct {
	QuizID                 uuid.UUID   `json:"quiz_id"`
	EliminatedCount        int         `json:"eliminated_count"`
	RemainingCount         int         `json:"remaining_count"`
	EliminatedParticipants []uuid.UUID `json:"eliminated_participants,omitempty"`
}

// ---------------------------------------------------------------------------
// Poll Questions (51.3.12)
// ---------------------------------------------------------------------------

// LivePollQuestion represents a single question within a multi-question poll.
type LivePollQuestion struct {
	ID           uuid.UUID              `json:"id"`
	PollID       uuid.UUID              `json:"poll_id"`
	QuestionText string                 `json:"question_text"`
	QuestionType PollQuestionType       `json:"question_type"`
	Options      map[string]interface{} `json:"options,omitempty"`
	DisplayOrder int                    `json:"display_order"`
	CreatedAt    time.Time              `json:"created_at"`
}

// LivePollVote represents an individual vote on a poll question (immutable).
type LivePollVote struct {
	ID              uuid.UUID              `json:"id"`
	QuestionID      uuid.UUID              `json:"question_id"`
	VoterID         *uuid.UUID             `json:"voter_id,omitempty"`
	SelectedOptions map[string]interface{} `json:"selected_options"`
	Anonymous       bool                   `json:"anonymous"`
	VoteHash        *string                `json:"vote_hash,omitempty"`
	CreatedAt       time.Time              `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Poll Visualization (51.3.15)
// ---------------------------------------------------------------------------

// VisualizationDataPoint represents a single data point for poll visualization.
type VisualizationDataPoint struct {
	Label      string  `json:"label"`
	Value      float64 `json:"value"`
	Percentage float64 `json:"percentage,omitempty"`
}

// PollVisualization holds aggregated poll results for chart rendering.
type PollVisualization struct {
	PollID     uuid.UUID                `json:"poll_id"`
	ChartType  string                   `json:"chart_type"`
	DataPoints []VisualizationDataPoint `json:"data_points"`
	TotalVotes int                      `json:"total_votes"`
}

// ---------------------------------------------------------------------------
// Embedded Poll (51.3.16)
// ---------------------------------------------------------------------------

// EmbeddedPoll links a poll to trigger within a quiz after a specific question.
type EmbeddedPoll struct {
	ID                   uuid.UUID `json:"id"`
	QuizID               uuid.UUID `json:"quiz_id"`
	PollID               uuid.UUID `json:"poll_id"`
	TriggerAfterQuestion int       `json:"trigger_after_question"`
	CreatedAt            time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Quiz Analytics (51.3.17)
// ---------------------------------------------------------------------------

// QuestionAnalytics holds per-question performance statistics.
type QuestionAnalytics struct {
	QuestionIndex      int    `json:"question_index"`
	QuestionText       string `json:"question_text"`
	TotalAnswers       int    `json:"total_answers"`
	CorrectCount       int    `json:"correct_count"`
	IncorrectCount     int    `json:"incorrect_count"`
	AvgTimeMs          int    `json:"avg_time_ms"`
	OptionDistribution []int  `json:"option_distribution,omitempty"`
}

// QuizAnalytics holds aggregate analytics for a quiz session.
type QuizAnalytics struct {
	ID                     uuid.UUID              `json:"id"`
	QuizID                 uuid.UUID              `json:"quiz_id"`
	TotalParticipants      int                    `json:"total_participants"`
	AvgScore               float64                `json:"avg_score"`
	AvgResponseTimeMs      int                    `json:"avg_response_time_ms"`
	QuestionsAnalytics     []QuestionAnalytics    `json:"questions_analytics,omitempty"`
	DifficultyDistribution map[string]interface{} `json:"difficulty_distribution,omitempty"`
	CreatedAt              time.Time              `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Score Calculation
// ---------------------------------------------------------------------------

// CalculatePoints computes quiz answer points based on correctness and speed.
// Correct answers earn BasePointsCorrect + a time bonus that decreases linearly
// from TimeBonusMax to 0 over the time limit.
func CalculatePoints(correct bool, timeTakenMs, timeLimitSeconds int) int {
	if !correct {
		return 0
	}
	timeLimitMs := timeLimitSeconds * 1000
	if timeTakenMs >= timeLimitMs {
		return BasePointsCorrect
	}
	bonus := TimeBonusMax * (timeLimitMs - timeTakenMs) / timeLimitMs
	return BasePointsCorrect + bonus
}
