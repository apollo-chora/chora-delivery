// live_quiz_session_stage.go — the L5.2 Live Classroom stage surface on LiveQuizSession
// (ADR-179, CHO-1704): join-code lobby with nicknames, lazily-derived answer
// windows (no schedulers — HPA-safe), streak + double-points scoring composed
// over TimeDecayScore, the deterministic scoreboard/podium fold, and the
// explainer-reveal gate.
//
// Hexagonal: pure domain. Everything here derives from the aggregate's own
// state; the ephemerality ruling (ADR-179 D4) holds because the scoreboard
// and podium are folds over the graded response log — no side tables.
package classroom

import (
	"crypto/rand"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// Errors (L5.2)
// -----------------------------------------------------------------------------

var (
	ErrLiveQuizSessionCannotJoin           = errors.New("classroom: session not joinable (CLOSED)")
	ErrLiveQuizSessionNicknameTaken        = errors.New("classroom: nickname already taken")
	ErrLiveQuizSessionQuestionAlreadyAsked = errors.New("classroom: question already asked — answer window cannot reopen")
	ErrLiveQuizSessionQuestionNotOpen      = errors.New("classroom: question is not the current open question")
	ErrLiveQuizSessionQuestionLocked       = errors.New("classroom: answer window closed (question locked)")
)

// -----------------------------------------------------------------------------
// Participant + lobby
// -----------------------------------------------------------------------------

// Participant is one lobby entry: a gcid that claimed a nickname via Join.
// The roster (and every learner-reachable projection) exposes nicknames, never
// gcids (ADR-179 ruling 1).
type Participant struct {
	GCID     string    `json:"gcid"`
	Nickname string    `json:"nickname"`
	JoinedAt time.Time `json:"joined_at"`
}

// Join claims a nickname for gcid (ARMED or LIVE — late join is allowed;
// CLOSED rejects). Idempotent per gcid: re-joining returns the
// existing Participant; a nickname RENAME is allowed only while ARMED so
// scoreboard identity freezes the moment the game starts. Nicknames are
// sanitised (SanitizeNickname) and unique case-insensitively across other
// gcids. Everyone may join, the instructor included (ruling 5).
func (s *LiveQuizSession) Join(gcid, rawNickname string, now time.Time) (Participant, error) {
	if s.State == LiveQuizSessionStateClosed {
		return Participant{}, ErrLiveQuizSessionCannotJoin
	}
	if strings.TrimSpace(gcid) == "" {
		return Participant{}, ErrLiveQuizSessionLearnerRequired
	}
	nick, err := SanitizeNickname(rawNickname)
	if err != nil {
		return Participant{}, err
	}
	if s.Participants == nil {
		s.Participants = map[string]Participant{}
	}
	if existing, ok := s.Participants[gcid]; ok {
		if s.State != LiveQuizSessionStateArmed || strings.EqualFold(existing.Nickname, nick) {
			return existing, nil // idempotent re-join; no rename once LIVE
		}
		if owner := s.nicknameOwner(nick); owner != "" && owner != gcid {
			return Participant{}, ErrLiveQuizSessionNicknameTaken
		}
		existing.Nickname = nick
		s.Participants[gcid] = existing
		s.UpdatedAt = now.UTC()
		return existing, nil
	}
	if owner := s.nicknameOwner(nick); owner != "" {
		return Participant{}, ErrLiveQuizSessionNicknameTaken
	}
	p := Participant{GCID: gcid, Nickname: nick, JoinedAt: now.UTC()}
	s.Participants[gcid] = p
	s.UpdatedAt = now.UTC()
	return p, nil
}

// nicknameOwner returns the gcid currently holding nick (case-insensitive),
// or "" when unclaimed.
func (s *LiveQuizSession) nicknameOwner(nick string) string {
	for gcid, p := range s.Participants {
		if strings.EqualFold(p.Nickname, nick) {
			return gcid
		}
	}
	return ""
}

// Roster returns the lobby in join order (JoinedAt, then gcid — fully
// deterministic; Participants is a map).
func (s *LiveQuizSession) Roster() []Participant {
	out := make([]Participant, 0, len(s.Participants))
	for _, p := range s.Participants {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].JoinedAt.Equal(out[j].JoinedAt) {
			return out[i].JoinedAt.Before(out[j].JoinedAt)
		}
		return out[i].GCID < out[j].GCID
	})
	return out
}

