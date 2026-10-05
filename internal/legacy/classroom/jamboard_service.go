package classroom

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// JamBoardService manages collaborative jamboard lifecycle and entry
// management.
type JamBoardService struct {
	boards  JamBoardRepository
	entries JamBoardEntryRepository
	events  EventPublisher
}

// NewJamBoardService creates a JamBoardService with the given repositories
// and event publisher.
func NewJamBoardService(
	boards JamBoardRepository,
	entries JamBoardEntryRepository,
	events EventPublisher,
) *JamBoardService {
	return &JamBoardService{
		boards:  boards,
		entries: entries,
		events:  events,
	}
}

// CreateJamBoard validates and persists a new JamBoard with UUIDv7 ID and
// open status.
func (s *JamBoardService) CreateJamBoard(ctx context.Context, board *JamBoard) (*JamBoard, error) {
	if board.Title == "" {
		return nil, fmt.Errorf("title must not be empty: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	board.ID = uuid.Must(uuid.NewV7())
	board.Status = BoardStatusOpen
	board.EntryCount = 0
	board.CreatedAt = now
	board.UpdatedAt = now

	if err := s.boards.Create(ctx, board); err != nil {
		return nil, err
	}

	return board, nil
}

// GetJamBoard retrieves a jamboard with all its entries. Returns
// ErrBoardNotFound if the board does not exist.
func (s *JamBoardService) GetJamBoard(ctx context.Context, id, tenantID uuid.UUID) (*JamBoardWithEntries, error) {
	board, err := s.boards.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if board == nil {
		return nil, ErrBoardNotFound
	}

	entries, err := s.entries.ListByBoard(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}

	return &JamBoardWithEntries{
		JamBoard: *board,
		Entries:  entries,
	}, nil
}

// AddEntry validates and persists a new entry to a jamboard. The board must
// be open. Publishes a jamboard.entry.added event.
func (s *JamBoardService) AddEntry(ctx context.Context, boardID, tenantID, gcid uuid.UUID, entry *JamBoardEntry) (*JamBoardEntry, error) {
	board, err := s.boards.GetByID(ctx, boardID, tenantID)
	if err != nil {
		return nil, err
	}
	if board == nil {
		return nil, ErrBoardNotFound
	}

	if board.Status == BoardStatusClosed {
		return nil, ErrBoardClosed
	}

	if !entry.EntryType.IsValid() {
		return nil, fmt.Errorf("invalid entry type %q: %w", entry.EntryType, ErrValidationFailed)
	}
	if entry.Content == "" {
		return nil, fmt.Errorf("content must not be empty: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	entry.ID = uuid.Must(uuid.NewV7())
	entry.BoardID = boardID
	entry.TenantID = tenantID
	entry.CreatedByGCID = gcid
	entry.CreatedAt = now

	if err := s.entries.Create(ctx, entry); err != nil {
		return nil, err
	}

	// Update entry count on the board.
	board.EntryCount++
	board.UpdatedAt = now
	if err := s.boards.Update(ctx, board); err != nil {
		return nil, fmt.Errorf("update board entry count: %w", err)
	}

	// Publish entry added event.
	evt := NewDomainEvent(
		EventJamBoardEntryAdded,
		tenantID,
		&gcid,
		boardID,
		AggregateJamBoard,
		map[string]interface{}{
			"board_id":   boardID.String(),
			"entry_id":   entry.ID.String(),
			"entry_type": string(entry.EntryType),
			"gcid":       gcid.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicClassroomEvents, evt); err != nil {
		return nil, fmt.Errorf("publish jamboard.entry.added event: %w", err)
	}

	return entry, nil
}
