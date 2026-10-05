// Package signage_state owns the real-time projector / TV signage state
// machine for the Classroom Experience inside chora-delivery.
//
// Per CHO-13 + docs/design/ux_classroom_experience.md "DisplaySignage" step
// the projector cycles between PreStart (showing the join code), Atom
// (currently teaching), MidQuiz (live response distribution), JamBoard
// (collaborative spotlight), Podium (post-session results), and Closed.
//
// State transitions:
//
//	PreStart ──► Atom ◄─────► MidQuiz
//	   │            │            │
//	   │            ▼            ▼
//	   │         JamBoard  ◄────┘
//	   ▼            │
//	 Podium ◄──────┘
//	    │
//	    ▼
//	 Closed (terminal)
//
// All non-terminal modes can transition into any other non-terminal mode,
// so the rules collapse to: only Closed is terminal. Each transition bumps
// Version so WebSocket subscribers can detect missed updates after a
// reconnect (per the "projector display disconnects" edge case).
//
// Hexagonal: pure domain. No HTTP, no persistence, no WS.
package signage_state

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	ErrInvalidArgument   = errors.New("invalid argument")
	ErrInvalidTransition = errors.New("invalid state transition")
)

// -----------------------------------------------------------------------------
// Mode enum
// -----------------------------------------------------------------------------

// Mode is the signage display mode.
type Mode string

const (
	// ModePreStart — projector shows join code + QR + URL.
	ModePreStart Mode = "pre-start"
	// ModeAtom — projector shows current teaching atom.
	ModeAtom Mode = "atom"
	// ModeMidQuiz — projector shows live mid-class quiz.
	ModeMidQuiz Mode = "mid-quiz"
	// ModeJamBoard — projector spotlights a collaborative board.
	ModeJamBoard Mode = "jamboard"
	// ModePodium — projector shows post-session leaderboard / results.
	ModePodium Mode = "podium"
	// ModeClosed — terminal; projector idle.
	ModeClosed Mode = "closed"
)

// -----------------------------------------------------------------------------
// Snapshot — wire format for WS broadcast
// -----------------------------------------------------------------------------

// Snapshot is the full signage state in a passive value type, suitable for
// WebSocket JSON serialization.
type Snapshot struct {
	ID             string
	TenantID       string
	ClassID        string
	JoinURL        string
	Mode           Mode
	CurrentAtomID  string
	CurrentTitle   string
	CurrentQuizID  string
	CurrentBoardID string
	Version        int64
	UpdatedAt      time.Time
}

// -----------------------------------------------------------------------------
// Signage aggregate root
// -----------------------------------------------------------------------------

// Signage is the projector / TV signage aggregate, keyed by (tenant, class).
type Signage struct {
	ID             string
	TenantID       string
	ClassID        string
	JoinURL        string
	Mode           Mode
	CurrentAtomID  string
	CurrentTitle   string
	CurrentQuizID  string
	CurrentBoardID string
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time

	mu sync.Mutex
}

// NewSignage constructs a Signage in ModePreStart.
func NewSignage(tenantID, classID, joinURL string) (*Signage, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(classID) == "" {
		return nil, fmt.Errorf("%w: class_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(joinURL) == "" {
		return nil, fmt.Errorf("%w: join_url required", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &Signage{
		ID:        NewUUIDv7(),
		TenantID:  tenantID,
		ClassID:   classID,
		JoinURL:   joinURL,
		Mode:      ModePreStart,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// guardNotClosed returns ErrInvalidTransition if Mode == ModeClosed.
func (s *Signage) guardNotClosed(op string) error {
	if s.Mode == ModeClosed {
		return fmt.Errorf("%w: %s after Close not permitted", ErrInvalidTransition, op)
	}
	return nil
}

// ShowAtom transitions to ModeAtom and stores the atom + title.
func (s *Signage) ShowAtom(atomID, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guardNotClosed("ShowAtom"); err != nil {
		return err
	}
	if strings.TrimSpace(atomID) == "" {
		return fmt.Errorf("%w: atom_id required", ErrInvalidArgument)
	}
	s.Mode = ModeAtom
	s.CurrentAtomID = atomID
	s.CurrentTitle = title
	s.CurrentQuizID = ""
	s.CurrentBoardID = ""
	s.bump()
	return nil
}

// ShowMidQuiz transitions to ModeMidQuiz and stores the quiz id.
func (s *Signage) ShowMidQuiz(quizID, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guardNotClosed("ShowMidQuiz"); err != nil {
		return err
	}
	if strings.TrimSpace(quizID) == "" {
		return fmt.Errorf("%w: quiz_id required", ErrInvalidArgument)
	}
	s.Mode = ModeMidQuiz
	s.CurrentQuizID = quizID
	s.CurrentTitle = title
	s.CurrentBoardID = ""
	s.bump()
	return nil
}

// ShowJamBoard transitions to ModeJamBoard and stores the board id.
func (s *Signage) ShowJamBoard(boardID, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guardNotClosed("ShowJamBoard"); err != nil {
		return err
	}
	if strings.TrimSpace(boardID) == "" {
		return fmt.Errorf("%w: board_id required", ErrInvalidArgument)
	}
	s.Mode = ModeJamBoard
	s.CurrentBoardID = boardID
	s.CurrentTitle = title
	s.CurrentQuizID = ""
	s.bump()
	return nil
}

// ShowPodium transitions to ModePodium and clears atom/quiz/board fields.
func (s *Signage) ShowPodium() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guardNotClosed("ShowPodium"); err != nil {
		return err
	}
	s.Mode = ModePodium
	s.CurrentAtomID = ""
	s.CurrentQuizID = ""
	s.CurrentBoardID = ""
	s.CurrentTitle = ""
	s.bump()
	return nil
}

// Close transitions to ModeClosed (terminal).
func (s *Signage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Mode == ModeClosed {
		return fmt.Errorf("%w: already Closed", ErrInvalidTransition)
	}
	s.Mode = ModeClosed
	s.bump()
	return nil
}

// Snapshot returns a value-typed snapshot for WS broadcast.
func (s *Signage) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		ID:             s.ID,
		TenantID:       s.TenantID,
		ClassID:        s.ClassID,
		JoinURL:        s.JoinURL,
		Mode:           s.Mode,
		CurrentAtomID:  s.CurrentAtomID,
		CurrentTitle:   s.CurrentTitle,
		CurrentQuizID:  s.CurrentQuizID,
		CurrentBoardID: s.CurrentBoardID,
		Version:        s.Version,
		UpdatedAt:      s.UpdatedAt,
	}
}

// bump increments Version + UpdatedAt. Caller holds s.mu.
func (s *Signage) bump() {
	s.Version++
	s.UpdatedAt = time.Now().UTC()
}

// -----------------------------------------------------------------------------
// UUIDv7 — local generator (deps-free)
// -----------------------------------------------------------------------------

// NewUUIDv7 returns a freshly generated UUIDv7 string per RFC 9562 §5.7.
func NewUUIDv7() string {
	const buflen = 16
	var b [buflen]byte
	now := uint64(time.Now().UnixMilli())
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	if _, err := rand.Read(b[6:]); err != nil {
		for i := 6; i < buflen; i++ {
			b[i] = byte(now >> uint(8*(i-6)))
		}
	}
	b[6] = (b[6] & 0x0F) | 0x70
	b[8] = (b[8] & 0x3F) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
