// exam_test.go — table-driven tests for the Exam aggregate (proctored
// exam sitting) and its 5-state FSM.
//
// TDD: written FIRST, drives exam.go. Coverage target: >=85% domain.
//
// Test matrix:
//
//	NewExam            — constructor guards + happy-path defaults
//	Exam.Schedule      — DRAFT → SCHEDULED + validation guards
//	Exam.Open          — SCHEDULED → OPEN
//	Exam.Close         — OPEN → CLOSED (or SCHEDULED → CLOSED for no-op admin close)
//	Exam.Grade         — CLOSED → GRADED
//	Exam.Enroll        — enrolled_count++ with capacity guard
//	Exam.IsValid state — only canonical states pass
//
// Aggregate invariants (per ddd-enforcement.md):
//   - LearningAtom-cross-domain: course_id is an opaque UUID reference
//   - UUIDv7 IDs (sortable creation order)
//   - Append-only audit via UpdatedAt advancement on every transition
package exam

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	examTestTenantID = "019e2f93-d586-71b5-8c3d-e2b0d0d50100"
	examTestCourseID = "019e2f93-d586-71b5-8c3d-e2b0d0d50200"
)

func mustScheduledAt(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
}

// -----------------------------------------------------------------------------
// NewExam
// -----------------------------------------------------------------------------

func TestNewExam_OK_PopulatesDraftShell(t *testing.T) {
	in := NewExamInput{
		TenantID:        examTestTenantID,
		CourseID:        examTestCourseID,
		Title:           "  Certified Scrum Product Owner  ",
		ScheduledAt:     mustScheduledAt(t),
		DurationMinutes: 120,
		Capacity:        30,
		ProctorMethod:   ProctorMethodAutoAI,
	}
	e, err := NewExam(in)
	if err != nil {
		t.Fatalf("NewExam err=%v want nil", err)
	}
	if e.ID == "" {
		t.Errorf("ID empty; want UUIDv7")
	}
	if e.State != ExamStateDraft {
		t.Errorf("State=%q want DRAFT", e.State)
	}
	if e.TenantID != examTestTenantID {
		t.Errorf("TenantID=%q", e.TenantID)
	}
	if e.CourseID != examTestCourseID {
		t.Errorf("CourseID=%q", e.CourseID)
	}
	if e.Title != "Certified Scrum Product Owner" {
		t.Errorf("Title=%q want trimmed", e.Title)
	}
	if e.DurationMinutes != 120 {
		t.Errorf("DurationMinutes=%d want 120", e.DurationMinutes)
	}
	if e.Capacity != 30 {
		t.Errorf("Capacity=%d want 30", e.Capacity)
	}
	if e.EnrolledCount != 0 {
		t.Errorf("EnrolledCount=%d want 0", e.EnrolledCount)
	}
	if e.ProctorMethod != ProctorMethodAutoAI {
		t.Errorf("ProctorMethod=%q want AUTO_AI", e.ProctorMethod)
	}
	if e.CreatedAt.IsZero() || e.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: created=%v updated=%v", e.CreatedAt, e.UpdatedAt)
	}
}