// ensureParticipant auto-registers a graded submitter who never went through
// Join (legacy flows, API callers) under a neutral fallback nickname so the
// scoreboard always has a display identity.
func (s *LiveQuizSession) ensureParticipant(gcid string, now time.Time) {
	if s.Participants == nil {
		s.Participants = map[string]Participant{}
	}
	if _, ok := s.Participants[gcid]; ok {
		return
	}
	suffix := gcid
	if len(suffix) > 4 {
		suffix = suffix[len(suffix)-4:]
	}
	s.Participants[gcid] = Participant{GCID: gcid, Nickname: "Player-" + suffix, JoinedAt: now.UTC()}
}

// -----------------------------------------------------------------------------
// Answer window — lazy lock (ruling 7)
// -----------------------------------------------------------------------------

// LockGraceSecs pads the instructor-set timer to absorb client/network skew
// before the server refuses a submit. The FE flips its UI at locks_at =
// opened_at + timer; the server accepts until locks_at + grace.
const LockGraceSecs = 2

// QuestionLocked reports whether the answer window has closed. Lazily derived
// per read — there is deliberately NO expiry writer/scheduler (a per-pod timer
// would double-fire under HPA and race the aggregate upsert). timerSecs <= 0
// means the question never auto-locks (host-paced).
func QuestionLocked(openedAt *time.Time, timerSecs int, now time.Time) bool {
	if openedAt == nil || timerSecs <= 0 {
		return false
	}
	deadline := openedAt.Add(time.Duration(timerSecs+LockGraceSecs) * time.Second)
	return !now.Before(deadline)
}

// LocksAt returns the moment the window closes (timer + grace), or nil for
// host-paced questions (timer <= 0) / nothing open. Snapshot DTOs surface this
// so every client renders the same countdown without trusting its own clock.
func LocksAt(openedAt *time.Time, timerSecs int) *time.Time {
	if openedAt == nil || timerSecs <= 0 {
		return nil
	}
	t := openedAt.Add(time.Duration(timerSecs+LockGraceSecs) * time.Second)
	return &t
}

// -----------------------------------------------------------------------------
// Scoring — TimeDecay × double-points + streak bonus (ruling 4)
// -----------------------------------------------------------------------------

// ScoreInput feeds ComposeScore.
type ScoreInput struct {
	Correct      bool
	Points       int
	Elapsed      time.Duration
	TimerSecs    int
	DoublePoints bool
	StreakBefore int
}

// ScoreResult is the composed grade for one answer.
type ScoreResult struct {
	Correct     bool `json:"correct"`
	Base        int  `json:"base"`
	Multiplier  int  `json:"multiplier"`
	StreakBonus int  `json:"streak_bonus"`
	Awarded     int  `json:"awarded"`
	StreakAfter int  `json:"streak_after"`
}

// ComposeScore composes the final award:
//
//	base   = TimeDecayScore(correct, points, elapsed, timer)
//	m      = 2 when DoublePoints else 1
//	bonus  = round(m·base · 0.10 · min(streakBefore, 5))
//	award  = m·base + bonus
//
// A wrong answer zeroes everything and resets the streak; the first correct
// answer (streakBefore 0) earns no bonus; the bonus caps at +50%.
func ComposeScore(in ScoreInput) ScoreResult {
	base := TimeDecayScore(in.Correct, in.Points, in.Elapsed, in.TimerSecs)
	m := 1
	if in.DoublePoints {
		m = 2
	}
	res := ScoreResult{Correct: in.Correct, Base: base, Multiplier: m}
	if !in.Correct {
		return res
	}
	streak := in.StreakBefore
	if streak > 5 {
		streak = 5
	}
	res.StreakBonus = int(math.Round(float64(m*base) * 0.10 * float64(streak)))
	res.Awarded = m*base + res.StreakBonus
	res.StreakAfter = in.StreakBefore + 1
	return res
}

// SubmitGradedInput carries the resolved question (the handler loads it from
// the quiz template) so the domain can grade without a repository dependency.
type SubmitGradedInput struct {
	Question    LiveQuizQuestion
	LearnerGCID string
	Choice      string
	At          time.Time
}

