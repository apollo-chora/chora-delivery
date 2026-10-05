package wbl

import (
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// InternshipStatus represents the lifecycle state of an internship posting.
type InternshipStatus string

const (
	InternshipStatusDraft    InternshipStatus = "draft"
	InternshipStatusOpen     InternshipStatus = "open"
	InternshipStatusClosed   InternshipStatus = "closed"
	InternshipStatusArchived InternshipStatus = "archived"
)

// IsValid checks whether the InternshipStatus value is a known enum member.
func (s InternshipStatus) IsValid() bool {
	switch s {
	case InternshipStatusDraft, InternshipStatusOpen, InternshipStatusClosed,
		InternshipStatusArchived:
		return true
	}
	return false
}

// ApplicationStatus represents the lifecycle of an internship application.
type ApplicationStatus string

const (
	ApplicationStatusSubmitted   ApplicationStatus = "submitted"
	ApplicationStatusUnderReview ApplicationStatus = "under_review"
	ApplicationStatusApproved    ApplicationStatus = "approved"
	ApplicationStatusRejected    ApplicationStatus = "rejected"
)

// IsValid checks whether the ApplicationStatus value is a known enum member.
func (a ApplicationStatus) IsValid() bool {
	switch a {
	case ApplicationStatusSubmitted, ApplicationStatusUnderReview,
		ApplicationStatusApproved, ApplicationStatusRejected:
		return true
	}
	return false
}

// PlacementStatus represents the lifecycle state of a placement.
type PlacementStatus string

const (
	PlacementStatusActive    PlacementStatus = "active"
	PlacementStatusCompleted PlacementStatus = "completed"
	PlacementStatusWithdrawn PlacementStatus = "withdrawn"
)

// IsValid checks whether the PlacementStatus value is a known enum member.
func (p PlacementStatus) IsValid() bool {
	switch p {
	case PlacementStatusActive, PlacementStatusCompleted, PlacementStatusWithdrawn:
		return true
	}
	return false
}

// CapstoneStatus represents the lifecycle state of a capstone project.
type CapstoneStatus string

const (
	CapstoneStatusDraft     CapstoneStatus = "draft"
	CapstoneStatusActive    CapstoneStatus = "active"
	CapstoneStatusCompleted CapstoneStatus = "completed"
	CapstoneStatusArchived  CapstoneStatus = "archived"
)

// IsValid checks whether the CapstoneStatus value is a known enum member.
func (c CapstoneStatus) IsValid() bool {
	switch c {
	case CapstoneStatusDraft, CapstoneStatusActive, CapstoneStatusCompleted,
		CapstoneStatusArchived:
		return true
	}
	return false
}

// EndorsementLevel represents the proficiency level for a skill endorsement.
type EndorsementLevel string

const (
	EndorsementLevelBeginner     EndorsementLevel = "beginner"
	EndorsementLevelIntermediate EndorsementLevel = "intermediate"
	EndorsementLevelAdvanced     EndorsementLevel = "advanced"
	EndorsementLevelExpert       EndorsementLevel = "expert"
)

// IsValid checks whether the EndorsementLevel value is a known enum member.
func (e EndorsementLevel) IsValid() bool {
	switch e {
	case EndorsementLevelBeginner, EndorsementLevelIntermediate,
		EndorsementLevelAdvanced, EndorsementLevelExpert:
		return true
	}
	return false
}

// PartnerStatus represents the status of an industry partner.
type PartnerStatus string

const (
	PartnerStatusActive   PartnerStatus = "active"
	PartnerStatusInactive PartnerStatus = "inactive"
)

// IsValid checks whether the PartnerStatus value is a known enum member.
func (p PartnerStatus) IsValid() bool {
	switch p {
	case PartnerStatusActive, PartnerStatusInactive:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Domain Entities
// ---------------------------------------------------------------------------

// IndustryPartner represents an organization that provides internship opportunities.
type IndustryPartner struct {
	ID           uuid.UUID     `json:"id"`
	TenantID     uuid.UUID     `json:"tenant_id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	ContactEmail string        `json:"contact_email"`
	Website      string        `json:"website"`
	Status       PartnerStatus `json:"status"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	DeletedAt    *time.Time    `json:"deleted_at,omitempty"`
}

// Internship represents an internship posting created by admin/instructor.
type Internship struct {
	ID              uuid.UUID        `json:"id"`
	TenantID        uuid.UUID        `json:"tenant_id"`
	Title           string           `json:"title"`
	Description     string           `json:"description"`
	Status          InternshipStatus `json:"status"`
	PartnerID       uuid.UUID        `json:"partner_id"`
	Location        *string          `json:"location,omitempty"`
	MaxPositions    int              `json:"max_positions"`
	FilledPositions int              `json:"filled_positions"`
	StartsAt        *time.Time       `json:"starts_at,omitempty"`
	EndsAt          *time.Time       `json:"ends_at,omitempty"`
	RequiredSkills  []string         `json:"required_skills,omitempty"`
	CreatedByGCID   uuid.UUID        `json:"created_by_gcid"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	DeletedAt       *time.Time       `json:"deleted_at,omitempty"`
}

// InternshipApplication represents a learner's application for an internship.
type InternshipApplication struct {
	ID              uuid.UUID         `json:"id"`
	TenantID        uuid.UUID         `json:"tenant_id"`
	InternshipID    uuid.UUID         `json:"internship_id"`
	ApplicantGCID   uuid.UUID         `json:"applicant_gcid"`
	Status          ApplicationStatus `json:"status"`
	CoverLetter     string            `json:"cover_letter"`
	ReviewedByGCID  *uuid.UUID        `json:"reviewed_by_gcid,omitempty"`
	RejectionReason *string           `json:"rejection_reason,omitempty"`
	SubmittedAt     time.Time         `json:"submitted_at"`
	ReviewedAt      *time.Time        `json:"reviewed_at,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// Placement represents an active work placement linking a learner to an internship.
type Placement struct {
	ID               uuid.UUID       `json:"id"`
	TenantID         uuid.UUID       `json:"tenant_id"`
	InternshipID     uuid.UUID       `json:"internship_id"`
	LearnerGCID      uuid.UUID       `json:"learner_gcid"`
	SupervisorGCID   *uuid.UUID      `json:"supervisor_gcid,omitempty"`
	Status           PlacementStatus `json:"status"`
	StartsAt         *time.Time      `json:"starts_at,omitempty"`
	EndsAt           *time.Time      `json:"ends_at,omitempty"`
	TotalHoursLogged float64         `json:"total_hours_logged"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	DeletedAt        *time.Time      `json:"deleted_at,omitempty"`
}

// PlacementSupervisor links a supervisor to a placement (stored inline on Placement).
// This is a value object — not a separate aggregate.

// WorkLogEntry represents a single work log entry within a placement.
type WorkLogEntry struct {
	ID                 uuid.UUID `json:"id"`
	TenantID           uuid.UUID `json:"tenant_id"`
	PlacementID        uuid.UUID `json:"placement_id"`
	Date               time.Time `json:"date"`
	Hours              float64   `json:"hours"`
	Description        string    `json:"description"`
	SkillsApplied      []string  `json:"skills_applied,omitempty"`
	SupervisorApproved bool      `json:"supervisor_approved"`
	CreatedAt          time.Time `json:"created_at"`
}

// SkillEndorsement represents a supervisor's endorsement of a skill for a learner.
type SkillEndorsement struct {
	ID           uuid.UUID        `json:"id"`
	TenantID     uuid.UUID        `json:"tenant_id"`
	PlacementID  uuid.UUID        `json:"placement_id"`
	EndorserGCID uuid.UUID        `json:"endorser_gcid"`
	SkillName    string           `json:"skill_name"`
	Level        EndorsementLevel `json:"level"`
	Comments     *string          `json:"comments,omitempty"`
	EndorsedAt   time.Time        `json:"endorsed_at"`
}

// CapstoneProject represents a capstone project associated with work-based learning.
type CapstoneProject struct {
	ID             uuid.UUID      `json:"id"`
	TenantID       uuid.UUID      `json:"tenant_id"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	Status         CapstoneStatus `json:"status"`
	PlacementID    *uuid.UUID     `json:"placement_id,omitempty"`
	RequiredSkills []string       `json:"required_skills,omitempty"`
	CreatedByGCID  uuid.UUID      `json:"created_by_gcid"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      *time.Time     `json:"deleted_at,omitempty"`
}

// CapstoneSubmission represents a learner's submission for a capstone project.
type CapstoneSubmission struct {
	ID            uuid.UUID `json:"id"`
	TenantID      uuid.UUID `json:"tenant_id"`
	CapstoneID    uuid.UUID `json:"capstone_id"`
	SubmitterGCID uuid.UUID `json:"submitter_gcid"`
	SubmissionURL string    `json:"submission_url"`
	Notes         string    `json:"notes"`
	SubmittedAt   time.Time `json:"submitted_at"`
}

// ---------------------------------------------------------------------------
// WBL Log Enums
// ---------------------------------------------------------------------------

// WBLLogStatus represents the approval state of a WBL log entry.
type WBLLogStatus string

const (
	WBLLogStatusPendingApproval WBLLogStatus = "pending_approval"
	WBLLogStatusApproved        WBLLogStatus = "approved"
	WBLLogStatusRejected        WBLLogStatus = "rejected"
)

// ---------------------------------------------------------------------------
// WBL Log Entity
// ---------------------------------------------------------------------------

// WBLLog represents a work-based learning log entry submitted by a learner
// for supervisor approval.
type WBLLog struct {
	ID                  uuid.UUID    `json:"id"`
	TenantID            uuid.UUID    `json:"tenant_id"`
	GCID                uuid.UUID    `json:"gcid"`
	SupervisorGCID      uuid.UUID    `json:"supervisor_gcid"`
	ActivityDescription string       `json:"activity_description"`
	DurationHours       float64      `json:"duration_hours"`
	TopicNodeIDs        []uuid.UUID  `json:"topic_node_ids,omitempty"`
	Status              WBLLogStatus `json:"status"`
	SupervisorFeedback  *string      `json:"supervisor_feedback,omitempty"`
	SubmittedAt         time.Time    `json:"submitted_at"`
	ReviewedAt          *time.Time   `json:"reviewed_at,omitempty"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
	DeletedAt           *time.Time   `json:"deleted_at,omitempty"`
}
