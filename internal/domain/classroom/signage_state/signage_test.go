// Package signage_state_test pins the contract for the real-time signage
// TV state machine per CHO-13 + docs/design/ux_classroom_experience.md
// "DisplaySignage" step.
//
// The signage aggregate owns what the projector should display "right
// now" — pre-start, atom-in-flight, mid-quiz, jamboard spotlight, podium.
// Frontend signage-realtime/ subscribes to state changes via WebSocket
// from this aggregate.
//
// Tests written FIRST per .claude/rules/development-execution.md TDD.
package signage_state_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom/signage_state"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	classA  = "01970000-0000-7000-7100-000000000001"
	atomA   = "atom://0xK3-NETWORK-DEFENSES"
	atomB   = "atom://0xK3-CRYPTO-INTRO"
	quizA   = "01970000-0000-7000-7300-000000000001"
)

// -----------------------------------------------------------------------------
// NewSignage — initial state
// -----------------------------------------------------------------------------

func TestNewSignage_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		tenant  string
		class   string
		joinURL string
		wantErr error
	}{
		{"happy", tenantA, classA, "https://chora.site/join/A7K2M9", nil},
		{"missing tenant", "", classA, "https://x", signage_state.ErrInvalidArgument},
		{"missing class", tenantA, "", "https://x", signage_state.ErrInvalidArgument},
		{"missing join url", tenantA, classA, "", signage_state.ErrInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := signage_state.NewSignage(c.tenant, c.class, c.joinURL)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("got %v want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if s.Mode != signage_state.ModePreStart {
				t.Errorf("expected ModePreStart, got %s", s.Mode)
			}
			if s.JoinURL != c.joinURL {
				t.Errorf("join url not set")
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Transitions: PreStart → Atom → MidQuiz → Atom → JamBoard → Podium → Closed
// -----------------------------------------------------------------------------

func TestShowAtom_FromPreStart(t *testing.T) {
	s, _ := signage_state.NewSignage(tenantA, classA, "https://chora.site/join/X")

	if err := s.ShowAtom(atomA, "Network defenses intro"); err != nil {
		t.Fatalf("show atom: %v", err)
	}
	if s.Mode != signage_state.ModeAtom {
		t.Errorf("expected ModeAtom, got %s", s.Mode)
	}
	if s.CurrentAtomID != atomA {
		t.Errorf("atom id: got %s want %s", s.CurrentAtomID, atomA)
	}
	if s.CurrentTitle != "Network defenses intro" {
		t.Errorf("title not stored")
	}

	// Show another atom (Atom→Atom permitted)
	if err := s.ShowAtom(atomB, "Crypto intro"); err != nil {
		t.Fatalf("atom2: %v", err)
	}
	if s.CurrentAtomID != atomB {
		t.Errorf("atom not advanced")
	}

	// Empty atom rejected
	if err := s.ShowAtom("", "x"); !errors.Is(err, signage_state.ErrInvalidArgument) {
		t.Errorf("empty atom: got %v", err)
	}
}

func TestShowMidQuiz_OnlyFromAtomOrPreStart(t *testing.T) {
	s, _ := signage_state.NewSignage(tenantA, classA, "https://chora.site/join/X")

	// PreStart → MidQuiz permitted (instructor jumps straight to a quiz)
	if err := s.ShowMidQuiz(quizA, "Knowledge check"); err != nil {
		t.Fatalf("pre→quiz: %v", err)
	}
	if s.Mode != signage_state.ModeMidQuiz {
		t.Errorf("got %s want ModeMidQuiz", s.Mode)
	}
	if s.CurrentQuizID != quizA {
		t.Errorf("quiz id not stored")
	}

	// MidQuiz → Atom permitted
	if err := s.ShowAtom(atomA, "after quiz"); err != nil {
		t.Fatalf("quiz→atom: %v", err)
	}

	// Atom → MidQuiz permitted
	if err := s.ShowMidQuiz(quizA, "again"); err != nil {
		t.Fatalf("atom→quiz: %v", err)
	}

	// Empty quiz id rejected
	if err := s.ShowMidQuiz("", "x"); !errors.Is(err, signage_state.ErrInvalidArgument) {
		t.Errorf("empty quiz: got %v", err)
	}
}

func TestShowJamBoard(t *testing.T) {
	s, _ := signage_state.NewSignage(tenantA, classA, "https://chora.site/join/X")
	_ = s.ShowAtom(atomA, "intro")

	if err := s.ShowJamBoard("board-1", "Brainstorm: defense layers"); err != nil {
		t.Fatalf("jamboard: %v", err)
	}
	if s.Mode != signage_state.ModeJamBoard {
		t.Errorf("got %s want ModeJamBoard", s.Mode)
	}
	if s.CurrentBoardID != "board-1" {
		t.Errorf("board id not stored")
	}

	// Empty board id rejected
	if err := s.ShowJamBoard("", "x"); !errors.Is(err, signage_state.ErrInvalidArgument) {
		t.Errorf("empty board: got %v", err)
	}
}

func TestShowPodium(t *testing.T) {
	s, _ := signage_state.NewSignage(tenantA, classA, "https://chora.site/join/X")
	_ = s.ShowAtom(atomA, "intro")

	if err := s.ShowPodium(); err != nil {
		t.Fatalf("podium: %v", err)
	}
	if s.Mode != signage_state.ModePodium {
		t.Errorf("got %s want ModePodium", s.Mode)
	}

	// Atom/Quiz/JamBoard fields cleared on Podium
	if s.CurrentAtomID != "" {
		t.Errorf("atom not cleared on podium")
	}
	if s.CurrentQuizID != "" {
		t.Errorf("quiz not cleared")
	}
	if s.CurrentBoardID != "" {
		t.Errorf("board not cleared")
	}
}

// -----------------------------------------------------------------------------
// Close — terminal
// -----------------------------------------------------------------------------

func TestClose_FromAnyNonTerminal(t *testing.T) {
	for _, start := range []func(s *signage_state.Signage){
		// PreStart
		func(s *signage_state.Signage) {},
		// Atom
		func(s *signage_state.Signage) { _ = s.ShowAtom(atomA, "x") },
		// MidQuiz
		func(s *signage_state.Signage) { _ = s.ShowMidQuiz(quizA, "x") },
		// JamBoard
		func(s *signage_state.Signage) { _ = s.ShowJamBoard("b", "x") },
		// Podium
		func(s *signage_state.Signage) { _ = s.ShowPodium() },
	} {
		s, _ := signage_state.NewSignage(tenantA, classA, "https://x")
		start(s)
		if err := s.Close(); err != nil {
			t.Fatalf("close from %s: %v", s.Mode, err)
		}
		if s.Mode != signage_state.ModeClosed {
			t.Errorf("got %s want ModeClosed", s.Mode)
		}
	}
}

func TestClose_TerminalIsTerminal(t *testing.T) {
	s, _ := signage_state.NewSignage(tenantA, classA, "https://x")
	_ = s.Close()

	// All operations rejected after Close
	if err := s.ShowAtom(atomA, "x"); !errors.Is(err, signage_state.ErrInvalidTransition) {
		t.Errorf("atom after close: got %v", err)
	}
	if err := s.ShowMidQuiz(quizA, "x"); !errors.Is(err, signage_state.ErrInvalidTransition) {
		t.Errorf("quiz after close: got %v", err)
	}
	if err := s.ShowJamBoard("b", "x"); !errors.Is(err, signage_state.ErrInvalidTransition) {
		t.Errorf("jam after close: got %v", err)
	}
	if err := s.ShowPodium(); !errors.Is(err, signage_state.ErrInvalidTransition) {
		t.Errorf("podium after close: got %v", err)
	}
	if err := s.Close(); !errors.Is(err, signage_state.ErrInvalidTransition) {
		t.Errorf("re-close: got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Version + transition counter for WebSocket reconnect
// -----------------------------------------------------------------------------

func TestVersion_BumpsOnEveryTransition(t *testing.T) {
	s, _ := signage_state.NewSignage(tenantA, classA, "https://x")
	v0 := s.Version

	_ = s.ShowAtom(atomA, "x")
	if s.Version != v0+1 {
		t.Errorf("v after atom: got %d want %d", s.Version, v0+1)
	}
	_ = s.ShowMidQuiz(quizA, "x")
	if s.Version != v0+2 {
		t.Errorf("v after quiz: got %d", s.Version)
	}
	_ = s.ShowJamBoard("b", "x")
	if s.Version != v0+3 {
		t.Errorf("v after jam: got %d", s.Version)
	}
	_ = s.ShowPodium()
	if s.Version != v0+4 {
		t.Errorf("v after podium: got %d", s.Version)
	}
	_ = s.Close()
	if s.Version != v0+5 {
		t.Errorf("v after close: got %d", s.Version)
	}
}

// -----------------------------------------------------------------------------
// Snapshot — wire format for WS broadcast
// -----------------------------------------------------------------------------

func TestSnapshot_Stable(t *testing.T) {
	s, _ := signage_state.NewSignage(tenantA, classA, "https://chora.site/join/X")
	_ = s.ShowAtom(atomA, "Intro")

	snap := s.Snapshot()
	if snap.Mode != signage_state.ModeAtom {
		t.Errorf("snap mode: got %s", snap.Mode)
	}
	if snap.CurrentAtomID != atomA {
		t.Errorf("snap atom: got %s", snap.CurrentAtomID)
	}
	if snap.Version != s.Version {
		t.Errorf("snap version mismatch")
	}
}
