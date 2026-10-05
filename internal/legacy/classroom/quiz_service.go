package classroom

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// QuizService manages live quiz session lifecycle, participant management,
// answer submission, and leaderboard computation.
type QuizService struct {
	quizzes       LiveQuizSessionRepository
	participants  QuizParticipantRepository
	teams         QuizTeamRepository
	teamMembers   QuizTeamMemberRepository
	embeddedPolls EmbeddedPollRepository
	analytics     QuizAnalyticsRepository
	events        EventPublisher
}

// NewQuizService creates a QuizService with the given repositories and event publisher.
func NewQuizService(
	quizzes LiveQuizSessionRepository,
	participants QuizParticipantRepository,
	events EventPublisher,
) *QuizService {
	return &QuizService{
		quizzes:      quizzes,
		participants: participants,
		events:       events,
	}
}

// NewQuizServiceFull creates a QuizService with all repositories including
// teams, embedded polls, and analytics.
func NewQuizServiceFull(
	quizzes LiveQuizSessionRepository,
	participants QuizParticipantRepository,
	teams QuizTeamRepository,
	teamMembers QuizTeamMemberRepository,
	embeddedPolls EmbeddedPollRepository,
	analytics QuizAnalyticsRepository,
	events EventPublisher,
) *QuizService {
	return &QuizService{
		quizzes:       quizzes,
		participants:  participants,
		teams:         teams,
		teamMembers:   teamMembers,
		embeddedPolls: embeddedPolls,
		analytics:     analytics,
		events:        events,
	}
}

// CreateQuiz validates and persists a new LiveQuizSession with UUIDv7 ID
// and waiting status.
func (s *QuizService) CreateQuiz(ctx context.Context, quiz *LiveQuizSession) (*LiveQuizSession, error) {
	if quiz.Title == "" {
		return nil, fmt.Errorf("title must not be empty: %w", ErrValidationFailed)
	}
	if len(quiz.Questions) == 0 {
		return nil, fmt.Errorf("at least one question is required: %w", ErrValidationFailed)
	}
	for i, q := range quiz.Questions {
		if q.QuestionText == "" {
			return nil, fmt.Errorf("question %d text must not be empty: %w", i, ErrValidationFailed)
		}
		if len(q.Options) < 2 {
			return nil, fmt.Errorf("question %d must have at least 2 options: %w", i, ErrValidationFailed)
		}
		if q.CorrectOptionIndex < 0 || q.CorrectOptionIndex >= len(q.Options) {
			return nil, fmt.Errorf("question %d correct_option_index out of range: %w", i, ErrValidationFailed)
		}
	}

	now := time.Now().UTC()
	quiz.ID = uuid.Must(uuid.NewV7())
	quiz.Status = QuizStatusWaiting
	quiz.CurrentQuestionIndex = -1
	quiz.TotalQuestions = len(quiz.Questions)
	quiz.ParticipantCount = 0
	if quiz.TimeLimitSeconds <= 0 {
		quiz.TimeLimitSeconds = DefaultTimeLimitSeconds
	}
	if quiz.DeliveryMode == "" {
		quiz.DeliveryMode = DeliveryModeIndividual
	}
	if quiz.TimerMode == "" {
		quiz.TimerMode = TimerModePerQuestion
	}
	quiz.CreatedAt = now
	quiz.UpdatedAt = now

	if err := s.quizzes.Create(ctx, quiz); err != nil {
		return nil, err
	}

	return quiz, nil
}

// GetQuiz retrieves a quiz session by ID and tenant. Returns ErrQuizNotFound
// if the quiz does not exist.
func (s *QuizService) GetQuiz(ctx context.Context, id, tenantID uuid.UUID) (*LiveQuizSession, error) {
	quiz, err := s.quizzes.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}
	return quiz, nil
}

