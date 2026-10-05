package classroom

import "errors"

// Sentinel errors for the classroom domain.
// Error codes use CLASSROOM_ prefix per error-handling conventions.
var (
	// ErrQuizNotFound is returned when a LiveQuizSession cannot be found.
	ErrQuizNotFound = errors.New("CLASSROOM_QUIZ_NOT_FOUND")

	// ErrQuizNotActive is returned when attempting an action on a quiz that is not active.
	ErrQuizNotActive = errors.New("CLASSROOM_QUIZ_NOT_ACTIVE")

	// ErrQuizAlreadyStarted is returned when attempting to start an already-started quiz.
	ErrQuizAlreadyStarted = errors.New("CLASSROOM_QUIZ_ALREADY_STARTED")

	// ErrQuizAlreadyEnded is returned when attempting actions on an ended quiz.
	ErrQuizAlreadyEnded = errors.New("CLASSROOM_QUIZ_ALREADY_ENDED")

	// ErrNoMoreQuestions is returned when there are no more questions to advance to.
	ErrNoMoreQuestions = errors.New("CLASSROOM_NO_MORE_QUESTIONS")

	// ErrAlreadyAnswered is returned when a participant has already answered the current question.
	ErrAlreadyAnswered = errors.New("CLASSROOM_ALREADY_ANSWERED")

	// ErrAnswerTooLate is returned when the time limit for the current question has expired.
	ErrAnswerTooLate = errors.New("CLASSROOM_ANSWER_TOO_LATE")

	// ErrParticipantNotFound is returned when a quiz participant cannot be found.
	ErrParticipantNotFound = errors.New("CLASSROOM_PARTICIPANT_NOT_FOUND")

	// ErrPollNotFound is returned when a LivePollSession cannot be found.
	ErrPollNotFound = errors.New("CLASSROOM_POLL_NOT_FOUND")

	// ErrPollClosed is returned when attempting to vote on a closed poll.
	ErrPollClosed = errors.New("CLASSROOM_POLL_CLOSED")

	// ErrAlreadyVoted is returned when a learner has already voted on a poll.
	ErrAlreadyVoted = errors.New("CLASSROOM_ALREADY_VOTED")

	// ErrInvalidOption is returned when a selected option index is out of range.
	ErrInvalidOption = errors.New("CLASSROOM_INVALID_OPTION")

	// ErrBoardNotFound is returned when a JamBoard cannot be found.
	ErrBoardNotFound = errors.New("CLASSROOM_BOARD_NOT_FOUND")

	// ErrBoardClosed is returned when attempting to add an entry to a closed board.
	ErrBoardClosed = errors.New("CLASSROOM_BOARD_CLOSED")

	// ErrProfileNotFound is returned when a ClassProfile cannot be found.
	ErrProfileNotFound = errors.New("CLASSROOM_PROFILE_NOT_FOUND")

	// ErrValidationFailed is returned when request validation fails.
	ErrValidationFailed = errors.New("CLASSROOM_VALIDATION_FAILED")

	// ErrInvalidStateTransition is returned when a state transition is not allowed.
	ErrInvalidStateTransition = errors.New("CLASSROOM_INVALID_STATE_TRANSITION")

	// ErrSessionNotFound is returned when a ClassroomSession cannot be found.
	ErrSessionNotFound = errors.New("CLASSROOM_SESSION_NOT_FOUND")

	// ErrSessionNotLive is returned when attempting an action on a session that is not live.
	ErrSessionNotLive = errors.New("CLASSROOM_SESSION_NOT_LIVE")

	// ErrSessionAlreadyLive is returned when attempting to start an already-live session.
	ErrSessionAlreadyLive = errors.New("CLASSROOM_SESSION_ALREADY_LIVE")

	// ErrSessionAlreadyEnded is returned when attempting actions on an ended session.
	ErrSessionAlreadyEnded = errors.New("CLASSROOM_SESSION_ALREADY_ENDED")

	// ErrDiscussionNotFound is returned when a DiscussionThread cannot be found.
	ErrDiscussionNotFound = errors.New("CLASSROOM_DISCUSSION_NOT_FOUND")

	// ErrKioskConfigNotFound is returned when a KioskConfig cannot be found.
	ErrKioskConfigNotFound = errors.New("CLASSROOM_KIOSK_CONFIG_NOT_FOUND")

	// ErrLearnerProfileNotFound is returned when a LearnerClassProfile cannot be found.
	ErrLearnerProfileNotFound = errors.New("CLASSROOM_LEARNER_PROFILE_NOT_FOUND")

	// ErrQuizNotPaused is returned when attempting to resume a quiz that is not paused.
	ErrQuizNotPaused = errors.New("CLASSROOM_QUIZ_NOT_PAUSED")

	// ErrTeamNotFound is returned when a QuizTeam cannot be found.
	ErrTeamNotFound = errors.New("CLASSROOM_TEAM_NOT_FOUND")

	// ErrDuplicateVote is returned when a duplicate vote hash is detected.
	ErrDuplicateVote = errors.New("CLASSROOM_DUPLICATE_VOTE")

	// ErrEmbeddedPollNotFound is returned when an EmbeddedPoll cannot be found.
	ErrEmbeddedPollNotFound = errors.New("CLASSROOM_EMBEDDED_POLL_NOT_FOUND")

	// ErrAnalyticsNotFound is returned when QuizAnalytics cannot be found.
	ErrAnalyticsNotFound = errors.New("CLASSROOM_ANALYTICS_NOT_FOUND")

	// ErrParticipantEliminated is returned when a participant has been eliminated.
	ErrParticipantEliminated = errors.New("CLASSROOM_PARTICIPANT_ELIMINATED")
)
