// course_cj2.go — CJ#2 state-FSM + validation for the Course aggregate.
//
// Adds the authoring → review → release lifecycle for Customer Journey #2
// per directive row at `docs/m13/e2e-fe-coord-directive-2026-05-16.md` §3
// + spec at `docs/TODO-DEVELOPMENT.md` "Customer Journey #2".
//
// State machine:
//
//	DRAFT            → AWAITING_REVIEW   via Publish (instructor)
//	AWAITING_REVIEW  → PUBLISHED         via Release (training-admin)
//	AWAITING_REVIEW  → DRAFT             via Reject  (training-admin)
//	PUBLISHED        → ARCHIVED          out-of-scope v1
//
// Per ddd-enforcement.md aggregate invariants:
//   - Append-only audit via review_notes (set, never cleared)
//   - Soft delete via deleted_at (NOT a state transition; orthogonal)
//
// All state transitions live on the aggregate and are pure; the persistence
// + event-emission concerns live in the http handler / repo layer.
package delivery

import (
	"errors"
	"strings"
	"time"
)

// CourseState models the Course CJ#2 lifecycle FSM.
type CourseState string

const (
	// CourseStateDraft — instructor is still authoring.
	CourseStateDraft CourseState = "DRAFT"
	// CourseStateAwaitingReview — submitted to R+ admin queue.
	CourseStateAwaitingReview CourseState = "AWAITING_REVIEW"
	// CourseStatePublished — released to learners (/a/catalog).
	CourseStatePublished CourseState = "PUBLISHED"
	// CourseStateArchived — out of service (out-of-scope v1).
	CourseStateArchived CourseState = "ARCHIVED"
)

// IsValid reports whether s is one of the canonical CJ#2 states.
func (s CourseState) IsValid() bool {
	switch s {
	case CourseStateDraft, CourseStateAwaitingReview,
		CourseStatePublished, CourseStateArchived:
		return true
	}
	return false
}

// Course CJ#2 domain errors.
var (
	// ErrCourseNotDraft — operation requires DRAFT state.
	ErrCourseNotDraft = errors.New("delivery: course not in DRAFT state")
	// ErrCourseNotAwaitingReview — operation requires AWAITING_REVIEW state.
	ErrCourseNotAwaitingReview = errors.New("delivery: course not in AWAITING_REVIEW state")
	// ErrCourseTitleRequired — publish/release require a non-empty title.
	ErrCourseTitleRequired = errors.New("delivery: title required")
	// ErrCourseTestSetIDsRequired — publish requires ≥1 test_set_id.
	ErrCourseTestSetIDsRequired = errors.New("delivery: at least one test_set_id required")
	// ErrCourseLearningObjectivesRequired — publish requires ≥1 objective.
	ErrCourseLearningObjectivesRequired = errors.New("delivery: at least one learning_objective required")
	// ErrCourseInstructorRosterRequired — release requires ≥1 instructor.
	ErrCourseInstructorRosterRequired = errors.New("delivery: at least one instructor_gcid required at release")
	// ErrCoursePriceNegative — release rejects negative prices.
	ErrCoursePriceNegative = errors.New("delivery: price_sgd_cents must be ≥ 0")
	// ErrCourseReviewNotesRequired — reject requires non-empty review_notes.
	ErrCourseReviewNotesRequired = errors.New("delivery: review_notes required on reject")
	// ErrCourseAuthorRequired — create requires non-empty author_gcid.
	ErrCourseAuthorRequired = errors.New("delivery: author_gcid required")
)

// NewCJ2Course constructs a DRAFT-state Course shell for the CJ#2 authoring
// flow. The constructor does NOT require capacity (legacy NewCourse field)
// — CJ#2 courses use test-sets instead of seat-counted classes.
//
// Validation guards (rejected with ErrInvalidArgument / specific sentinel):
//   - tenant_id non-empty
//   - author_gcid non-empty
//   - title trim-non-empty (≥1 char)
//   - test_set_ids: at least one
//
// Returned course is in CourseStateDraft, with current UTC timestamps.
// State transition methods (Publish / Release / Reject) drive subsequent
// flow; the aggregate ID is freshly generated UUIDv7.
type NewCJ2CourseInput struct {
	TenantID           string
	AuthorGCID         string
	Title              string
	Description        string
	LearningObjectives []string
	PrerequisiteNotes  []string
	TestSetIDs         []string
	// Certification — optional cert the course awards (CHO-1795). nil ⇒ none.
	Certification *CertDefinition
}