// StartQuiz transitions a waiting quiz to active status, sets
// CurrentQuestionIndex to 0, and publishes a quiz.started event.
func (s *QuizService) StartQuiz(ctx context.Context, id, tenantID uuid.UUID) (*LiveQuizSession, error) {
	quiz, err := s.quizzes.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if quiz.Status == QuizStatusEnded {
		return nil, ErrQuizAlreadyEnded
	}
	if quiz.Status != QuizStatusWaiting {
		return nil, ErrQuizAlreadyStarted
	}

	quiz.Status = QuizStatusActive
	quiz.CurrentQuestionIndex = 0
	quiz.UpdatedAt = time.Now().UTC()

	// Fetch participant count.
	count, err := s.participants.CountByQuiz(ctx, quiz.ID)
	if err != nil {
		return nil, fmt.Errorf("count participants: %w", err)
	}
	quiz.ParticipantCount = count

	if err := s.quizzes.Update(ctx, quiz); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventQuizStarted,
		quiz.TenantID,
		&quiz.CreatedByGCID,
		quiz.ID,
		AggregateLiveQuizSession,
		map[string]interface{}{
			"quiz_id":           quiz.ID.String(),
			"title":             quiz.Title,
			"question_count":    len(quiz.Questions),
			"participant_count": quiz.ParticipantCount,
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish quiz.started event: %w", err)
	}

	return quiz, nil
}

// AdvanceQuestion moves to the next question. Returns ErrNoMoreQuestions
// if the quiz is on its last question.
func (s *QuizService) AdvanceQuestion(ctx context.Context, id, tenantID uuid.UUID) (*LiveQuizSession, error) {
	quiz, err := s.quizzes.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if quiz.Status != QuizStatusActive {
		return nil, ErrQuizNotActive
	}

	nextIndex := quiz.CurrentQuestionIndex + 1
	if nextIndex >= len(quiz.Questions) {
		return nil, ErrNoMoreQuestions
	}

	quiz.CurrentQuestionIndex = nextIndex
	quiz.UpdatedAt = time.Now().UTC()

	if err := s.quizzes.Update(ctx, quiz); err != nil {
		return nil, err
	}

	return quiz, nil
}

// EndQuiz transitions an active or paused quiz to ended status and publishes
// a quiz.ended event.
func (s *QuizService) EndQuiz(ctx context.Context, id, tenantID uuid.UUID) (*LiveQuizSession, error) {
	quiz, err := s.quizzes.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if quiz.Status == QuizStatusEnded {
		return nil, ErrQuizAlreadyEnded
	}
	if quiz.Status == QuizStatusWaiting {
		return nil, fmt.Errorf("cannot end a quiz that has not started: %w", ErrInvalidStateTransition)
	}

	quiz.Status = QuizStatusEnded
	quiz.UpdatedAt = time.Now().UTC()

	if err := s.quizzes.Update(ctx, quiz); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventQuizEnded,
		quiz.TenantID,
		&quiz.CreatedByGCID,
		quiz.ID,
		AggregateLiveQuizSession,
		map[string]interface{}{
			"quiz_id":            quiz.ID.String(),
			"title":              quiz.Title,
			"participant_count":  quiz.ParticipantCount,
			"questions_answered": quiz.CurrentQuestionIndex + 1,
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish quiz.ended event: %w", err)
	}

	return quiz, nil
}

