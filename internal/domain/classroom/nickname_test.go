// nickname_test.go — RED tests for SanitizeNickname (ADR-179 ruling 1):
// trim/collapse, 2–20 runes, control chars rejected, leet-folded profanity
// wordlist. Pure domain — no external services.
package classroom

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeNickname(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{"plain ok", "MathWizard", "MathWizard", nil},
		{"trims + collapses whitespace", "  Math   Wizard  ", "Math Wizard", nil},
		{"digits + letters ok", "QuizKid42", "QuizKid42", nil},
		{"unicode ok", "Léa", "Léa", nil},
		{"too short", "x", "", ErrNicknameRequired},
		{"blank", "   ", "", ErrNicknameRequired},
		{"too long", strings.Repeat("a", 21), "", ErrNicknameTooLong},
		{"max length ok", strings.Repeat("a", 20), strings.Repeat("a", 20), nil},
		{"control char rejected", "Bad\x00Name", "", ErrNicknameInvalid},
		{"profane plain", "fuck", "", ErrNicknameProfane},
		{"profane mixed case", "FuCkFace", "", ErrNicknameProfane},
		{"profane leet 1->i", "sh1tlord", "", ErrNicknameProfane},
		{"profane leet $->s", "$hitshow", "", ErrNicknameProfane},
		{"profane leet 5->s", "a55hole", "", ErrNicknameProfane},
		{"profane embedded", "xXbitchXx", "", ErrNicknameProfane},
		{"clean word containing no slur", "ShellSort", "ShellSort", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := SanitizeNickname(c.in)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("SanitizeNickname(%q) err = %v, want %v", c.in, err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SanitizeNickname(%q) unexpected err: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("SanitizeNickname(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
