// Package mid_class_quiz owns the mid-lesson quiz aggregate for the
// Classroom Experience inside chora-delivery.
//
// Per CHO-13 + docs/design/ux_classroom_experience.md "LivePlay" step the
// instructor launches a Live Classroom quiz mid-lesson and the system
// aggregates real-time responses for projector + post-session analytics.
//
// 3-state lifecycle: Draft → Live → Closed.
// Distinct from Surface-1 quiz authoring (chora-creation) — this aggregate
// owns the live-session instance, not the source content.
//
// Hexagonal: pure domain. UUIDv7 IDs. AtomID is a cross-aggregate UUID.
package mid_class_quiz

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
	ErrInvalidArgument     = errors.New("invalid argument")
	ErrInvalidTransition   = errors.New("invalid state transition")
	ErrInsufficientOptions = errors.New("quiz needs >= 2 options to launch")
	ErrNoCorrectOption     = errors.New("quiz needs at least one correct option")
	ErrUnknownOption       = errors.New("unknown option id")
	ErrTimerExpired        = errors.New("submission window expired")
)

// -----------------------------------------------------------------------------
// State enum
// -----------------------------------------------------------------------------

// State is the quiz lifecycle.
type State string

const (
	// StateDraft — instructor is editing options.
	StateDraft State = "draft"
	// StateLive — quiz pushed to learners; submissions accepted within timer.
	StateLive State = "live"
	// StateClosed — instructor closed the quiz; submissions rejected.
	StateClosed State = "closed"
)

// -----------------------------------------------------------------------------
// Constants — defensive bounds
// -----------------------------------------------------------------------------

const (
	// MinSeconds matches the QuizPrep range (10..120s) — see
	// docs/design/ux_classroom_experience.md QuizPrep step.
	MinSeconds = 5
	// MaxSeconds caps timer per atom to 300s (5 min) so a stuck quiz is
	// recoverable without a manual close.
	MaxSeconds = 300
	// MinOptions is the minimum option count required to launch.
	MinOptions = 2
)

// -----------------------------------------------------------------------------
// Option child entity
// -----------------------------------------------------------------------------

// Option is a single multiple-choice answer.
type Option struct {
	ID      string // 1-3 char display id (typically "a","b","c","d")
	Label   string
	Correct bool
}

// -----------------------------------------------------------------------------
// Response — atomic submission record
// -----------------------------------------------------------------------------

// Response is one learner's answer to the quiz.
type Response struct {
	GCID        string
	OptionID    string
	SubmittedAt time.Time
	LatencyMs   int64 // ms from LaunchedAt
}

// -----------------------------------------------------------------------------
// Quiz aggregate root
// -----------------------------------------------------------------------------

// Quiz is the mid-class quiz aggregate, keyed by (tenant, class, id).
type Quiz struct {
	ID             string
	TenantID       string
	ClassID        string
	InstructorGCID string
	AtomID         string
	Stem           string
	Seconds        int
	Options        []*Option
	State          State
	LaunchedAt     *time.Time
	ClosedAt       *time.Time
	responses      map[string]*Response // by gcid; first-wins idempotency
	CreatedAt      time.Time
	UpdatedAt      time.Time

	mu sync.Mutex
}