// NewCJ2Course constructs a DRAFT shell.
func NewCJ2Course(in NewCJ2CourseInput) (*Course, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, ErrAssessmentTenantRequired
	}
	if strings.TrimSpace(in.AuthorGCID) == "" {
		return nil, ErrCourseAuthorRequired
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, ErrCourseTitleRequired
	}
	if len(in.TestSetIDs) == 0 {
		return nil, ErrCourseTestSetIDsRequired
	}
	var cert CertDefinition
	if in.Certification != nil {
		if err := in.Certification.Validate(); err != nil {
			return nil, err
		}
		cert = *in.Certification
	}
	now := time.Now().UTC()
	return &Course{
		ID:                 NewUUIDv7(),
		TenantID:           in.TenantID,
		InstructorGCID:     in.AuthorGCID, // legacy column mirror for back-compat
		AuthorGCID:         in.AuthorGCID,
		Title:              strings.TrimSpace(in.Title),
		Description:        in.Description,
		LearningObjectives: dupSlice(in.LearningObjectives),
		PrerequisiteNotes:  dupSlice(in.PrerequisiteNotes),
		TestSetIDs:         dupSlice(in.TestSetIDs),
		State:              CourseStateDraft,
		MaxCapacity:        1, // sentinel: CJ#2 courses don't seat-count
		Certification:      cert,
		CreatedAt:          now,
		UpdatedAt:          now,
	}, nil
}

// UpdateDraftContent mutates a DRAFT course's content fields (title /
// description / objectives / prerequisites / test_set_ids). All inputs
// are optional via nil-slice / empty-string convention: nil slices are
// ignored; empty strings clear the field.
//
// Returns ErrCourseNotDraft when the course is not in DRAFT state.
type UpdateCourseInput struct {
	Title              *string
	Description        *string
	LearningObjectives []string
	PrerequisiteNotes  []string
	TestSetIDs         []string
	// Certification — optional cert update (CHO-1795). nil ⇒ unchanged.
	Certification *CertDefinition
}

// UpdateDraftContent applies partial updates to a DRAFT course.
func (c *Course) UpdateDraftContent(in UpdateCourseInput) error {
	if c.State != CourseStateDraft {
		return ErrCourseNotDraft
	}
	if in.Certification != nil {
		if err := in.Certification.Validate(); err != nil {
			return err
		}
	}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return ErrCourseTitleRequired
		}
		c.Title = t
	}
	if in.Description != nil {
		c.Description = *in.Description
	}
	if in.LearningObjectives != nil {
		c.LearningObjectives = dupSlice(in.LearningObjectives)
	}
	if in.PrerequisiteNotes != nil {
		c.PrerequisiteNotes = dupSlice(in.PrerequisiteNotes)
	}
	if in.TestSetIDs != nil {
		c.TestSetIDs = dupSlice(in.TestSetIDs)
	}
	if in.Certification != nil {
		c.Certification = *in.Certification
	}
	c.UpdatedAt = time.Now().UTC()
	return nil
}

// Publish transitions DRAFT → AWAITING_REVIEW.
//
// Validates: title non-empty, ≥1 test_set_id, ≥1 learning_objective.
// RBAC enforcement (author OR instructor role) is the http handler's job.
func (c *Course) Publish() error {
	if c.State != CourseStateDraft {
		return ErrCourseNotDraft
	}
	if strings.TrimSpace(c.Title) == "" {
		return ErrCourseTitleRequired
	}
	if len(c.TestSetIDs) == 0 {
		return ErrCourseTestSetIDsRequired
	}
	if len(c.LearningObjectives) == 0 {
		return ErrCourseLearningObjectivesRequired
	}
	c.State = CourseStateAwaitingReview
	c.UpdatedAt = time.Now().UTC()
	return nil
}

