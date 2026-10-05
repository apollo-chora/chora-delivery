// live_poll.go — LivePoll aggregate for the R+ classroom-realtime stack
// (M8/M9/M10/M11). A LivePoll is a simpler sibling to LiveQuiz: a single
// multi-choice question (no per-question concept, no correctness, no scoring)
// that an instructor opens to the room, learners vote on, then closes.
//
// Hexagonal: pure domain. No infra, no repository imports.
//
// Aggregate invariants per `.claude/rules/ddd-enforcement.md`:
//   - State machine is one-way: DRAFT → OPEN → CLOSED. Re-open of a CLOSED
//     poll is rejected (a re-poll requires authoring a new LivePoll).
//   - Per-learner first-vote-wins: a second CastVote by the same learner is
//     rejected with ErrLivePollDuplicateVote (matches LiveQuizSession's
//     idempotency semantics).
//   - Votes for unknown options are rejected (the OptionLabel set is frozen
//     at construction).
package classroom

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// LivePoll state machine
// -----------------------------------------------------------------------------

// LivePollState models the LivePoll lifecycle FSM: DRAFT → OPEN → CLOSED.
type LivePollState string

const (
	// LivePollStateDraft — poll created, not yet opened to the room.
	LivePollStateDraft LivePollState = "DRAFT"
	// LivePollStateOpen — poll accepting votes.
	LivePollStateOpen LivePollState = "OPEN"
	// LivePollStateClosed — poll closed, votes frozen, results visible.
	LivePollStateClosed LivePollState = "CLOSED"
)

