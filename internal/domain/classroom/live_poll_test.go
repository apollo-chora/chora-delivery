// live_poll_test.go — RED-phase TDD coverage for the LivePoll aggregate
// (R+ M8/M9/M10/M11 classroom-realtime).
package classroom

import (
	"testing"
	"time"
)

func TestNewLivePoll_RequiresFields(t *testing.T) {
	_, err := NewLivePoll(NewLivePollInput{
		TenantID:       "",
		InstructorGCID: "instr-1",
		Question:       "How are we doing?",
		Options:        []string{"Great", "Meh"},
	})
	if err == nil {
		t.Fatalf("want tenant required")
	}
	_, err = NewLivePoll(NewLivePollInput{
		TenantID:       "tenant",
		InstructorGCID: "",
		Question:       "How are we doing?",
		Options:        []string{"Great", "Meh"},
	})
	if err == nil {
		t.Fatalf("want instructor required")
	}
	_, err = NewLivePoll(NewLivePollInput{
		TenantID:       "tenant",
		InstructorGCID: "instr",
		Question:       "",
		Options:        []string{"Great", "Meh"},
	})
	if err == nil {
		t.Fatalf("want question required")
	}
	_, err = NewLivePoll(NewLivePollInput{
		TenantID:       "tenant",
		InstructorGCID: "instr",
		Question:       "How?",
		Options:        []string{"Only One"},
	})
	if err == nil {
		t.Fatalf("want >=2 options")
	}
}

func TestNewLivePoll_StartsDraft(t *testing.T) {
	p, err := NewLivePoll(NewLivePollInput{
		TenantID:       "tenant",
		InstructorGCID: "instr",
		Question:       "How is the pacing?",
		Options:        []string{"Too slow", "Just right", "Too fast"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p.State != LivePollStateDraft {
		t.Fatalf("want DRAFT; got %s", p.State)
	}
	if len(p.Options) != 3 {
		t.Fatalf("want 3 options; got %d", len(p.Options))
	}
	for _, opt := range p.Options {
		if opt.VoteCount != 0 {
			t.Fatalf("fresh poll must have zero votes per option")
		}
	}
}

func TestOpen_DraftToOpen(t *testing.T) {
	p := mustLivePoll(t)
	now := time.Now()
	if err := p.Open(now); err != nil {
		t.Fatalf("Open err: %v", err)
	}
	if p.State != LivePollStateOpen {
		t.Fatalf("want OPEN; got %s", p.State)
	}
	if p.OpenedAt == nil || !p.OpenedAt.Equal(now.UTC()) {
		t.Fatalf("OpenedAt must equal now")
	}
}

func TestOpen_RejectsAfterClose(t *testing.T) {
	p := mustLivePoll(t)
	_ = p.Open(time.Now())
	_ = p.Close(time.Now())
	if err := p.Open(time.Now()); err == nil {
		t.Fatalf("want err re-opening CLOSED poll; got nil")
	}
}

func TestVote_OnlyWhenOpen(t *testing.T) {
	p := mustLivePoll(t)
	// Draft — reject.
	if err := p.CastVote("learner-a", "Just right", time.Now()); err == nil {
		t.Fatalf("want err voting in DRAFT; got nil")
	}
	_ = p.Open(time.Now())
	if err := p.CastVote("learner-a", "Just right", time.Now()); err != nil {
		t.Fatalf("vote err: %v", err)
	}
}

func TestVote_DuplicateLearnerFirstWins(t *testing.T) {
	p := mustLivePoll(t)
	_ = p.Open(time.Now())
	_ = p.CastVote("learner-a", "Just right", time.Now())
	err := p.CastVote("learner-a", "Too fast", time.Now())
	if err == nil {
		t.Fatalf("want duplicate-vote error")
	}
	if got := p.VoteCount("Just right"); got != 1 {
		t.Fatalf("want 1 vote 'Just right'; got %d", got)
	}
	if got := p.VoteCount("Too fast"); got != 0 {
		t.Fatalf("first-wins must keep 'Too fast' at 0; got %d", got)
	}
}

func TestVote_UnknownOptionRejected(t *testing.T) {
	p := mustLivePoll(t)
	_ = p.Open(time.Now())
	if err := p.CastVote("a", "Banana", time.Now()); err == nil {
		t.Fatalf("want error on unknown option; got nil")
	}
}

func TestVote_MultipleLearners(t *testing.T) {
	p := mustLivePoll(t)
	_ = p.Open(time.Now())
	_ = p.CastVote("a", "Just right", time.Now())
	_ = p.CastVote("b", "Just right", time.Now())
	_ = p.CastVote("c", "Too slow", time.Now())
	if got := p.VoteCount("Just right"); got != 2 {
		t.Fatalf("want 2; got %d", got)
	}
	if got := p.VoteCount("Too slow"); got != 1 {
		t.Fatalf("want 1; got %d", got)
	}
	if total := p.TotalVotes(); total != 3 {
		t.Fatalf("want total 3; got %d", total)
	}
}

func TestClose_OpenToClosed(t *testing.T) {
	p := mustLivePoll(t)
	_ = p.Open(time.Now())
	now := time.Now()
	if err := p.Close(now); err != nil {
		t.Fatalf("Close err: %v", err)
	}
	if p.State != LivePollStateClosed {
		t.Fatalf("want CLOSED; got %s", p.State)
	}
	if p.ClosedAt == nil {
		t.Fatalf("want ClosedAt set")
	}
}

func TestClose_RejectsDraft(t *testing.T) {
	p := mustLivePoll(t)
	if err := p.Close(time.Now()); err == nil {
		t.Fatalf("want err closing DRAFT poll; got nil")
	}
}

func TestPollClose_IdempotentOnClosed(t *testing.T) {
	p := mustLivePoll(t)
	_ = p.Open(time.Now())
	_ = p.Close(time.Now())
	if err := p.Close(time.Now()); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
}

func mustLivePoll(t *testing.T) *LivePoll {
	t.Helper()
	p, err := NewLivePoll(NewLivePollInput{
		TenantID:       "00000000-0000-7000-8000-000000000000",
		InstructorGCID: "11111111-1111-7111-8111-111111111111",
		Question:       "How is the pacing?",
		Options:        []string{"Too slow", "Just right", "Too fast"},
		CourseID:       "course-cspo",
	})
	if err != nil {
		t.Fatalf("mustLivePoll: %v", err)
	}
	return p
}
