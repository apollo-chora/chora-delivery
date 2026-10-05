package classroom

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PollService manages live poll session lifecycle, voting, and result
// aggregation.
type PollService struct {
	polls         LivePollSessionRepository
	votes         PollVoteRepository
	pollQuestions LivePollQuestionRepository
	pollVotes     LivePollVoteRepository
	events        EventPublisher
}

// NewPollService creates a PollService with the given repositories and event
// publisher.
func NewPollService(
	polls LivePollSessionRepository,
	votes PollVoteRepository,
	events EventPublisher,
) *PollService {
	return &PollService{
		polls:  polls,
		votes:  votes,
		events: events,
	}
}

// NewPollServiceFull creates a PollService with all repositories including
// multi-question poll support.
func NewPollServiceFull(
	polls LivePollSessionRepository,
	votes PollVoteRepository,
	pollQuestions LivePollQuestionRepository,
	pollVotes LivePollVoteRepository,
	events EventPublisher,
) *PollService {
	return &PollService{
		polls:         polls,
		votes:         votes,
		pollQuestions: pollQuestions,
		pollVotes:     pollVotes,
		events:        events,
	}
}

// CreatePoll validates and persists a new LivePollSession with UUIDv7 ID
// and open status. Publishes a poll.created event.
func (s *PollService) CreatePoll(ctx context.Context, poll *LivePollSession) (*LivePollSession, error) {
	if poll.Question == "" {
		return nil, fmt.Errorf("question must not be empty: %w", ErrValidationFailed)
	}
	if len(poll.Options) < 2 {
		return nil, fmt.Errorf("at least 2 options are required: %w", ErrValidationFailed)
	}
	if !poll.PollType.IsValid() {
		return nil, fmt.Errorf("invalid poll type %q: %w", poll.PollType, ErrValidationFailed)
	}

	now := time.Now().UTC()
	poll.ID = uuid.Must(uuid.NewV7())
	poll.Status = PollStatusOpen
	poll.VoteCount = 0
	poll.CreatedAt = now
	poll.UpdatedAt = now

	if err := s.polls.Create(ctx, poll); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventPollCreated,
		poll.TenantID,
		&poll.CreatedByGCID,
		poll.ID,
		AggregateLivePollSession,
		map[string]interface{}{
			"poll_id":      poll.ID.String(),
			"question":     poll.Question,
			"option_count": len(poll.Options),
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish poll.created event: %w", err)
	}

	return poll, nil
}