// ReleaseInput captures the commercial fields the training-admin sets at
// release time. Fields:
//   - PriceSGDCents      : price in cents (≥ 0; 0 = free)
//   - SFEligible         : SkillsFuture eligibility
//   - InstructorGCIDs    : roster of authorised instructors (≥ 1)
//   - ScheduledOpenAt    : when enrolment opens (optional; nil → immediate)
type ReleaseInput struct {
	PriceSGDCents   int64
	SFEligible      bool
	InstructorGCIDs []string
	ScheduledOpenAt *time.Time
}

// Release transitions AWAITING_REVIEW → PUBLISHED.
//
// Sets PublishedAt = now() + commercial fields atomically. RBAC enforcement
// (training-admin role) is the http handler's job.
func (c *Course) Release(in ReleaseInput) error {
	if c.State != CourseStateAwaitingReview {
		return ErrCourseNotAwaitingReview
	}
	if in.PriceSGDCents < 0 {
		return ErrCoursePriceNegative
	}
	if len(in.InstructorGCIDs) == 0 {
		return ErrCourseInstructorRosterRequired
	}
	now := time.Now().UTC()
	c.State = CourseStatePublished
	c.PriceSGDCents = in.PriceSGDCents
	c.SFEligible = in.SFEligible
	c.InstructorGCIDs = dupSlice(in.InstructorGCIDs)
	if in.ScheduledOpenAt != nil {
		t := in.ScheduledOpenAt.UTC()
		c.ScheduledOpenAt = &t
	}
	c.PublishedAt = &now
	c.UpdatedAt = now
	// Mirror lead instructor onto the legacy InstructorGCID column for the
	// existing /a/catalog projection (catalogueDTO reads InstructorGCID).
	c.InstructorGCID = c.InstructorGCIDs[0]
	return nil
}

// Reject transitions AWAITING_REVIEW → DRAFT. Sets review_notes.
//
// RBAC enforcement (training-admin role) is the http handler's job.
func (c *Course) Reject(reviewNotes string) error {
	if c.State != CourseStateAwaitingReview {
		return ErrCourseNotAwaitingReview
	}
	notes := strings.TrimSpace(reviewNotes)
	if notes == "" {
		return ErrCourseReviewNotesRequired
	}
	c.State = CourseStateDraft
	c.ReviewNotes = notes
	c.UpdatedAt = time.Now().UTC()
	return nil
}

// VisibleToCaller reports whether the course is visible to the given caller
// given their GCID + role set. Visibility rules:
//   - PUBLISHED : any in-tenant caller
//   - DRAFT / AWAITING_REVIEW / ARCHIVED : author OR staff (training-admin)
//
// The role strings are lowercased by the caller (roleSet). The staff set is
// the CANONICAL Identity vocabulary: mint_handler.go stamps the underscore
// token training_admin (and tenant_admin), so a reviewer's x-mesh-user-roles
// is "instructor,training_admin" — the hyphen "training-admin" is the legacy
// assessment-surface shape kept for parity. This gate is the app-layer
// defence-in-depth over the state-aware RLS policy on courses (mig 0043/0044),
// so it MUST match that policy's staff set exactly
// (admin|training-admin|training_admin|tenant_admin); a narrower set 404s
// review-queue rows the list (RLS-governed) correctly shows (CHO-2256
// follow-up, same class as the CHO-2233 dead-branch on hasInstructorOrAdmin).
// A bare instructor is deliberately NOT staff here — RLS excludes it too.
func (c *Course) VisibleToCaller(callerGCID string, roles map[string]bool) bool {
	if c.State == CourseStatePublished {
		return true
	}
	if callerGCID != "" && callerGCID == c.AuthorGCID {
		return true
	}
	if roles["training-admin"] || roles["training_admin"] ||
		roles["admin"] || roles["tenant_admin"] {
		return true
	}
	return false
}

// dupSlice returns a defensive copy of src (nil → nil). Used to keep the
// aggregate's slice fields decoupled from caller-supplied backing arrays.
func dupSlice(src []string) []string {
	if src == nil {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}