// IsValid reports whether s is a canonical FSM state.
func (s LivePollState) IsValid() bool {
	switch s {
	case LivePollStateDraft, LivePollStateOpen, LivePollStateClosed:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	ErrLivePollTenantRequired     = errors.New("classroom: tenant_id required")
	ErrLivePollInstructorRequired = errors.New("classroom: instructor_gcid required")
	ErrLivePollQuestionRequired   = errors.New("classroom: question required")
	ErrLivePollNeedsOptions       = errors.New("classroom: poll needs at least 2 options")
	ErrLivePollCannotOpen         = errors.New("classroom: poll not openable from current state")
	ErrLivePollCannotClose        = errors.New("classroom: poll not closable from current state")
	ErrLivePollNotOpen            = errors.New("classroom: poll not OPEN")
	ErrLivePollLearnerRequired    = errors.New("classroom: learner_gcid required")
	ErrLivePollUnknownOption      = errors.New("classroom: unknown option")
	ErrLivePollDuplicateVote      = errors.New("classroom: learner already voted (first-vote-wins)")
)

// -----------------------------------------------------------------------------
// LivePollOption value object
// -----------------------------------------------------------------------------

// LivePollOption is one selectable answer with a running vote tally.
//
// Note on naming: Label is the author-typed answer-choice content (e.g.
// "Just right"), per `feedback_mcq_option_naming`. There is no positional
// "marker" (A/B/C/D) — the FE computes those at render time from the
// stable order of the slice.
type LivePollOption struct {
	Label     string `json:"label"`
	VoteCount int    `json:"vote_count"`
}

// -----------------------------------------------------------------------------
// LivePoll aggregate root
// -----------------------------------------------------------------------------

// LivePoll is the R+ classroom-realtime poll aggregate.
type LivePoll struct {
	ID             string           `json:"id"`
	TenantID       string           `json:"tenant_id"`
	CourseID       string           `json:"course_id,omitempty"`
	InstructorGCID string           `json:"instructor_gcid"`
	Question       string           `json:"question"`
	Options        []LivePollOption `json:"options"`
	State          LivePollState    `json:"state"`
	// voters tracks the set of learner_gcids that have already voted
	// (for first-vote-wins idempotency). Unexported because the only valid
	// mutation is via CastVote.
	voters    map[string]struct{}
	OpenedAt  *time.Time `json:"opened_at,omitempty"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// NewLivePollInput is the constructor payload.
type NewLivePollInput struct {
	TenantID       string
	CourseID       string
	InstructorGCID string
	Question       string
	Options        []string
}

// NewLivePoll constructs a DRAFT LivePoll. Validates required fields and
// that at least 2 options are provided. Option labels are trimmed; blank
// options are rejected.
func NewLivePoll(in NewLivePollInput) (*LivePoll, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, ErrLivePollTenantRequired
	}
	if strings.TrimSpace(in.InstructorGCID) == "" {
		return nil, ErrLivePollInstructorRequired
	}
	if strings.TrimSpace(in.Question) == "" {
		return nil, ErrLivePollQuestionRequired
	}
	if len(in.Options) < 2 {
		return nil, ErrLivePollNeedsOptions
	}
	opts := make([]LivePollOption, 0, len(in.Options))
	seen := make(map[string]struct{}, len(in.Options))
	for _, raw := range in.Options {
		label := strings.TrimSpace(raw)
		if label == "" {
			return nil, fmt.Errorf("%w: option label cannot be blank", ErrLivePollNeedsOptions)
		}
		if _, dup := seen[label]; dup {
			return nil, fmt.Errorf("%w: duplicate option %q", ErrLivePollNeedsOptions, label)
		}
		seen[label] = struct{}{}
		opts = append(opts, LivePollOption{Label: label, VoteCount: 0})
	}
	now := time.Now().UTC()
	return &LivePoll{
		ID:             NewUUIDv7(),
		TenantID:       in.TenantID,
		CourseID:       strings.TrimSpace(in.CourseID),
		InstructorGCID: in.InstructorGCID,
		Question:       strings.TrimSpace(in.Question),
		Options:        opts,
		State:          LivePollStateDraft,
		voters:         make(map[string]struct{}),
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// Open transitions DRAFT → OPEN. Idempotent within OPEN. Returns
// ErrLivePollCannotOpen when the poll has already been CLOSED (no re-open).
func (p *LivePoll) Open(now time.Time) error {
	switch p.State {
	case LivePollStateOpen:
		return nil // idempotent
	case LivePollStateDraft:
		opened := now.UTC()
		p.State = LivePollStateOpen
		p.OpenedAt = &opened
		p.UpdatedAt = opened
		return nil
	default:
		return ErrLivePollCannotOpen
	}
}

// Close transitions OPEN → CLOSED. Idempotent within CLOSED. Returns
// ErrLivePollCannotClose when called from DRAFT (poll never ran).
func (p *LivePoll) Close(now time.Time) error {
	switch p.State {
	case LivePollStateClosed:
		return nil // idempotent
	case LivePollStateOpen:
		closed := now.UTC()
		p.State = LivePollStateClosed
		p.ClosedAt = &closed
		p.UpdatedAt = closed
		return nil
	default:
		return ErrLivePollCannotClose
	}
}

// CastVote records one learner's vote for an option by label. First-vote-wins
// per learner — a second CastVote by the same learner (even for a different
// option) is rejected with ErrLivePollDuplicateVote. The option label MUST
// match one of the labels frozen at construction; unknown labels return
// ErrLivePollUnknownOption. Only valid when the poll is OPEN.
func (p *LivePoll) CastVote(learnerGCID, optionLabel string, at time.Time) error {
	if p.State != LivePollStateOpen {
		return ErrLivePollNotOpen
	}
	if strings.TrimSpace(learnerGCID) == "" {
		return ErrLivePollLearnerRequired
	}
	if p.voters == nil {
		// Defensive: a poll rehydrated from storage without going through
		// NewLivePoll still gets a working voter set.
		p.voters = make(map[string]struct{})
	}
	if _, already := p.voters[learnerGCID]; already {
		return ErrLivePollDuplicateVote
	}
	idx := -1
	for i, opt := range p.Options {
		if opt.Label == optionLabel {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrLivePollUnknownOption
	}
	p.Options[idx].VoteCount++
	p.voters[learnerGCID] = struct{}{}
	p.UpdatedAt = at.UTC()
	return nil
}

// VoteCount returns the tally for the given option label, or 0 if the label
// is unknown.
func (p *LivePoll) VoteCount(optionLabel string) int {
	for _, opt := range p.Options {
		if opt.Label == optionLabel {
			return opt.VoteCount
		}
	}
	return 0
}

// TotalVotes returns the sum of votes across all options (= the number of
// unique learners who have voted).
func (p *LivePoll) TotalVotes() int {
	total := 0
	for _, opt := range p.Options {
		total += opt.VoteCount
	}
	return total
}

// -----------------------------------------------------------------------------
// Lossless JSON serialization (ADR-168 gap-1b)
// -----------------------------------------------------------------------------
//
// LivePoll is persisted as a JSONB aggregate snapshot by the pg adapter
// (internal/adapter/repo/pg/live_poll.go), mirroring the LiveQuiz /
// LiveQuizSession JSONB-snapshot pattern from gap-1. Those siblings are all
// exported → encoding/json round-trips them losslessly with zero custom code.
//
// LivePoll is NOT: the `voters` set is UNEXPORTED (it must be — first-vote-wins
// idempotency may only mutate via CastVote, never from outside the aggregate).
// encoding/json cannot see unexported fields, so a default Marshal would DROP
// the voter set; a poll rehydrated on a non-owning pod would then let an
// already-voted learner vote AGAIN. THIS is why LivePoll was deferred from
// gap-1 (the lossy-snapshot risk).
//
// The arch-clean fix keeps the snapshot pattern (no separate `voters` child
// table — polls resolve by id; voters need not be independently queryable, and
// a child table would diverge from the Session/Quiz pattern + add normalization
// debt) and makes the snapshot LOSSLESS by giving the aggregate explicit
// Marshal/Unmarshal that owns `voters` serialization (emitted as a JSON array of
// learner_gcids, rehydrated into the set on unmarshal). Serialization therefore
// lives ON the aggregate that owns the field — `voters` stays unexported to
// every other package. The pg adapter just calls json.Marshal/Unmarshal exactly
// like the quiz/session repos (no special-casing leaks into the adapter).

// livePollWire is the on-the-wire / on-disk shape. The type alias on LivePoll
// strips its Marshal/Unmarshal methods (avoiding infinite recursion) while
// keeping all exported field tags; `Voters` is the explicit projection of the
// unexported set.
type livePollWire struct {
	*livePollAlias
	Voters []string `json:"voters"`
}

// livePollAlias aliases LivePoll to drop its custom (Un)MarshalJSON methods so
// the embedded struct serializes its exported fields by the standard rules.
type livePollAlias LivePoll

// MarshalJSON emits all exported LivePoll fields plus `voters` as a JSON array
// of learner_gcids, so the JSONB snapshot is lossless (first-vote-wins state
// survives cross-pod rehydration).
func (p LivePoll) MarshalJSON() ([]byte, error) {
	voters := make([]string, 0, len(p.voters))
	for gcid := range p.voters {
		voters = append(voters, gcid)
	}
	sort.Strings(voters) // deterministic snapshot bytes (stable upsert diffs)
	alias := livePollAlias(p)
	return json.Marshal(livePollWire{livePollAlias: &alias, Voters: voters})
}

// UnmarshalJSON rehydrates all exported fields plus the unexported voter set
// from the `voters` array. A snapshot written before this method existed (no
// `voters` key) yields an empty-but-non-nil set — CastVote stays correct and
// the legacy poll simply starts tracking voters from rehydration onward.
func (p *LivePoll) UnmarshalJSON(b []byte) error {
	alias := livePollAlias{}
	wire := livePollWire{livePollAlias: &alias}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	*p = LivePoll(alias)
	p.voters = make(map[string]struct{}, len(wire.Voters))
	for _, gcid := range wire.Voters {
		p.voters[gcid] = struct{}{}
	}
	return nil
}