// NewQuiz constructs a Quiz in StateDraft with no options.
func NewQuiz(tenantID, classID, instructorGCID, atomID, stem string, seconds int) (*Quiz, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(classID) == "" {
		return nil, fmt.Errorf("%w: class_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(instructorGCID) == "" {
		return nil, fmt.Errorf("%w: instructor_gcid required", ErrInvalidArgument)
	}
	if strings.TrimSpace(atomID) == "" {
		return nil, fmt.Errorf("%w: atom_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(stem) == "" {
		return nil, fmt.Errorf("%w: stem required", ErrInvalidArgument)
	}
	if seconds < MinSeconds || seconds > MaxSeconds {
		return nil, fmt.Errorf("%w: seconds must be in [%d,%d]", ErrInvalidArgument, MinSeconds, MaxSeconds)
	}
	now := time.Now().UTC()
	return &Quiz{
		ID:             NewUUIDv7(),
		TenantID:       tenantID,
		ClassID:        classID,
		InstructorGCID: instructorGCID,
		AtomID:         atomID,
		Stem:           stem,
		Seconds:        seconds,
		Options:        make([]*Option, 0, 4),
		State:          StateDraft,
		responses:      make(map[string]*Response),
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// AddOption appends an Option (only valid in Draft).
func (q *Quiz) AddOption(id, label string, correct bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.State != StateDraft {
		return fmt.Errorf("%w: AddOption requires Draft, got %s", ErrInvalidTransition, q.State)
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: option id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(label) == "" {
		return fmt.Errorf("%w: option label required", ErrInvalidArgument)
	}
	for _, o := range q.Options {
		if o.ID == id {
			return fmt.Errorf("%w: duplicate option id %q", ErrInvalidArgument, id)
		}
	}
	q.Options = append(q.Options, &Option{ID: id, Label: label, Correct: correct})
	q.UpdatedAt = time.Now().UTC()
	return nil
}

// Launch transitions Draft → Live, capturing wall-clock launch.
//
// Pre-conditions:
//   - >= 2 options
//   - >= 1 correct option.
func (q *Quiz) Launch(now time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.State != StateDraft {
		return fmt.Errorf("%w: Launch requires Draft, got %s", ErrInvalidTransition, q.State)
	}
	if len(q.Options) < MinOptions {
		return ErrInsufficientOptions
	}
	hasCorrect := false
	for _, o := range q.Options {
		if o.Correct {
			hasCorrect = true
			break
		}
	}
	if !hasCorrect {
		return ErrNoCorrectOption
	}
	stamp := now.UTC()
	q.LaunchedAt = &stamp
	q.State = StateLive
	q.UpdatedAt = stamp
	return nil
}

// Submit records a learner's response. First-wins idempotency keyed by gcid.
//
// Pre-conditions:
//   - State == Live
//   - now is within [LaunchedAt, LaunchedAt + Seconds]
//   - optionID matches one of q.Options.
func (q *Quiz) Submit(gcid, optionID string, now time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.State != StateLive {
		return fmt.Errorf("%w: Submit requires Live, got %s", ErrInvalidTransition, q.State)
	}
	if strings.TrimSpace(gcid) == "" {
		return fmt.Errorf("%w: gcid required", ErrInvalidArgument)
	}
	deadline := q.LaunchedAt.Add(time.Duration(q.Seconds) * time.Second)
	if now.After(deadline) {
		return ErrTimerExpired
	}
	known := false
	for _, o := range q.Options {
		if o.ID == optionID {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("%w: option %q not in quiz", ErrUnknownOption, optionID)
	}
	if _, exists := q.responses[gcid]; exists {
		// First-wins idempotency — second submission by same learner is a
		// no-op and not an error.
		return nil
	}
	stamp := now.UTC()
	q.responses[gcid] = &Response{
		GCID:        gcid,
		OptionID:    optionID,
		SubmittedAt: stamp,
		LatencyMs:   stamp.Sub(*q.LaunchedAt).Milliseconds(),
	}
	q.UpdatedAt = stamp
	return nil
}

// Close transitions Live → Closed.
func (q *Quiz) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.State != StateLive {
		return fmt.Errorf("%w: Close requires Live, got %s", ErrInvalidTransition, q.State)
	}
	stamp := time.Now().UTC()
	q.ClosedAt = &stamp
	q.State = StateClosed
	q.UpdatedAt = stamp
	return nil
}

// Distribution returns response counts per option id (zero-filled for all
// known option ids).
func (q *Quiz) Distribution() map[string]int {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make(map[string]int, len(q.Options))
	for _, o := range q.Options {
		out[o.ID] = 0
	}
	for _, r := range q.responses {
		out[r.OptionID]++
	}
	return out
}

// TotalResponses returns the count of distinct learners who submitted.
func (q *Quiz) TotalResponses() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.responses)
}

// CorrectRate returns the proportion of responses that picked a correct
// option (0..1). Returns 0.0 if no responses.
func (q *Quiz) CorrectRate() float64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.responses) == 0 {
		return 0.0
	}
	correct := 0
	for _, r := range q.responses {
		for _, o := range q.Options {
			if o.ID == r.OptionID && o.Correct {
				correct++
				break
			}
		}
	}
	return float64(correct) / float64(len(q.responses))
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