// SubmitGraded records AND grades a learner's answer with the full L5.2 rule
// chain: LIVE → current-question (ruling 7: answers to non-current questions
// are rejected — pre-answering from a leaked id is impossible) → window open
// → first-write-wins. The composed grade is frozen onto the response so the
// scoreboard/podium are pure folds. The legacy SubmitResponse remains as the
// ungraded fallback for sessions whose quiz template is unavailable.
func (s *LiveQuizSession) SubmitGraded(in SubmitGradedInput) (ScoreResult, error) {
	if s.State != LiveQuizSessionStateLive {
		return ScoreResult{}, ErrLiveQuizSessionNotLive
	}
	if strings.TrimSpace(in.Question.QuestionID) == "" {
		return ScoreResult{}, ErrLiveQuizSessionQuestionRequired
	}
	if strings.TrimSpace(in.LearnerGCID) == "" {
		return ScoreResult{}, ErrLiveQuizSessionLearnerRequired
	}
	if strings.TrimSpace(in.Choice) == "" {
		return ScoreResult{}, ErrLiveQuizSessionChoiceRequired
	}
	if s.CurrentQuestionID == "" || in.Question.QuestionID != s.CurrentQuestionID {
		return ScoreResult{}, ErrLiveQuizSessionQuestionNotOpen
	}
	if QuestionLocked(s.CurrentQuestionOpenedAt, in.Question.TimerSecs, in.At) {
		return ScoreResult{}, ErrLiveQuizSessionQuestionLocked
	}
	for _, r := range s.QuestionResponses {
		if r.QuestionID == in.Question.QuestionID && r.LearnerGCID == in.LearnerGCID {
			return ScoreResult{}, ErrLiveQuizSessionDuplicateResponse
		}
	}

	correct := questionChoiceCorrect(in.Question, in.Choice)
	var elapsed time.Duration
	if s.CurrentQuestionOpenedAt != nil {
		if elapsed = in.At.Sub(*s.CurrentQuestionOpenedAt); elapsed < 0 {
			elapsed = 0
		}
	}
	res := ComposeScore(ScoreInput{
		Correct:      correct,
		Points:       in.Question.Points,
		Elapsed:      elapsed,
		TimerSecs:    in.Question.TimerSecs,
		DoublePoints: in.Question.DoublePoints,
		StreakBefore: s.streakBefore(in.LearnerGCID),
	})
	s.QuestionResponses = append(s.QuestionResponses, QuestionResponse{
		QuestionID:    in.Question.QuestionID,
		LearnerGCID:   in.LearnerGCID,
		Choice:        in.Choice,
		SubmittedAt:   in.At.UTC(),
		Graded:        true,
		Correct:       correct,
		BasePoints:    res.Base,
		StreakBonus:   res.StreakBonus,
		AwardedPoints: res.Awarded,
		StreakAfter:   res.StreakAfter,
		ElapsedMillis: elapsed.Milliseconds(),
	})
	s.ensureParticipant(in.LearnerGCID, in.At)
	s.UpdatedAt = time.Now().UTC()
	return res, nil
}

// questionChoiceCorrect matches the submitted choice against option LABELS
// (the wire contract: choice is the label text, never the A/B/C/D marker).
func questionChoiceCorrect(q LiveQuizQuestion, choice string) bool {
	for _, opt := range q.Options {
		if opt.Label == choice {
			return opt.IsCorrect
		}
	}
	return false
}

// streakBefore derives the learner's streak entering the CURRENT question:
// the trailing run of correct graded answers over the asked-question log
// (excluding the current question). A skipped asked question — no graded
// response — breaks the run, exactly like a wrong answer.
// Pure derivation from the log: no mutable counter to drift or double-apply.
func (s *LiveQuizSession) streakBefore(gcid string) int {
	if len(s.AskedQuestionIDs) == 0 {
		return 0
	}
	prior := s.AskedQuestionIDs[:len(s.AskedQuestionIDs)-1]
	streak := 0
	for i := len(prior) - 1; i >= 0; i-- {
		r, ok := s.gradedResponse(gcid, prior[i])
		if !ok || !r.Correct {
			break
		}
		streak++
	}
	return streak
}

func (s *LiveQuizSession) gradedResponse(gcid, questionID string) (QuestionResponse, bool) {
	for _, r := range s.QuestionResponses {
		if r.LearnerGCID == gcid && r.QuestionID == questionID && r.Graded {
			return r, true
		}
	}
	return QuestionResponse{}, false
}