func TestNewExam_Errors(t *testing.T) {
	base := NewExamInput{
		TenantID:        examTestTenantID,
		CourseID:        examTestCourseID,
		Title:           "T",
		ScheduledAt:     mustScheduledAt(t),
		DurationMinutes: 60,
		Capacity:        20,
		ProctorMethod:   ProctorMethodAutoAI,
	}
	cases := []struct {
		name string
		mut  func(in *NewExamInput)
		want error
	}{
		{"empty tenant_id", func(in *NewExamInput) { in.TenantID = "" }, ErrExamTenantRequired},
		{"empty course_id", func(in *NewExamInput) { in.CourseID = "" }, ErrExamCourseRequired},
		{"non-uuid course_id", func(in *NewExamInput) { in.CourseID = "course-cspo" }, ErrExamCourseInvalid},
		{"empty title", func(in *NewExamInput) { in.Title = "   " }, ErrExamTitleRequired},
		{"zero duration", func(in *NewExamInput) { in.DurationMinutes = 0 }, ErrExamDurationInvalid},
		{"negative duration", func(in *NewExamInput) { in.DurationMinutes = -10 }, ErrExamDurationInvalid},
		{"zero capacity", func(in *NewExamInput) { in.Capacity = 0 }, ErrExamCapacityInvalid},
		{"negative capacity", func(in *NewExamInput) { in.Capacity = -1 }, ErrExamCapacityInvalid},
		{"invalid proctor_method", func(in *NewExamInput) { in.ProctorMethod = "PROCTOR_METHOD_TELEPATHY" }, ErrExamProctorMethodInvalid},
		{"empty proctor_method defaults", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			if tc.mut != nil {
				tc.mut(&in)
			} else {
				in.ProctorMethod = ""
			}
			e, err := NewExam(in)
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Errorf("err=%v want %v", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Errorf("err=%v want nil", err)
			}
			// Empty proctor_method should default to AUTO_AI.
			if e.ProctorMethod != ProctorMethodAutoAI {
				t.Errorf("default ProctorMethod=%q want AUTO_AI", e.ProctorMethod)
			}
		})
	}
}

