// nickname.go — SanitizeNickname for the L5.2 lobby (ADR-179 ruling 1).
//
// Pure domain, no external moderation service: trim + collapse whitespace,
// 2–20 runes, control characters rejected, and a small leet-folded profanity
// wordlist (substring match) so the obvious evasions ("sh1t", "$hit",
// "a55hole") don't reach the projector. This is a classroom-display filter,
// not a content-safety system — Cloud Model Armor remains the platform
// guardrail for generated content.
package classroom

import (
	"errors"
	"strings"
	"unicode"
)

var (
	ErrNicknameRequired = errors.New("classroom: nickname must be at least 2 characters")
	ErrNicknameTooLong  = errors.New("classroom: nickname too long (max 20)")
	ErrNicknameInvalid  = errors.New("classroom: nickname contains invalid characters")
	ErrNicknameProfane  = errors.New("classroom: nickname not allowed")
)

// nicknameWordlist holds folded forms (lowercase letters only) matched as
// substrings against the leet-folded candidate.
var nicknameWordlist = []string{
	"fuck", "shit", "bitch", "cunt", "dick", "cock", "pussy", "asshole",
	"arsehole", "bastard", "whore", "slut", "nigger", "nigga", "faggot",
	"retard", "wanker", "bollocks", "prick", "twat", "douche",
	"motherfucker", "dipshit", "jackass", "cocksucker",
}

// leetFold maps the common digit/symbol substitutions back to letters before
// the wordlist check.
var leetFold = map[rune]rune{
	'0': 'o', '1': 'i', '3': 'e', '4': 'a', '5': 's', '7': 't', '@': 'a', '$': 's',
}

// SanitizeNickname returns the canonical display nickname or a typed error:
// ErrNicknameInvalid (control chars), ErrNicknameRequired (< 2 runes after
// collapsing), ErrNicknameTooLong (> 20 runes), ErrNicknameProfane.
func SanitizeNickname(raw string) (string, error) {
	collapsed := strings.Join(strings.Fields(raw), " ")
	for _, r := range collapsed {
		if unicode.IsControl(r) {
			return "", ErrNicknameInvalid
		}
	}
	runes := []rune(collapsed)
	if len(runes) < 2 {
		return "", ErrNicknameRequired
	}
	if len(runes) > 20 {
		return "", ErrNicknameTooLong
	}
	folded := foldForProfanity(collapsed)
	for _, w := range nicknameWordlist {
		if strings.Contains(folded, w) {
			return "", ErrNicknameProfane
		}
	}
	return collapsed, nil
}

// foldForProfanity lowercases, applies the leet map, and drops everything
// that is not a letter, so separators can't split a slur.
func foldForProfanity(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if sub, ok := leetFold[r]; ok {
			b.WriteRune(sub)
			continue
		}
		if unicode.IsLetter(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
