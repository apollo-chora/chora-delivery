// offering.go — the Offering aggregate: a delivery INSTANCE of a reusable
// Course, carrying delivery_type + the offering lifecycle FSM.
//
// R+ four-delivery-mode refactor W1, per ADR-190 (delivery_type policy over
// ONE Content Delivery context). This promotes the prior Cohort skeleton:
// the offering is the thing a Course is *run as*, and delivery_type selects
// which of the three same-context modes it runs as (graduate / short /
// async). Exam is its own bounded context (ADR-190) and is NOT a
// delivery_type here.
//
//	delivery_type ∈ {graduate, short, async}   — set once at creation
//
// State machine (mirrors the in-package course_cj2.go / assessment.go idiom):
//
//	DRAFT      → LAUNCHED   via Launch   (offering opens for enrolment/publish)
//	LAUNCHED   → RUNNING    via Start    (delivery is under way)
//	RUNNING    → CONCLUDED  via Conclude (delivery has finished)
//	any        → ARCHIVED   via Archive  (terminal soft-delete; idempotent)
//
// Cross-aggregate references travel as opaque UUIDs (CourseID has no Go-level
// FK) per .claude/rules/ddd-enforcement.md. This file is pure domain — NO
// HTTP, NO persistence imports.
package delivery

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// delivery_type
// -----------------------------------------------------------------------------

// DeliveryType is the policy attribute (ADR-190) that selects which of the
// three same-context delivery modes an Offering runs as. Lowercase tokens are
// the canonical wire/db values, verbatim from the plan (§2/§3).
type DeliveryType string

const (
	// DeliveryTypeGraduate — multi-section graduate-programme cohort.
	DeliveryTypeGraduate DeliveryType = "graduate"
	// DeliveryTypeShort — short-course / single-cohort run.
	DeliveryTypeShort DeliveryType = "short"
	// DeliveryTypeAsync — self-paced async (Udemy-style); lightest offering.
	DeliveryTypeAsync DeliveryType = "async"
)