func TestNewExam_AcceptsAllThreeProctorMethods(t *testing.T) {
	for _, pm := range []ProctorMethod{
		ProctorMethodAutoAI,
		ProctorMethodHumanLive,
		ProctorMethodHumanRecorded,
	} {
		t.Run(string(pm), func(t *testing.T) {
			e, err := NewExam(NewExamInput{
				TenantID:        examTestTenantID,
				CourseID:        examTestCourseID,
				Title:           "T",
				ScheduledAt:     mustScheduledAt(t),
				DurationMinutes: 60,
				Capacity:        20,
				ProctorMethod:   pm,
			})
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			if e.ProctorMethod != pm {
				t.Errorf("got=%q want %q", e.ProctorMethod, pm)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// CourseID must be a well-formed UUID (the live cert-rollup finding)
// -----------------------------------------------------------------------------

// TestNewExam_RejectsNonUUIDCourseID pins the invariant that closed the live
// R+ EXAM cert-rollup outage: an exam was scheduled with CourseID
// "course-cspo" (a placeholder string the R+ form's own hint suggested), the
// candidate PASSED, and the downstream cert INSERT died on
// `invalid input syntax for type uuid: "course-cspo"` (SQLSTATE 22P02), then
// NACK-retried 5 times into the DLQ. The credential was earned and never
// minted.
//
// course_id is an opaque cross-aggregate UUID reference (no Go-level FK, per
// ddd-enforcement) - "opaque" constrains its MEANING, not its SHAPE. Rejecting
// the bad shape HERE, in the constructor, is the only place that covers every
// creation path; downstream the value is already load-bearing.
func TestNewExam_RejectsNonUUIDCourseID(t *testing.T) {
	bad := []struct {
		name     string
		courseID string
	}{
		{"live finding placeholder", "course-cspo"},
		{"slug", "advanced-scrum-master"},
		{"numeric id", "12345"},
		{"truncated uuid", "019e2f93-d586-71b5-8c3d"},
		{"uuid with trailing junk", examTestCourseID + "-oops"},
		{"sql-ish injection shape", "'; DROP TABLE certifications; --"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewExam(NewExamInput{
				TenantID:        examTestTenantID,
				CourseID:        tc.courseID,
				Title:           "T",
				ScheduledAt:     mustScheduledAt(t),
				DurationMinutes: 60,
				Capacity:        20,
				ProctorMethod:   ProctorMethodAutoAI,
			})
			if !errors.Is(err, ErrExamCourseInvalid) {
				t.Errorf("NewExam(course_id=%q) err=%v; want ErrExamCourseInvalid", tc.courseID, err)
			}
		})
	}
}

// TestNewExam_AcceptsUUIDCourseID_AnyVersion - the guard checks SHAPE only. A
// course_id minted by another aggregate is opaque: any RFC 9562 version parses,
// and surrounding whitespace is trimmed rather than rejected (consistent with
// how the constructor already treats tenant_id + title).
func TestNewExam_AcceptsUUIDCourseID_AnyVersion(t *testing.T) {
	good := []struct {
		name     string
		courseID string
		want     string
	}{
		{"uuidv7", examTestCourseID, examTestCourseID},
		{"uuidv4", "9f1b7c2e-4a3d-4b8e-9c1f-2d3e4f5a6b7c", "9f1b7c2e-4a3d-4b8e-9c1f-2d3e4f5a6b7c"},
		{"padded with whitespace", "  " + examTestCourseID + "  ", examTestCourseID},
	}
	for _, tc := range good {
		t.Run(tc.name, func(t *testing.T) {
			e, err := NewExam(NewExamInput{
				TenantID:        examTestTenantID,
				CourseID:        tc.courseID,
				Title:           "T",
				ScheduledAt:     mustScheduledAt(t),
				DurationMinutes: 60,
				Capacity:        20,
				ProctorMethod:   ProctorMethodAutoAI,
			})
			if err != nil {
				t.Fatalf("NewExam(course_id=%q) err=%v; want nil", tc.courseID, err)
			}
			if e.CourseID != tc.want {
				t.Errorf("CourseID=%q want %q", e.CourseID, tc.want)
			}
		})
	}
}

// TestNewExam_CanonicalisesCourseID - uuid.Parse is lenient (urn: prefix,
// braces, dashless) but Postgres is not: `urn:uuid:...` is itself a 22P02 there.
// Validating with Parse while storing the RAW text would leave the original bug
// reachable through a narrower door, so what gets stored is the canonical form
// every downstream uuid cast accepts.
func TestNewExam_CanonicalisesCourseID(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"urn prefix (postgres would 22P02 on this raw)", "urn:uuid:" + examTestCourseID},
		{"braced", "{" + examTestCourseID + "}"},
		{"dashless", strings.ReplaceAll(examTestCourseID, "-", "")},
		{"uppercase", strings.ToUpper(examTestCourseID)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := NewExam(NewExamInput{
				TenantID:        examTestTenantID,
				CourseID:        tc.in,
				Title:           "T",
				ScheduledAt:     mustScheduledAt(t),
				DurationMinutes: 60,
				Capacity:        20,
				ProctorMethod:   ProctorMethodAutoAI,
			})
			if err != nil {
				t.Fatalf("NewExam(course_id=%q) err=%v; want nil", tc.in, err)
			}
			if e.CourseID != examTestCourseID {
				t.Errorf("CourseID=%q; want canonical %q so every downstream uuid cast accepts it",
					e.CourseID, examTestCourseID)
			}
		})
	}
}

// TestNewExam_EmptyCourseID_StaysRequiredNotInvalid - an ABSENT course and a
// MALFORMED course are different operator mistakes and must keep their distinct
// sentinels, so the 400 tells the admin which one they made.
func TestNewExam_EmptyCourseID_StaysRequiredNotInvalid(t *testing.T) {
	_, err := NewExam(NewExamInput{
		TenantID:        examTestTenantID,
		CourseID:        "   ",
		Title:           "T",
		ScheduledAt:     mustScheduledAt(t),
		DurationMinutes: 60,
		Capacity:        20,
		ProctorMethod:   ProctorMethodAutoAI,
	})
	if !errors.Is(err, ErrExamCourseRequired) {
		t.Errorf("blank course_id err=%v; want ErrExamCourseRequired", err)
	}
	if errors.Is(err, ErrExamCourseInvalid) {
		t.Errorf("a blank course_id must not report as MALFORMED; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// State.IsValid
// -----------------------------------------------------------------------------

func TestExamState_IsValid(t *testing.T) {
	for _, s := range []ExamState{
		ExamStateDraft,
		ExamStateScheduled,
		ExamStateOpen,
		ExamStateClosed,
		ExamStateGraded,
	} {
		if !s.IsValid() {
			t.Errorf("%q.IsValid()=false want true", s)
		}
	}
	if ExamState("BANANA").IsValid() {
		t.Errorf("BANANA.IsValid()=true want false")
	}
	if ExamState("").IsValid() {
		t.Errorf("''.IsValid()=true want false")
	}
}

// -----------------------------------------------------------------------------
// Schedule (DRAFT → SCHEDULED)
// -----------------------------------------------------------------------------

func newDraftExam(t *testing.T) *Exam {
	t.Helper()
	e, err := NewExam(NewExamInput{
		TenantID:        examTestTenantID,
		CourseID:        examTestCourseID,
		Title:           "T",
		ScheduledAt:     mustScheduledAt(t),
		DurationMinutes: 60,
		Capacity:        20,
		ProctorMethod:   ProctorMethodAutoAI,
	})
	if err != nil {
		t.Fatalf("setup NewExam: %v", err)
	}
	return e
}

func TestExam_Schedule_DraftToScheduled(t *testing.T) {
	e := newDraftExam(t)
	orig := e.UpdatedAt
	time.Sleep(2 * time.Millisecond)
	if err := e.Schedule(); err != nil {
		t.Fatalf("Schedule err=%v", err)
	}
	if e.State != ExamStateScheduled {
		t.Errorf("State=%q want SCHEDULED", e.State)
	}
	if !e.UpdatedAt.After(orig) {
		t.Errorf("UpdatedAt did not advance: orig=%v new=%v", orig, e.UpdatedAt)
	}
}

func TestExam_Schedule_RejectsNotDraft(t *testing.T) {
	e := newDraftExam(t)
	if err := e.Schedule(); err != nil {
		t.Fatalf("setup Schedule: %v", err)
	}
	err := e.Schedule()
	if !errors.Is(err, ErrExamNotDraft) {
		t.Errorf("err=%v want ErrExamNotDraft", err)
	}
}

// -----------------------------------------------------------------------------
// Open (SCHEDULED → OPEN)
// -----------------------------------------------------------------------------

func TestExam_Open_ScheduledToOpen(t *testing.T) {
	e := newDraftExam(t)
	_ = e.Schedule()
	if err := e.Open(); err != nil {
		t.Fatalf("Open err=%v", err)
	}
	if e.State != ExamStateOpen {
		t.Errorf("State=%q want OPEN", e.State)
	}
}

func TestExam_Open_RejectsFromDraft(t *testing.T) {
	e := newDraftExam(t)
	err := e.Open()
	if !errors.Is(err, ErrExamNotScheduled) {
		t.Errorf("err=%v want ErrExamNotScheduled", err)
	}
}

// -----------------------------------------------------------------------------
// Close (OPEN | SCHEDULED → CLOSED)
// -----------------------------------------------------------------------------

func TestExam_Close_FromOpen(t *testing.T) {
	e := newDraftExam(t)
	_ = e.Schedule()
	_ = e.Open()
	if err := e.Close(); err != nil {
		t.Fatalf("Close err=%v", err)
	}
	if e.State != ExamStateClosed {
		t.Errorf("State=%q want CLOSED", e.State)
	}
}

func TestExam_Close_FromScheduledAlsoOK(t *testing.T) {
	// Admin cancel before opening for registration is a legitimate path —
	// SCHEDULED → CLOSED short-circuits the OPEN intermediate.
	e := newDraftExam(t)
	_ = e.Schedule()
	if err := e.Close(); err != nil {
		t.Fatalf("Close err=%v", err)
	}
	if e.State != ExamStateClosed {
		t.Errorf("State=%q want CLOSED", e.State)
	}
}

func TestExam_Close_RejectsFromDraft(t *testing.T) {
	e := newDraftExam(t)
	err := e.Close()
	if !errors.Is(err, ErrExamNotCloseable) {
		t.Errorf("err=%v want ErrExamNotCloseable", err)
	}
}

func TestExam_Close_RejectsFromGraded(t *testing.T) {
	e := newDraftExam(t)
	_ = e.Schedule()
	_ = e.Open()
	_ = e.Close()
	_ = e.Grade()
	err := e.Close()
	if !errors.Is(err, ErrExamNotCloseable) {
		t.Errorf("err=%v want ErrExamNotCloseable", err)
	}
}

// -----------------------------------------------------------------------------
// Grade (CLOSED → GRADED)
// -----------------------------------------------------------------------------

func TestExam_Grade_ClosedToGraded(t *testing.T) {
	e := newDraftExam(t)
	_ = e.Schedule()
	_ = e.Open()
	_ = e.Close()
	if err := e.Grade(); err != nil {
		t.Fatalf("Grade err=%v", err)
	}
	if e.State != ExamStateGraded {
		t.Errorf("State=%q want GRADED", e.State)
	}
}

func TestExam_Grade_RejectsFromOpen(t *testing.T) {
	e := newDraftExam(t)
	_ = e.Schedule()
	_ = e.Open()
	err := e.Grade()
	if !errors.Is(err, ErrExamNotClosed) {
		t.Errorf("err=%v want ErrExamNotClosed", err)
	}
}

// -----------------------------------------------------------------------------
// Enroll (enrolled_count++ capacity gate)
// -----------------------------------------------------------------------------

func TestExam_Enroll_HappyPath(t *testing.T) {
	e := newDraftExam(t)
	_ = e.Schedule()
	_ = e.Open()
	for i := 0; i < 20; i++ {
		if err := e.Enroll(); err != nil {
			t.Fatalf("Enroll #%d err=%v", i, err)
		}
	}
	if e.EnrolledCount != 20 {
		t.Errorf("EnrolledCount=%d want 20", e.EnrolledCount)
	}
}

func TestExam_Enroll_AtCapacityRejects(t *testing.T) {
	e := newDraftExam(t)
	_ = e.Schedule()
	_ = e.Open()
	for i := 0; i < 20; i++ {
		_ = e.Enroll()
	}
	err := e.Enroll()
	if !errors.Is(err, ErrExamAtCapacity) {
		t.Errorf("err=%v want ErrExamAtCapacity", err)
	}
	if e.EnrolledCount != 20 {
		t.Errorf("EnrolledCount=%d want 20 (no overflow)", e.EnrolledCount)
	}
}

func TestExam_Enroll_RejectsWhenNotOpen(t *testing.T) {
	// DRAFT (not OPEN) — enrolment not permitted.
	e := newDraftExam(t)
	err := e.Enroll()
	if !errors.Is(err, ErrExamNotOpen) {
		t.Errorf("err=%v want ErrExamNotOpen", err)
	}
}

func TestExam_Enroll_RejectsWhenClosed(t *testing.T) {
	e := newDraftExam(t)
	_ = e.Schedule()
	_ = e.Open()
	_ = e.Close()
	err := e.Enroll()
	if !errors.Is(err, ErrExamNotOpen) {
		t.Errorf("err=%v want ErrExamNotOpen", err)
	}
}

// -----------------------------------------------------------------------------
// Title trim
// -----------------------------------------------------------------------------

func TestNewExam_TrimsTitle(t *testing.T) {
	e, err := NewExam(NewExamInput{
		TenantID:        examTestTenantID,
		CourseID:        examTestCourseID,
		Title:           "\tHello World\n",
		ScheduledAt:     mustScheduledAt(t),
		DurationMinutes: 30,
		Capacity:        5,
		ProctorMethod:   ProctorMethodHumanLive,
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if e.Title != "Hello World" || strings.Contains(e.Title, "\t") {
		t.Errorf("Title=%q want trimmed", e.Title)
	}
}
