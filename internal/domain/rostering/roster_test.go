// roster_test.go — TDD coverage for the course-centric CourseRoster view
// aggregate.
//
// Distinct from rostering_test.go (which covers the class-centric Roster
// with capacity). Pins:
//
//   - NewCourseRoster guard clauses (tenant_id + course_id required)
//   - Empty roster materialises with a NON-NIL zero-length Learners slice
//     (so JSON marshalling yields `[]` not `null` per the FE contract).
//   - LearnerCount() reports zero on a fresh roster.
package rostering_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/rostering"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	courseA = "01970000-0000-7000-6000-000000000001"
)

func TestNewCourseRoster_RequiresTenantAndCourse(t *testing.T) {
	cases := []struct {
		name    string
		tenant  string
		course  string
		wantErr error
	}{
		{"happy", tenantA, courseA, nil},
		{"missing tenant", "", courseA, rostering.ErrTenantIDRequired},
		{"missing course", tenantA, "", rostering.ErrCourseIDRequired},
		{"whitespace tenant", "  ", courseA, rostering.ErrTenantIDRequired},
		{"whitespace course", tenantA, "  ", rostering.ErrCourseIDRequired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := rostering.NewCourseRoster(c.tenant, c.course)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("got err %v want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if r == nil {
				t.Fatalf("nil roster")
			}
			if r.TenantID != c.tenant || r.CourseID != c.course {
				t.Errorf("tenant/course mismatch: got (%s,%s) want (%s,%s)",
					r.TenantID, r.CourseID, c.tenant, c.course)
			}
		})
	}
}

func TestNewCourseRoster_EmptyLearnersIsNonNil(t *testing.T) {
	r, err := rostering.NewCourseRoster(tenantA, courseA)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if r.Learners == nil {
		t.Fatalf("Learners must be non-nil (FE contract: empty array, not null)")
	}
	if r.LearnerCount() != 0 {
		t.Errorf("LearnerCount() = %d, want 0", r.LearnerCount())
	}
}

// TestCourseRoster_EmptyMarshalsAsArrayNotNull pins the FE contract: when a
// course has zero enrolments the JSON wire shape is `"learners":[]` NOT
// `"learners":null`. This is the per-`feedback_no_stubs_real_wiring`
// "empty learners array, NOT a placeholder" rule from the M4 directive.
func TestCourseRoster_EmptyMarshalsAsArrayNotNull(t *testing.T) {
	r, err := rostering.NewCourseRoster(tenantA, courseA)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	type wire struct {
		Learners []rostering.RosterLearner `json:"learners"`
	}
	b, err := json.Marshal(wire{Learners: r.Learners})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, `"learners":[]`) {
		t.Errorf("expected learners=[], got %s", got)
	}
}