// IsValid reports whether dt is one of the canonical delivery types.
func (dt DeliveryType) IsValid() bool {
	switch dt {
	case DeliveryTypeGraduate, DeliveryTypeShort, DeliveryTypeAsync:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// State
// -----------------------------------------------------------------------------

// OfferingState models the Offering lifecycle FSM.
type OfferingState string

const (
	// OfferingStateDraft — admin is configuring the offering.
	OfferingStateDraft OfferingState = "DRAFT"
	// OfferingStateLaunched — open for enrolment / published; not yet running.
	OfferingStateLaunched OfferingState = "LAUNCHED"
	// OfferingStateRunning — delivery is under way.
	OfferingStateRunning OfferingState = "RUNNING"
	// OfferingStateConcluded — delivery has finished (terminal, non-archived).
	OfferingStateConcluded OfferingState = "CONCLUDED"
	// OfferingStateArchived — out of service; archived + soft-deleted.
	OfferingStateArchived OfferingState = "ARCHIVED"
)

// IsValid reports whether s is one of the canonical Offering states.
func (s OfferingState) IsValid() bool {
	switch s {
	case OfferingStateDraft, OfferingStateLaunched, OfferingStateRunning,
		OfferingStateConcluded, OfferingStateArchived:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	// ErrOfferingTenantRequired — tenant_id must be trim-non-empty.
	ErrOfferingTenantRequired = errors.New("delivery: offering tenant_id required")
	// ErrOfferingCourseRequired — at least one course_id must be trim-non-empty
	// (offering→course is one-to-many).
	ErrOfferingCourseRequired = errors.New("delivery: offering requires at least one course_id")
	// ErrOfferingCourseInvalid: every course_id must be a well-formed UUID.
	// Distinct from ErrOfferingCourseRequired so the 400 tells an admin whether
	// they OMITTED the courses or MISTYPED one of them. Returned wrapped with the
	// offending index + value; errors.Is still matches the sentinel.
	ErrOfferingCourseInvalid = errors.New("delivery: offering course_id must be a UUID")
	// ErrOfferingLabelRequired — label must be trim-non-empty.
	ErrOfferingLabelRequired = errors.New("delivery: offering label required")
	// ErrOfferingDeliveryTypeInvalid — delivery_type must be one of
	// {graduate, short, async}.
	ErrOfferingDeliveryTypeInvalid = errors.New("delivery: offering delivery_type invalid (want graduate|short|async)")
	// ErrOfferingCapacityNegative — capacity must be ≥ 0 (0 = unbounded).
	ErrOfferingCapacityNegative = errors.New("delivery: offering capacity must be ≥ 0 (0 = unbounded)")
	// ErrOfferingNotDraft — Launch requires DRAFT state.
	ErrOfferingNotDraft = errors.New("delivery: offering not in DRAFT state")
	// ErrOfferingNotLaunched — Start requires LAUNCHED state.
	ErrOfferingNotLaunched = errors.New("delivery: offering not in LAUNCHED state")
	// ErrOfferingNotRunning — Conclude requires RUNNING state.
	ErrOfferingNotRunning = errors.New("delivery: offering not in RUNNING state")
	// ErrSectionNameRequired — a section's name must be trim-non-empty.
	ErrSectionNameRequired = errors.New("delivery: section name required")
	// ErrSectionNotGraduate — sections exist only on a graduate-delivery offering
	// (an intra-cohort sub-group; short/async offerings have no cohort to split).
	ErrSectionNotGraduate = errors.New("delivery: sections require a graduate-delivery offering")
	// ErrSectionNotFound — the referenced section_id is not on this offering.
	ErrSectionNotFound = errors.New("delivery: section not found")
)

// -----------------------------------------------------------------------------
// Aggregate
// -----------------------------------------------------------------------------

// Offering is the delivery-instance aggregate root: a specific run of a
// reusable Course in one delivery mode.
//
// Soft delete via DeletedAt (set by Archive); transitions advance UpdatedAt.
// CourseIDs are opaque cross-aggregate course UUIDs (no FK). Offering→course is
// one-to-many: an offering bundles ≥1 course; CourseIDs[0] is the PRIMARY
// (denormalised into the write-only course_id column). Order preserved, deduped.
type Offering struct {
	ID        string
	TenantID  string
	CourseIDs []string
	// Sections are intra-cohort sub-groups (GRADUATE offerings only): each has
	// its own lead instructor / room / delivery dates but shares the cohort's
	// curriculum + gradebook. A child collection reached only via this root and
	// persisted inside the JSONB aggregate (no own table, no FK). Empty for
	// short/async offerings.
	Sections []Section
	// CompletionPolicy is the OFFERING-level completion + certification policy
	// (S2 — "cert = policy on top", plan W6). nil ⇒ none declared (the attached
	// course's own CertDefinition default applies). Distinct from the course
	// cert (which is DRAFT-locked + authored in A+); editable at any lifecycle
	// state and persisted in the Offering JSONB snapshot (no own table).
	CompletionPolicy *CompletionPolicy
	// CompletionRequirement declares WHICH components this offering needs before
	// it certifies anyone (CHO-2222, ADR-190 D1 "a per-course
	// CompletionRequirement declares which components this offering needs").
	// nil or empty ⇒ this offering declares no components, which is VACUOUSLY
	// satisfied, not unmet (the live shape of every offering predating the
	// type). Like CompletionPolicy above: a child of this root, persisted in the
	// Offering JSONB snapshot (no own table), editable at any lifecycle state.
	// See completion_requirement.go.
	CompletionRequirement *CompletionRequirement
	DeliveryType          DeliveryType
	Label                 string
	// Capacity is the seat budget; 0 = unbounded (e.g. async).
	Capacity    int
	State       OfferingState
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LaunchedAt  *time.Time
	ConcludedAt *time.Time
	ArchivedAt  *time.Time
	DeletedAt   *time.Time
}

// NewOfferingInput is the value-bag for NewOffering.
type NewOfferingInput struct {
	TenantID     string
	CourseIDs    []string
	DeliveryType DeliveryType
	Label        string
	Capacity     int
}

// canonicalCourseIDs trims, drops blanks, validates every entry as a UUID, and
// de-duplicates on the CANONICAL form while preserving the caller's order (the
// first occurrence wins → stable PRIMARY).
//
// Why validate here: CourseIDs is persisted inside the JSONB `data` column, so
// there is no typed DB guard on the list. The `offerings.course_id UUID NOT
// NULL` extract column is written from PrimaryCourseID(), so it guards
// CourseIDs[0] ONLY, and only by accident: CourseIDs[1:] reach the database
// inside the blob unchecked. Two live queries then cast that blob straight to
// uuid (adapter/repo/pg/cohort_roster.go SQLLearnerEnrolledInOffering, the
// ADR-234 cohort-authz linkage, and adapter/repo/pg/assessment.go OPEN_LINK
// visibility), so one bad non-primary entry 22P02s cohort authz + assessment
// visibility for the whole offering, far from the request that stored it. This
// constructor is the one chokepoint every creation path crosses.
//
// "Opaque" constrains the reference's MEANING (this domain never dereferences a
// course_id, per the no-Go-level-FK rule); it does not license an arbitrary SHAPE.
//
// It stores the CANONICAL form rather than the raw input on purpose. uuid.Parse
// is deliberately lenient: it accepts "urn:uuid:<uuid>", "{<uuid>}" and the
// 32-char dashless form, and Postgres accepts only two of those three (the urn:
// prefix is a 22P02 there). Validating with Parse but storing the raw text would
// leave the exact bug this guard exists to stop still reachable, just through a
// narrower door. Normalising to uuid.String() closes it, and makes dedup honest:
// on raw text, "X" and "urn:uuid:X" survive as two entries for one course.
//
// Blanks are DROPPED rather than rejected (the pre-existing contract): a blank
// never reaches the blob, so it cannot arm the cast, and an all-blank list still
// fails as ErrOfferingCourseRequired.
func canonicalCourseIDs(in []string) ([]string, error) {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for i, c := range in {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		u, err := uuid.Parse(c)
		if err != nil {
			// Index is into the caller's own array, so the 400 points at the
			// entry they typed, blanks and all.
			return nil, fmt.Errorf("%w (course_ids[%d]=%q)", ErrOfferingCourseInvalid, i, c)
		}
		canon := u.String()
		if seen[canon] {
			continue
		}
		seen[canon] = true
		out = append(out, canon)
	}
	return out, nil
}

// PrimaryCourseID is the denormalised "main" course (the first), used for the
// write-only course_id extract column + the legacy course_id wire field. Returns
// "" only for a (validation-prevented) empty aggregate.
func (o *Offering) PrimaryCourseID() string {
	if len(o.CourseIDs) == 0 {
		return ""
	}
	return o.CourseIDs[0]
}

// NewOffering constructs a DRAFT-state Offering with a UUIDv7 ID.
//
// Validation guards (rejected with a specific sentinel):
//   - tenant_id trim-non-empty
//   - course_ids — at least one trim-non-empty (deduped, order preserved)
//   - label trim-non-empty
//   - delivery_type one of {graduate, short, async}
//   - capacity ≥ 0 (0 = unbounded)
//
// delivery_type is set once here and is not mutated by any transition.
func NewOffering(in NewOfferingInput) (*Offering, error) {
	tenant := strings.TrimSpace(in.TenantID)
	if tenant == "" {
		return nil, ErrOfferingTenantRequired
	}
	courses, err := canonicalCourseIDs(in.CourseIDs)
	if err != nil {
		return nil, err
	}
	if len(courses) == 0 {
		return nil, ErrOfferingCourseRequired
	}
	label := strings.TrimSpace(in.Label)
	if label == "" {
		return nil, ErrOfferingLabelRequired
	}
	if !in.DeliveryType.IsValid() {
		return nil, ErrOfferingDeliveryTypeInvalid
	}
	if in.Capacity < 0 {
		return nil, ErrOfferingCapacityNegative
	}
	now := time.Now().UTC()
	return &Offering{
		ID:           NewUUIDv7(),
		TenantID:     tenant,
		CourseIDs:    courses,
		DeliveryType: in.DeliveryType,
		Label:        label,
		Capacity:     in.Capacity,
		State:        OfferingStateDraft,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// -----------------------------------------------------------------------------
// State transitions
// -----------------------------------------------------------------------------

// Launch transitions DRAFT → LAUNCHED and stamps LaunchedAt.
func (o *Offering) Launch() error {
	if o.State != OfferingStateDraft {
		return ErrOfferingNotDraft
	}
	now := time.Now().UTC()
	o.State = OfferingStateLaunched
	o.LaunchedAt = &now
	o.UpdatedAt = now
	return nil
}

// Start transitions LAUNCHED → RUNNING (delivery begins).
func (o *Offering) Start() error {
	if o.State != OfferingStateLaunched {
		return ErrOfferingNotLaunched
	}
	o.State = OfferingStateRunning
	o.UpdatedAt = time.Now().UTC()
	return nil
}

// Conclude transitions RUNNING → CONCLUDED and stamps ConcludedAt.
func (o *Offering) Conclude() error {
	if o.State != OfferingStateRunning {
		return ErrOfferingNotRunning
	}
	now := time.Now().UTC()
	o.State = OfferingStateConcluded
	o.ConcludedAt = &now
	o.UpdatedAt = now
	return nil
}

// Archive transitions to ARCHIVED from any state and soft-deletes the
// offering (sets ArchivedAt + DeletedAt). Idempotent — a second call on an
// already-archived offering is a no-op.
func (o *Offering) Archive() {
	if o.State == OfferingStateArchived {
		return
	}
	now := time.Now().UTC()
	o.State = OfferingStateArchived
	o.ArchivedAt = &now
	o.DeletedAt = &now
	o.UpdatedAt = now
}

// -----------------------------------------------------------------------------
// Sections (intra-cohort sub-groups; GRADUATE offerings only)
// -----------------------------------------------------------------------------

// Section is an intra-cohort sub-group of a GRADUATE Offering. It carries its
// own lead instructor / room / delivery window but SHARES the cohort's
// curriculum + gradebook (it varies only delivery logistics, per D2). A Section
// has NO independent lifecycle, NO own RLS, and NO cross-aggregate references —
// it is a child entity reached only through its Offering root and persisted
// inside the Offering's JSONB snapshot (no own table). LeadInstructorGCID is an
// opaque GCID (no Go-level FK), like every other cross-domain reference here.
type Section struct {
	SectionID          string
	Name               string
	LeadInstructorGCID string
	Room               string
	// StartDate / EndDate are the section's delivery window as ISO-8601 date
	// strings (YYYY-MM-DD), optional. Date-only (no clock/timezone) keeps the
	// "varies only delivery dates" invariant simple + extensible — timeslots can
	// be added later without a migration (the aggregate is JSONB).
	StartDate string
	EndDate   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AddSectionInput is the value-bag for Offering.AddSection. Only Name is
// required; the rest are optional delivery logistics.
type AddSectionInput struct {
	Name               string
	LeadInstructorGCID string
	Room               string
	StartDate          string
	EndDate            string
}

// AddSection appends an intra-cohort Section to a GRADUATE offering and returns
// it. Guards:
//   - the offering must be graduate-delivery (ErrSectionNotGraduate) — only a
//     cohort can be subdivided;
//   - name must be trim-non-empty (ErrSectionNameRequired).
//
// The Section gets a fresh UUIDv7 id; all string fields are trimmed; the
// section's CreatedAt/UpdatedAt and the offering's UpdatedAt are stamped from a
// single `now`. The section lives in the offering's JSONB aggregate (no table).
func (o *Offering) AddSection(in AddSectionInput) (*Section, error) {
	if o.DeliveryType != DeliveryTypeGraduate {
		return nil, ErrSectionNotGraduate
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, ErrSectionNameRequired
	}
	now := time.Now().UTC()
	sec := Section{
		SectionID:          NewUUIDv7(),
		Name:               name,
		LeadInstructorGCID: strings.TrimSpace(in.LeadInstructorGCID),
		Room:               strings.TrimSpace(in.Room),
		StartDate:          strings.TrimSpace(in.StartDate),
		EndDate:            strings.TrimSpace(in.EndDate),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	o.Sections = append(o.Sections, sec)
	o.UpdatedAt = now
	return &sec, nil
}

// RenameSection updates an existing section's name in place — the one mutable
// attribute that is cheap + safe to change without touching shared cohort
// state. Guards: newName trim-non-empty (ErrSectionNameRequired); sectionID must
// exist on this offering (ErrSectionNotFound). Bumps the section's + the
// offering's UpdatedAt.
func (o *Offering) RenameSection(sectionID, newName string) error {
	name := strings.TrimSpace(newName)
	if name == "" {
		return ErrSectionNameRequired
	}
	for i := range o.Sections {
		if o.Sections[i].SectionID == sectionID {
			now := time.Now().UTC()
			o.Sections[i].Name = name
			o.Sections[i].UpdatedAt = now
			o.UpdatedAt = now
			return nil
		}
	}
	return ErrSectionNotFound
}

// UpdateSectionInput is the value-bag for Offering.UpdateSection (R+ Phase-2 S4).
// Every field is optional-by-pointer: a nil field is left unchanged; a non-nil
// field is applied (trimmed). A provided Name must be trim-non-empty.
type UpdateSectionInput struct {
	Name               *string
	LeadInstructorGCID *string
	Room               *string
	StartDate          *string
	EndDate            *string
}

// UpdateSection edits an existing section's delivery logistics (name / lead /
// room / dates) in place, applying only the provided (non-nil) fields — so a
// caller can change just the room without touching the dates. Guards: section
// must exist (ErrSectionNotFound); a provided Name must be trim-non-empty
// (ErrSectionNameRequired). Bumps the section's + the offering's UpdatedAt and
// returns the updated section. The section lives in the Offering JSONB aggregate
// (no own table) — the caller persists via OfferingPort.Save.
func (o *Offering) UpdateSection(sectionID string, in UpdateSectionInput) (*Section, error) {
	for i := range o.Sections {
		if o.Sections[i].SectionID != sectionID {
			continue
		}
		if in.Name != nil {
			n := strings.TrimSpace(*in.Name)
			if n == "" {
				return nil, ErrSectionNameRequired
			}
			o.Sections[i].Name = n
		}
		if in.LeadInstructorGCID != nil {
			o.Sections[i].LeadInstructorGCID = strings.TrimSpace(*in.LeadInstructorGCID)
		}
		if in.Room != nil {
			o.Sections[i].Room = strings.TrimSpace(*in.Room)
		}
		if in.StartDate != nil {
			o.Sections[i].StartDate = strings.TrimSpace(*in.StartDate)
		}
		if in.EndDate != nil {
			o.Sections[i].EndDate = strings.TrimSpace(*in.EndDate)
		}
		now := time.Now().UTC()
		o.Sections[i].UpdatedAt = now
		o.UpdatedAt = now
		s := o.Sections[i]
		return &s, nil
	}
	return nil, ErrSectionNotFound
}

// -----------------------------------------------------------------------------
// Completion policy (offering-level "cert = policy on top", S2)
// -----------------------------------------------------------------------------

// ErrCompletionPolicyScoreRange — passing_score_pct must be in [0,100].
var ErrCompletionPolicyScoreRange = errors.New("delivery: completion policy passing_score_pct must be 0..100")

// CompletionPolicy is the OFFERING-level completion + certification policy (S2).
// It is the "cert = policy on top" layer (plan W6): the delivery decision for
// THIS offering — whether completing it awards a certificate, at what passing
// threshold, under what label — distinct from a Course's own DRAFT-locked
// CertDefinition. Rides in the Offering JSONB snapshot (no own table / migration).
type CompletionPolicy struct {
	// AwardsCertificate — whether completing this offering awards a certificate.
	AwardsCertificate bool
	// PassingScorePct — the passing threshold (0..100) a learner must meet.
	PassingScorePct int
	// CertTitle — an optional display label for the awarded certificate.
	CertTitle string
	UpdatedAt time.Time
}

// SetCompletionPolicyInput is the value-bag for Offering.SetCompletionPolicy.
type SetCompletionPolicyInput struct {
	AwardsCertificate bool
	PassingScorePct   int
	CertTitle         string
}

// SetCompletionPolicy sets or replaces the offering's completion policy. Guard:
// PassingScorePct must be in [0,100] (ErrCompletionPolicyScoreRange). CertTitle
// is trimmed. Bumps the policy's + the offering's UpdatedAt. Allowed in any
// lifecycle state (a delivery policy, not course content).
func (o *Offering) SetCompletionPolicy(in SetCompletionPolicyInput) error {
	if in.PassingScorePct < 0 || in.PassingScorePct > 100 {
		return ErrCompletionPolicyScoreRange
	}
	now := time.Now().UTC()
	o.CompletionPolicy = &CompletionPolicy{
		AwardsCertificate: in.AwardsCertificate,
		PassingScorePct:   in.PassingScorePct,
		CertTitle:         strings.TrimSpace(in.CertTitle),
		UpdatedAt:         now,
	}
	o.UpdatedAt = now
	return nil
}