// -----------------------------------------------------------------------------
// Scoreboard + podium (rulings 3 + 8)
// -----------------------------------------------------------------------------

// ScoreboardEntry is one ranked row. GCID is carried for per-caller "me"
// resolution in the adapter; learner-facing projections expose Nickname only.
type ScoreboardEntry struct {
	GCID     string `json:"gcid"`
	Nickname string `json:"nickname"`
	Score    int    `json:"score"`
	Streak   int    `json:"streak"`
	Rank     int    `json:"rank"`
}

// Scoreboard folds the graded response log into the ranked board. Joined
// participants with no answers appear at 0 points (they're in the room).
// Deterministic tie-break: score DESC → Σ elapsed ASC (faster aggregate wins)
// → JoinedAt ASC → gcid ASC.
func (s *LiveQuizSession) Scoreboard() []ScoreboardEntry {
	type acc struct {
		score     int
		streak    int
		elapsedMS int64
	}
	byGCID := make(map[string]*acc, len(s.Participants))
	for gcid := range s.Participants {
		byGCID[gcid] = &acc{}
	}
	for _, r := range s.QuestionResponses {
		if !r.Graded {
			continue
		}
		a, ok := byGCID[r.LearnerGCID]
		if !ok {
			a = &acc{}
			byGCID[r.LearnerGCID] = a
		}
		a.score += r.AwardedPoints
		a.streak = r.StreakAfter // appends are chronological — last wins
		a.elapsedMS += r.ElapsedMillis
	}
	entries := make([]ScoreboardEntry, 0, len(byGCID))
	for gcid, a := range byGCID {
		nick := gcid
		if p, ok := s.Participants[gcid]; ok {
			nick = p.Nickname
		}
		entries = append(entries, ScoreboardEntry{GCID: gcid, Nickname: nick, Score: a.score, Streak: a.streak})
	}
	joinedAt := func(gcid string) time.Time {
		if p, ok := s.Participants[gcid]; ok {
			return p.JoinedAt
		}
		return time.Time{}
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		ea, eb := byGCID[a.GCID].elapsedMS, byGCID[b.GCID].elapsedMS
		if ea != eb {
			return ea < eb
		}
		ja, jb := joinedAt(a.GCID), joinedAt(b.GCID)
		if !ja.Equal(jb) {
			return ja.Before(jb)
		}
		return a.GCID < b.GCID
	})
	for i := range entries {
		entries[i].Rank = i + 1
	}
	return entries
}

// -----------------------------------------------------------------------------
// Reveal gate (explainer_mode → who may see correctness when)
// -----------------------------------------------------------------------------

// RevealAllowed gates the per-caller reveal payload (correct label +
// explainer) per the quiz's ExplainerMode. Unknown/blank modes deny.
func RevealAllowed(mode ExplainerMode, locked, callerSubmitted, sessionClosed bool) bool {
	switch mode {
	case ExplainerModeImmediate:
		return callerSubmitted || locked
	case ExplainerModeEndOfQuestion:
		return locked
	case ExplainerModeEndOfSession:
		return sessionClosed
	default: // NEVER, blank, invalid
		return false
	}
}

// -----------------------------------------------------------------------------
// Join code (ruling 6)
// -----------------------------------------------------------------------------

// joinCodeAlphabet omits 0/O/1/I/L — every character is unambiguous when read
// off a projector or typed from a phone. 32^6 ≈ 1.07e9 codes.
const joinCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// joinCodeLength is the projector-friendly code size.
const joinCodeLength = 6

// NewJoinCode returns a fresh 6-char join code from the unambiguous alphabet.
func NewJoinCode() string {
	var b [joinCodeLength]byte
	if _, err := rand.Read(b[:]); err != nil {
		now := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(now >> uint(8*i))
		}
	}
	out := make([]byte, joinCodeLength)
	for i, v := range b {
		out[i] = joinCodeAlphabet[int(v)%len(joinCodeAlphabet)]
	}
	return string(out)
}

// NormalizeJoinCode canonicalises user input: uppercase, spaces/dashes
// stripped — "k7-m3 qx" → "K7M3QX".
func NormalizeJoinCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	return strings.ReplaceAll(s, "-", "")
}