// CastVote records a vote on a poll. Validates the poll is open, the learner
// hasn't already voted, and the selected options are valid. Publishes a
// poll.vote.cast event.
func (s *PollService) CastVote(ctx context.Context, pollID, tenantID, gcid uuid.UUID, selectedOptionIndices []int) error {
	poll, err := s.polls.GetByID(ctx, pollID, tenantID)
	if err != nil {
		return err
	}
	if poll == nil {
		return ErrPollNotFound
	}

	if poll.Status == PollStatusClosed {
		return ErrPollClosed
	}

	// Check if already voted.
	voted, err := s.votes.HasVoted(ctx, pollID, gcid)
	if err != nil {
		return fmt.Errorf("check voted: %w", err)
	}
	if voted {
		return ErrAlreadyVoted
	}

	// Validate option indices.
	for _, idx := range selectedOptionIndices {
		if idx < 0 || idx >= len(poll.Options) {
			return fmt.Errorf("option index %d out of range: %w", idx, ErrInvalidOption)
		}
	}

	// For single_choice, enforce exactly one selection.
	if poll.PollType == PollTypeSingleChoice && len(selectedOptionIndices) != 1 {
		return fmt.Errorf("single_choice poll requires exactly one selection: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	vote := &PollVote{
		ID:                    uuid.Must(uuid.NewV7()),
		PollSessionID:         pollID,
		TenantID:              tenantID,
		GCID:                  gcid,
		SelectedOptionIndices: selectedOptionIndices,
		CastAt:                now,
	}

	if err := s.votes.Create(ctx, vote); err != nil {
		return err
	}

	// Update vote count.
	count, err := s.votes.CountByPoll(ctx, pollID)
	if err != nil {
		return fmt.Errorf("count votes: %w", err)
	}
	poll.VoteCount = count
	poll.UpdatedAt = now
	if err := s.polls.Update(ctx, poll); err != nil {
		return fmt.Errorf("update poll vote count: %w", err)
	}

	// Publish vote cast event.
	evt := NewDomainEvent(
		EventPollVoteCast,
		tenantID,
		&gcid,
		pollID,
		AggregateLivePollSession,
		map[string]interface{}{
			"poll_id":          pollID.String(),
			"gcid":             gcid.String(),
			"selected_options": selectedOptionIndices,
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return fmt.Errorf("publish poll.vote.cast event: %w", err)
	}

	return nil
}

// GetPollResults aggregates votes and returns percentage breakdown for each
// option.
func (s *PollService) GetPollResults(ctx context.Context, pollID, tenantID uuid.UUID) (*PollResults, error) {
	poll, err := s.polls.GetByID(ctx, pollID, tenantID)
	if err != nil {
		return nil, err
	}
	if poll == nil {
		return nil, ErrPollNotFound
	}

	votes, err := s.votes.ListByPoll(ctx, pollID)
	if err != nil {
		return nil, fmt.Errorf("list votes: %w", err)
	}

	// Count votes per option.
	optionCounts := make([]int, len(poll.Options))
	for _, v := range votes {
		for _, idx := range v.SelectedOptionIndices {
			if idx >= 0 && idx < len(optionCounts) {
				optionCounts[idx]++
			}
		}
	}

	totalVotes := len(votes)
	results := make([]OptionResult, len(poll.Options))
	for i, option := range poll.Options {
		pct := 0.0
		if totalVotes > 0 {
			pct = float64(optionCounts[i]) * 100.0 / float64(totalVotes)
		}
		results[i] = OptionResult{
			OptionText: option,
			VoteCount:  optionCounts[i],
			Percentage: pct,
		}
	}

	return &PollResults{
		PollID:        pollID,
		Question:      poll.Question,
		TotalVotes:    totalVotes,
		OptionResults: results,
	}, nil
}

// GetPollVisualization returns aggregated poll results formatted for chart rendering.
func (s *PollService) GetPollVisualization(ctx context.Context, pollID, tenantID uuid.UUID) (*PollVisualization, error) {
	poll, err := s.polls.GetByID(ctx, pollID, tenantID)
	if err != nil {
		return nil, err
	}
	if poll == nil {
		return nil, ErrPollNotFound
	}

	votes, err := s.votes.ListByPoll(ctx, pollID)
	if err != nil {
		return nil, fmt.Errorf("list votes: %w", err)
	}

	totalVotes := len(votes)

	// Determine chart type based on poll type.
	var chartType string
	switch poll.PollType {
	case PollTypeWordCloud:
		chartType = "word_cloud"
	case PollTypeMultipleChoice:
		chartType = "bar"
	default:
		chartType = "pie"
	}

	// Count votes per option.
	optionCounts := make([]int, len(poll.Options))
	for _, v := range votes {
		for _, idx := range v.SelectedOptionIndices {
			if idx >= 0 && idx < len(optionCounts) {
				optionCounts[idx]++
			}
		}
	}

	dataPoints := make([]VisualizationDataPoint, len(poll.Options))
	for i, option := range poll.Options {
		pct := 0.0
		if totalVotes > 0 {
			pct = float64(optionCounts[i]) * 100.0 / float64(totalVotes)
		}
		dataPoints[i] = VisualizationDataPoint{
			Label:      option,
			Value:      float64(optionCounts[i]),
			Percentage: pct,
		}
	}

	return &PollVisualization{
		PollID:     pollID,
		ChartType:  chartType,
		DataPoints: dataPoints,
		TotalVotes: totalVotes,
	}, nil
}

// CastAnonymousVote records a vote with deduplication via vote hash.
func (s *PollService) CastAnonymousVote(ctx context.Context, pollID, tenantID uuid.UUID, questionID uuid.UUID, selectedOptions map[string]interface{}, voteHash string) (*LivePollVote, error) {
	poll, err := s.polls.GetByID(ctx, pollID, tenantID)
	if err != nil {
		return nil, err
	}
	if poll == nil {
		return nil, ErrPollNotFound
	}

	if poll.Status == PollStatusClosed {
		return nil, ErrPollClosed
	}

	// Check for duplicate vote hash.
	if voteHash != "" && s.pollVotes != nil {
		exists, err := s.pollVotes.ExistsByHash(ctx, voteHash)
		if err != nil {
			return nil, fmt.Errorf("check vote hash: %w", err)
		}
		if exists {
			return nil, ErrDuplicateVote
		}
	}

	vote := &LivePollVote{
		ID:              uuid.Must(uuid.NewV7()),
		QuestionID:      questionID,
		SelectedOptions: selectedOptions,
		Anonymous:       true,
		VoteHash:        &voteHash,
		CreatedAt:       time.Now().UTC(),
	}

	if s.pollVotes != nil {
		if err := s.pollVotes.Create(ctx, vote); err != nil {
			return nil, err
		}
	}

	return vote, nil
}
