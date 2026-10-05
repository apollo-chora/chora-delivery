package classroom

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// testJamBoardDeps holds all mocks wired into a JamBoardService.
type testJamBoardDeps struct {
	svc       *JamBoardService
	boardR    *mockBoardRepo
	entryR    *mockEntryRepo
	publisher *mockEventPublisher
}

// newTestJamBoardService creates a JamBoardService with fresh mocks.
func newTestJamBoardService() testJamBoardDeps {
	br := &mockBoardRepo{}
	er := &mockEntryRepo{}
	ep := &mockEventPublisher{}
	return testJamBoardDeps{
		svc:       NewJamBoardService(br, er, ep),
		boardR:    br,
		entryR:    er,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// TestCreateJamBoard
// ---------------------------------------------------------------------------

func TestCreateJamBoard(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *JamBoard
		setupMocks   func(d testJamBoardDeps)
		wantErr      error
		assertResult func(t *testing.T, got *JamBoard)
	}{
		{
			name: "success: creates board with UUIDv7 and open status",
			input: &JamBoard{
				TenantID:      tenantID,
				Title:         "Brainstorm Board",
				Description:   "Let's brainstorm!",
				CreatedByGCID: gcid,
			},
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.JamBoard")).Return(nil)
			},
			assertResult: func(t *testing.T, got *JamBoard) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, BoardStatusOpen, got.Status)
				assert.Equal(t, 0, got.EntryCount)
				assert.Equal(t, "Brainstorm Board", got.Title)
			},
		},
		{
			name: "fails: empty title returns ErrValidationFailed",
			input: &JamBoard{
				TenantID:      tenantID,
				Title:         "",
				CreatedByGCID: gcid,
			},
			setupMocks: func(d testJamBoardDeps) {},
			wantErr:    ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestJamBoardService()
			tc.setupMocks(d)

			got, err := d.svc.CreateJamBoard(context.Background(), tc.input)

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

			d.boardR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAddEntry
// ---------------------------------------------------------------------------

func TestAddEntry(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	boardID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	// AddEntry writes EntryCount and UpdatedAt THROUGH the board pointer the
	// repo hands back, so a fixture shared across t.Parallel() sub-tests is
	// written by one while another reads it (directly, or inside testify's
	// Called argument capture). setupMocks runs once per sub-test, in that
	// sub-test's goroutine, so building here gives each its own board.
	newOpenBoard := func() *JamBoard {
		return &JamBoard{
			ID:         boardID,
			TenantID:   tenantID,
			Title:      "Open Board",
			Status:     BoardStatusOpen,
			EntryCount: 0,
		}
	}

	tests := []struct {
		name         string
		entry        *JamBoardEntry
		setupMocks   func(d testJamBoardDeps)
		wantErr      error
		assertResult func(t *testing.T, got *JamBoardEntry)
	}{
		{
			name: "success: adds sticky note entry",
			entry: &JamBoardEntry{
				EntryType: EntryTypeStickyNote,
				Content:   "Great idea!",
			},
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(newOpenBoard(), nil)
				d.entryR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.JamBoardEntry")).Return(nil)
				d.boardR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.JamBoard")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *JamBoardEntry) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, boardID, got.BoardID)
				assert.Equal(t, gcid, got.CreatedByGCID)
				assert.Equal(t, EntryTypeStickyNote, got.EntryType)
				assert.Equal(t, "Great idea!", got.Content)
			},
		},
		{
			name: "success: adds link entry",
			entry: &JamBoardEntry{
				EntryType: EntryTypeLink,
				Content:   "https://example.com",
			},
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(newOpenBoard(), nil)
				d.entryR.On("Create", mock.Anything, mock.AnythingOfType("*classroom.JamBoardEntry")).Return(nil)
				d.boardR.On("Update", mock.Anything, mock.AnythingOfType("*classroom.JamBoard")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicClassroomEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *JamBoardEntry) {
				assert.Equal(t, EntryTypeLink, got.EntryType)
			},
		},
		{
			name: "fails: board not found",
			entry: &JamBoardEntry{
				EntryType: EntryTypeStickyNote,
				Content:   "test",
			},
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(nil, nil)
			},
			wantErr: ErrBoardNotFound,
		},
		{
			name: "fails: board closed",
			entry: &JamBoardEntry{
				EntryType: EntryTypeStickyNote,
				Content:   "test",
			},
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(&JamBoard{
					ID:       boardID,
					TenantID: tenantID,
					Status:   BoardStatusClosed,
				}, nil)
			},
			wantErr: ErrBoardClosed,
		},
		{
			name: "fails: invalid entry type",
			entry: &JamBoardEntry{
				EntryType: EntryType("hologram"),
				Content:   "test",
			},
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(newOpenBoard(), nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name: "fails: empty content",
			entry: &JamBoardEntry{
				EntryType: EntryTypeStickyNote,
				Content:   "",
			},
			setupMocks: func(d testJamBoardDeps) {
				d.boardR.On("GetByID", mock.Anything, boardID, tenantID).Return(newOpenBoard(), nil)
			},
			wantErr: ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestJamBoardService()
			tc.setupMocks(d)

			got, err := d.svc.AddEntry(context.Background(), boardID, tenantID, gcid, tc.entry)

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

			d.boardR.AssertExpectations(t)
			d.entryR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}
