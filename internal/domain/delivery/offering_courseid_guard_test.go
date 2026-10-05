// Offering course_id UUID-guard tests.
//
// The Offering aggregate persists CourseIDs inside the JSONB `data` column, so
// there is no typed DB guard on the list. Two live queries cast that JSONB
// straight to uuid:
//
//	adapter/repo/pg/cohort_roster.go  (SQLLearnerEnrolledInOffering, ADR-234)
//	adapter/repo/pg/assessment.go     (OPEN_LINK offering-attached visibility)
//
// so ONE non-UUID entry 22P02s those queries for the whole offering. The
// aggregate's own `offerings.course_id UUID NOT NULL` extract column guards
// only CourseIDs[0] (the PRIMARY, written via PrimaryCourseID); CourseIDs[1:]
// reach the database inside the blob, unchecked. These tests pin the guard at
// NewOffering, the one chokepoint every creation path crosses.
//
// Mirrors the exam aggregate's canonicalCourseID (domain/exam/exam.go).
package delivery_test

import (
	"errors"
	"strings"
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// guardCourseCanonical is the canonical (lowercase, dashed) form of the course
// UUID the shape-tests below feed in via their various accepted spellings.
const guardCourseCanonical = "01970000-0000-7000-8000-0000000000c0"

// TestNewOffering_RejectsNonUUIDCourseID: the placeholder that armed the exam
// bomb ("course-cspo") must be refused at construction, not a fortnight later
// inside a Pub/Sub subscriber.
func TestNewOffering_RejectsNonUUIDCourseID(t *testing.T) {
	t.Parallel()
	cases := []string{
		"course-cspo",
		"not-a-uuid",
		"01970000-0000-7000-8000-0000000000cZ", // non-hex digit
		"01970000-0000-7000-8000",              // truncated
	}
	for _, raw := range cases {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			in := validOfferingInput()
			in.CourseIDs = []string{raw}
			_, err := domain.NewOffering(in)
			if !errors.Is(err, domain.ErrOfferingCourseInvalid) {
				t.Fatalf("want ErrOfferingCourseInvalid for %q, got %v", raw, err)
			}
		})
	}
}

// TestNewOffering_RejectsNonUUIDInNonPrimaryPosition is THE bomb: the
// `offerings.course_id UUID NOT NULL` extract column only ever sees
// PrimaryCourseID (CourseIDs[0]), so a bad SECOND course is accepted by every
// existing guard and lands in the JSONB blob, where it detonates later in the
// cohort-roster / assessment casts. A guard that only checks the primary is
// worthless here.
func TestNewOffering_RejectsNonUUIDInNonPrimaryPosition(t *testing.T) {
	t.Parallel()
	in := validOfferingInput()
	in.CourseIDs = []string{guardCourseCanonical, "course-cspo"}
	_, err := domain.NewOffering(in)
	if !errors.Is(err, domain.ErrOfferingCourseInvalid) {
		t.Fatalf("want ErrOfferingCourseInvalid for a bad non-primary course, got %v", err)
	}
}

// TestNewOffering_CourseInvalidNamesTheOffendingIndex: the sentinel is wrapped
// with the offending position so a 400 tells an admin WHICH of five pasted
// courses was mistyped, while errors.Is still matches.
func TestNewOffering_CourseInvalidNamesTheOffendingIndex(t *testing.T) {
	t.Parallel()
	in := validOfferingInput()
	in.CourseIDs = []string{guardCourseCanonical, "course-cspo"}
	_, err := domain.NewOffering(in)
	if !errors.Is(err, domain.ErrOfferingCourseInvalid) {
		t.Fatalf("want ErrOfferingCourseInvalid, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "course_ids[1]") {
		t.Fatalf("error must name the offending index course_ids[1], got %q", msg)
	}
	if !strings.Contains(msg, "course-cspo") {
		t.Fatalf("error must quote the rejected value, got %q", msg)
	}
}

// TestNewOffering_CanonicalisesCourseID: uuid.Parse is lenient (it accepts
// urn:uuid:, brace-wrapped and dashless forms) but Postgres is not: the urn:
// prefix is a 22P02 there. Validating and storing the RAW text would leave the
// same bomb reachable through a narrower door, so what is STORED must be the
// one form every downstream ::uuid cast accepts.
func TestNewOffering_CanonicalisesCourseID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{"already canonical", guardCourseCanonical},
		{"uppercase", "01970000-0000-7000-8000-0000000000C0"},
		{"urn prefix", "urn:uuid:01970000-0000-7000-8000-0000000000c0"},
		{"brace wrapped", "{01970000-0000-7000-8000-0000000000c0}"},
		{"dashless", "019700000000700080000000000000c0"},
		{"surrounding space", "  01970000-0000-7000-8000-0000000000c0  "},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := validOfferingInput()
			in.CourseIDs = []string{tc.raw}
			o, err := domain.NewOffering(in)
			if err != nil {
				t.Fatalf("NewOffering(%q): %v", tc.raw, err)
			}
			if o.CourseIDs[0] != guardCourseCanonical {
				t.Fatalf("want canonical %q, got %q", guardCourseCanonical, o.CourseIDs[0])
			}
			if o.PrimaryCourseID() != guardCourseCanonical {
				t.Fatalf("primary must be canonical, got %q", o.PrimaryCourseID())
			}
		})
	}
}

// TestNewOffering_DedupesOnCanonicalForm: dedup must run on the canonical
// form, not the raw text. Otherwise "X" and "urn:uuid:X" survive as two
// distinct entries and the offering carries the same course twice.
func TestNewOffering_DedupesOnCanonicalForm(t *testing.T) {
	t.Parallel()
	in := validOfferingInput()
	in.CourseIDs = []string{
		guardCourseCanonical,
		"urn:uuid:01970000-0000-7000-8000-0000000000c0",
		"01970000-0000-7000-8000-0000000000C0",
		"{01970000-0000-7000-8000-0000000000c0}",
	}
	o, err := domain.NewOffering(in)
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	if len(o.CourseIDs) != 1 {
		t.Fatalf("four spellings of one course must dedup to 1, got %v", o.CourseIDs)
	}
	if o.CourseIDs[0] != guardCourseCanonical {
		t.Fatalf("want canonical %q, got %q", guardCourseCanonical, o.CourseIDs[0])
	}
}

// TestNewOffering_BlankCoursesStillRequired pins the PRESERVED lenient
// behaviour: blank entries are dropped (not rejected as malformed), and an
// all-blank list is still ErrOfferingCourseRequired, not ErrOfferingCourseInvalid.
// A blank cannot arm the cast (it never reaches the blob), so the guard does not
// change this contract.
func TestNewOffering_BlankCoursesStillRequired(t *testing.T) {
	t.Parallel()
	in := validOfferingInput()
	in.CourseIDs = []string{"", "   "}
	_, err := domain.NewOffering(in)
	if !errors.Is(err, domain.ErrOfferingCourseRequired) {
		t.Fatalf("want ErrOfferingCourseRequired, got %v", err)
	}
}