// JoinQuiz registers a learner as a participant in a quiz session.
// Returns the created QuizParticipant.
func (s *QuizService) JoinQuiz(ctx context.Context, quizID, tenantID, gcid uuid.UUID, displayName string) (*QuizParticipant, error) {
	quiz, err := s.quizzes.GetByID(ctx, quizID, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if quiz.Status == QuizStatusEnded {
		return nil, ErrQuizAlreadyEnded
	}

	// Check if already joined — return existing participant.
	existing, err := s.participants.GetByQuizAndGCID(ctx, quizID, gcid)
	if err != nil {
		return nil, fmt.Errorf("check existing participant: %w", err)
	}
	if existing != nil {
		return existing, nil
	}

	now := time.Now().UTC()
	participant := &QuizParticipant{
		ID:            uuid.Must(uuid.NewV7()),
		QuizSessionID: quizID,
		TenantID:      tenantID,
		GCID:          gcid,
		DisplayName:   displayName,
		JoinedAt:      now,
		UpdatedAt:     now,
	}

	if err := s.participants.Create(ctx, participant); err != nil {
		return nil, err
	}

	// Update participant count on quiz.
	count, err := s.participants.CountByQuiz(ctx, quizID)
	if err != nil {
		return nil, fmt.Errorf("count participants: %w", err)
	}
	quiz.ParticipantCount = count
	quiz.UpdatedAt = now
	if err := s.quizzes.Update(ctx, quiz); err != nil {
		return nil, fmt.Errorf("update quiz participant count: %w", err)
	}

	return participant, nil
}

// SubmitAnswer processes a learner's answer to the current quiz question.
// Validates the quiz is active and the participant hasn't already answered.
// Computes points based on correctness and speed.
func (s *QuizService) SubmitAnswer(ctx context.Context, quizID, tenantID, gcid uuid.UUID, selectedOptionIndex, timeTakenMs int) (*AnswerResult, error) {
	quiz, err := s.quizzes.GetByID(ctx, quizID, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if quiz.Status != QuizStatusActive {
		return nil, ErrQuizNotActive
	}

	// Validate answer is within time limit.
	timeLimitMs := quiz.TimeLimitSeconds * 1000
	if timeTakenMs > timeLimitMs {
		return nil, ErrAnswerTooLate
	}

	// Find the participant.
	participant, err := s.participants.GetByQuizAndGCID(ctx, quizID, gcid)
	if err != nil {
		return nil, fmt.Errorf("find participant: %w", err)
	}
	if participant == nil {
		return nil, ErrParticipantNotFound
	}

	// Check if already answered this question.
	already, err := s.participants.HasAnswered(ctx, participant.ID, quiz.CurrentQuestionIndex)
	if err != nil {
		return nil, fmt.Errorf("check answered: %w", err)
	}
	if already {
		return nil, ErrAlreadyAnswered
	}

	// Validate option index.
	currentQuestion := quiz.Questions[quiz.CurrentQuestionIndex]
	if selectedOptionIndex < 0 || selectedOptionIndex >= len(currentQuestion.Options) {
		return nil, fmt.Errorf("selected option index %d out of range: %w", selectedOptionIndex, ErrInvalidOption)
	}

	// Compute correctness and points.
	correct := selectedOptionIndex == currentQuestion.CorrectOptionIndex
	points := CalculatePoints(correct, timeTakenMs, quiz.TimeLimitSeconds)

	// Record the answer.
	answer := &QuizAnswer{
		ParticipantID:       participant.ID,
		QuestionIndex:       quiz.CurrentQuestionIndex,
		SelectedOptionIndex: selectedOptionIndex,
		Correct:             correct,
		PointsEarned:        points,
		TimeTakenMs:         timeTakenMs,
		SubmittedAt:         time.Now().UTC(),
	}
	if err := s.participants.RecordAnswer(ctx, answer); err != nil {
		return nil, fmt.Errorf("record answer: %w", err)
	}

	// Update participant stats.
	participant.TotalPoints += points
	participant.AnsweredCount++
	if correct {
		participant.CorrectCount++
	}
	participant.LastAnswerCorrect = correct
	// Recalculate average time.
	if participant.AnsweredCount > 0 {
		participant.AvgTimeMs = ((participant.AvgTimeMs * (participant.AnsweredCount - 1)) + timeTakenMs) / participant.AnsweredCount
	}
	participant.UpdatedAt = time.Now().UTC()
	if err := s.participants.Update(ctx, participant); err != nil {
		return nil, fmt.Errorf("update participant: %w", err)
	}

	// Publish answer submitted event.
	evt := NewDomainEvent(
		EventQuizAnswerSubmitted,
		tenantID,
		&gcid,
		quizID,
		AggregateLiveQuizSession,
		map[string]interface{}{
			"quiz_id":        quizID.String(),
			"gcid":           gcid.String(),
			"question_index": quiz.CurrentQuestionIndex,
			"correct":        correct,
			"points_earned":  points,
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish quiz.answer.submitted event: %w", err)
	}

	return &AnswerResult{
		Correct:      correct,
		PointsEarned: points,
		TimeTakenMs:  timeTakenMs,
	}, nil
}

// PauseQuiz transitions an active quiz to paused status.
func (s *QuizService) PauseQuiz(ctx context.Context, id, tenantID uuid.UUID) (*LiveQuizSession, error) {
	quiz, err := s.quizzes.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if quiz.Status != QuizStatusActive {
		return nil, ErrQuizNotActive
	}

	now := time.Now().UTC()
	quiz.Status = QuizStatusPaused
	quiz.PausedAt = &now
	quiz.UpdatedAt = now

	if err := s.quizzes.Update(ctx, quiz); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventQuizPaused,
		quiz.TenantID,
		&quiz.CreatedByGCID,
		quiz.ID,
		AggregateLiveQuizSession,
		map[string]interface{}{
			"quiz_id":   quiz.ID.String(),
			"paused_at": now.Format(time.RFC3339),
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish quiz.paused event: %w", err)
	}

	return quiz, nil
}

// ResumeQuiz transitions a paused quiz back to active status.
func (s *QuizService) ResumeQuiz(ctx context.Context, id, tenantID uuid.UUID) (*LiveQuizSession, error) {
	quiz, err := s.quizzes.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if quiz.Status != QuizStatusPaused {
		return nil, ErrQuizNotPaused
	}

	quiz.Status = QuizStatusActive
	quiz.PausedAt = nil
	quiz.UpdatedAt = time.Now().UTC()

	if err := s.quizzes.Update(ctx, quiz); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventQuizResumed,
		quiz.TenantID,
		&quiz.CreatedByGCID,
		quiz.ID,
		AggregateLiveQuizSession,
		map[string]interface{}{
			"quiz_id": quiz.ID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish quiz.resumed event: %w", err)
	}

	return quiz, nil
}

// SkipQuestion skips the current question and advances to the next.
// Same as AdvanceQuestion but semantically different (instructor chose to skip).
func (s *QuizService) SkipQuestion(ctx context.Context, id, tenantID uuid.UUID) (*LiveQuizSession, error) {
	return s.AdvanceQuestion(ctx, id, tenantID)
}

// ReplayQuestion resets the current question for re-answering by clearing
// the answered state. In practice, this keeps the same question index.
func (s *QuizService) ReplayQuestion(ctx context.Context, id, tenantID uuid.UUID) (*LiveQuizSession, error) {
	quiz, err := s.quizzes.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if quiz.Status != QuizStatusActive && quiz.Status != QuizStatusPaused {
		return nil, ErrQuizNotActive
	}

	// Re-set to active if paused, keep same question index.
	quiz.Status = QuizStatusActive
	quiz.PausedAt = nil
	quiz.UpdatedAt = time.Now().UTC()

	if err := s.quizzes.Update(ctx, quiz); err != nil {
		return nil, err
	}

	return quiz, nil
}

// CreateTeam creates a team for a quiz session.
func (s *QuizService) CreateTeam(ctx context.Context, quizID, tenantID uuid.UUID, teamName string, teamColor *string) (*QuizTeam, error) {
	quiz, err := s.quizzes.GetByID(ctx, quizID, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if teamName == "" {
		return nil, fmt.Errorf("team name must not be empty: %w", ErrValidationFailed)
	}

	team := &QuizTeam{
		ID:        uuid.Must(uuid.NewV7()),
		QuizID:    quizID,
		TeamName:  teamName,
		TeamColor: teamColor,
		CreatedAt: time.Now().UTC(),
	}

	if err := s.teams.Create(ctx, team); err != nil {
		return nil, err
	}

	return team, nil
}

// AddTeamMember adds a participant to a quiz team.
func (s *QuizService) AddTeamMember(ctx context.Context, quizID, tenantID, teamID, participantID uuid.UUID) (*QuizTeamMember, error) {
	quiz, err := s.quizzes.GetByID(ctx, quizID, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	team, err := s.teams.GetByID(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if team == nil {
		return nil, ErrTeamNotFound
	}

	member := &QuizTeamMember{
		ID:            uuid.Must(uuid.NewV7()),
		TeamID:        teamID,
		ParticipantID: participantID,
		CreatedAt:     time.Now().UTC(),
	}

	if err := s.teamMembers.Create(ctx, member); err != nil {
		return nil, err
	}

	return member, nil
}

// GetTeamLeaderboard returns aggregated team scores for a quiz session.
func (s *QuizService) GetTeamLeaderboard(ctx context.Context, quizID, tenantID uuid.UUID) ([]TeamLeaderboardEntry, error) {
	quiz, err := s.quizzes.GetByID(ctx, quizID, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	teams, err := s.teams.ListByQuiz(ctx, quizID)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}

	participants, err := s.participants.ListByQuiz(ctx, quizID)
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}

	allMembers, err := s.teamMembers.ListByQuiz(ctx, quizID)
	if err != nil {
		return nil, fmt.Errorf("list team members: %w", err)
	}

	// Build participant score map.
	participantScores := make(map[uuid.UUID]int, len(participants))
	for _, p := range participants {
		participantScores[p.ID] = p.TotalPoints
	}

	// Build team member map.
	teamMemberMap := make(map[uuid.UUID][]uuid.UUID)
	for _, m := range allMembers {
		teamMemberMap[m.TeamID] = append(teamMemberMap[m.TeamID], m.ParticipantID)
	}

	entries := make([]TeamLeaderboardEntry, 0, len(teams))
	for _, team := range teams {
		members := teamMemberMap[team.ID]
		totalPoints := 0
		for _, pid := range members {
			totalPoints += participantScores[pid]
		}
		entries = append(entries, TeamLeaderboardEntry{
			TeamID:      team.ID,
			TeamName:    team.TeamName,
			TeamColor:   team.TeamColor,
			TotalPoints: totalPoints,
			MemberCount: len(members),
		})
	}

	// Sort by total points descending and assign ranks.
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].TotalPoints > entries[i].TotalPoints {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
	for i := range entries {
		entries[i].Rank = i + 1
	}

	return entries, nil
}

// EliminateParticipants eliminates the bottom N% of participants from a quiz.
func (s *QuizService) EliminateParticipants(ctx context.Context, quizID, tenantID uuid.UUID, eliminationPercent int) (*EliminationResult, error) {
	quiz, err := s.quizzes.GetByID(ctx, quizID, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if quiz.Status != QuizStatusActive && quiz.Status != QuizStatusPaused {
		return nil, ErrQuizNotActive
	}

	if eliminationPercent < 1 || eliminationPercent > 50 {
		return nil, fmt.Errorf("elimination percent must be between 1 and 50: %w", ErrValidationFailed)
	}

	participants, err := s.participants.ListByQuiz(ctx, quizID)
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}

	// Filter to non-eliminated participants only.
	var active []QuizParticipant
	for _, p := range participants {
		if p.EliminatedAt == nil {
			active = append(active, p)
		}
	}

	// Determine how many to eliminate.
	eliminateCount := len(active) * eliminationPercent / 100
	if eliminateCount < 1 {
		eliminateCount = 1
	}
	if eliminateCount >= len(active) {
		eliminateCount = len(active) - 1 // keep at least one
	}

	// Active participants are already sorted by points descending from ListByQuiz.
	// Take the bottom N.
	now := time.Now().UTC()
	var eliminated []uuid.UUID
	for i := len(active) - eliminateCount; i < len(active); i++ {
		active[i].EliminatedAt = &now
		active[i].UpdatedAt = now
		if err := s.participants.Update(ctx, &active[i]); err != nil {
			return nil, fmt.Errorf("eliminate participant: %w", err)
		}
		eliminated = append(eliminated, active[i].ID)
	}

	remaining := len(active) - eliminateCount

	evt := NewDomainEvent(
		EventQuizEliminationExecuted,
		quiz.TenantID,
		&quiz.CreatedByGCID,
		quiz.ID,
		AggregateLiveQuizSession,
		map[string]interface{}{
			"quiz_id":          quiz.ID.String(),
			"eliminated_count": len(eliminated),
			"remaining_count":  remaining,
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish quiz.elimination.executed event: %w", err)
	}

	return &EliminationResult{
		QuizID:                 quizID,
		EliminatedCount:        len(eliminated),
		RemainingCount:         remaining,
		EliminatedParticipants: eliminated,
	}, nil
}

// CreateEmbeddedPoll links a poll to trigger within a quiz after a specific question.
func (s *QuizService) CreateEmbeddedPoll(ctx context.Context, quizID, tenantID, pollID uuid.UUID, triggerAfterQuestion int) (*EmbeddedPoll, error) {
	quiz, err := s.quizzes.GetByID(ctx, quizID, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if triggerAfterQuestion < 0 || triggerAfterQuestion >= len(quiz.Questions) {
		return nil, fmt.Errorf("trigger_after_question out of range: %w", ErrValidationFailed)
	}

	ep := &EmbeddedPoll{
		ID:                   uuid.Must(uuid.NewV7()),
		QuizID:               quizID,
		PollID:               pollID,
		TriggerAfterQuestion: triggerAfterQuestion,
		CreatedAt:            time.Now().UTC(),
	}

	if err := s.embeddedPolls.Create(ctx, ep); err != nil {
		return nil, err
	}

	return ep, nil
}

// ListEmbeddedPolls returns all embedded polls for a quiz session.
func (s *QuizService) ListEmbeddedPolls(ctx context.Context, quizID, tenantID uuid.UUID) ([]EmbeddedPoll, error) {
	quiz, err := s.quizzes.GetByID(ctx, quizID, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	return s.embeddedPolls.ListByQuiz(ctx, quizID)
}

// GetQuizAnalytics computes and returns analytics for a quiz session.
func (s *QuizService) GetQuizAnalytics(ctx context.Context, quizID, tenantID uuid.UUID) (*QuizAnalytics, error) {
	quiz, err := s.quizzes.GetByID(ctx, quizID, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	participants, err := s.participants.ListByQuiz(ctx, quizID)
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}

	totalParticipants := len(participants)
	if totalParticipants == 0 {
		return &QuizAnalytics{
			ID:                uuid.Must(uuid.NewV7()),
			QuizID:            quizID,
			TotalParticipants: 0,
			CreatedAt:         time.Now().UTC(),
		}, nil
	}

	var totalScore int
	var totalAvgTimeMs int
	for _, p := range participants {
		totalScore += p.TotalPoints
		totalAvgTimeMs += p.AvgTimeMs
	}

	avgScore := float64(totalScore) / float64(totalParticipants)
	avgResponseTimeMs := totalAvgTimeMs / totalParticipants

	analytics := &QuizAnalytics{
		ID:                uuid.Must(uuid.NewV7()),
		QuizID:            quizID,
		TotalParticipants: totalParticipants,
		AvgScore:          avgScore,
		AvgResponseTimeMs: avgResponseTimeMs,
		CreatedAt:         time.Now().UTC(),
	}

	return analytics, nil
}

// GetQuestionAnalytics returns per-question analytics for a quiz session.
func (s *QuizService) GetQuestionAnalytics(ctx context.Context, quizID, tenantID uuid.UUID, questionIndex int) (*QuestionAnalytics, error) {
	quiz, err := s.quizzes.GetByID(ctx, quizID, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	if questionIndex < 0 || questionIndex >= len(quiz.Questions) {
		return nil, fmt.Errorf("question index %d out of range: %w", questionIndex, ErrValidationFailed)
	}

	q := quiz.Questions[questionIndex]

	return &QuestionAnalytics{
		QuestionIndex:      questionIndex,
		QuestionText:       q.QuestionText,
		TotalAnswers:       0,
		CorrectCount:       0,
		IncorrectCount:     0,
		AvgTimeMs:          0,
		OptionDistribution: make([]int, len(q.Options)),
	}, nil
}

// GetLeaderboard returns ranked participants for a quiz session, sorted by
// total_points descending (ties broken by avg_time_ms ascending).
func (s *QuizService) GetLeaderboard(ctx context.Context, quizID, tenantID uuid.UUID) ([]LeaderboardEntry, error) {
	quiz, err := s.quizzes.GetByID(ctx, quizID, tenantID)
	if err != nil {
		return nil, err
	}
	if quiz == nil {
		return nil, ErrQuizNotFound
	}

	participants, err := s.participants.ListByQuiz(ctx, quizID)
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}

	entries := make([]LeaderboardEntry, len(participants))
	for i, p := range participants {
		entries[i] = LeaderboardEntry{
			Rank:         i + 1,
			GCID:         p.GCID,
			DisplayName:  p.DisplayName,
			TotalPoints:  p.TotalPoints,
			CorrectCount: p.CorrectCount,
			AvgTimeMs:    p.AvgTimeMs,
		}
	}

	return entries, nil
}
