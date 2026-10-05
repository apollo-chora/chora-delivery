package training_admin

import (
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// SessionStatus represents the lifecycle state of a training session.
type SessionStatus string

const (
	SessionStatusDraft      SessionStatus = "draft"
	SessionStatusScheduled  SessionStatus = "scheduled"
	SessionStatusInProgress SessionStatus = "in_progress"
	SessionStatusCompleted  SessionStatus = "completed"
	SessionStatusCancelled  SessionStatus = "cancelled"
)

// IsValid checks whether the SessionStatus value is a known enum member.
func (s SessionStatus) IsValid() bool {
	switch s {
	case SessionStatusDraft, SessionStatusScheduled, SessionStatusInProgress,
		SessionStatusCompleted, SessionStatusCancelled:
		return true
	}
	return false
}

// DeliveryMode represents how the training session is delivered.
type DeliveryMode string

const (
	DeliveryModePhysical DeliveryMode = "physical"
	DeliveryModeVirtual  DeliveryMode = "virtual"
	DeliveryModeHybrid   DeliveryMode = "hybrid"
)

// IsValid checks whether the DeliveryMode value is a known enum member.
func (d DeliveryMode) IsValid() bool {
	switch d {
	case DeliveryModePhysical, DeliveryModeVirtual, DeliveryModeHybrid:
		return true
	}
	return false
}

// AttendanceStatus represents whether a learner attended a session.
type AttendanceStatus string

const (
	AttendanceStatusPresent AttendanceStatus = "present"
	AttendanceStatusLate    AttendanceStatus = "late"
	AttendanceStatusAbsent  AttendanceStatus = "absent"
	AttendanceStatusExcused AttendanceStatus = "excused"
)

// IsValid checks whether the AttendanceStatus value is a known enum member.
func (a AttendanceStatus) IsValid() bool {
	switch a {
	case AttendanceStatusPresent, AttendanceStatusLate, AttendanceStatusAbsent,
		AttendanceStatusExcused:
		return true
	}
	return false
}

// CheckInMethod represents how attendance was recorded.
type CheckInMethod string

const (
	CheckInMethodManual     CheckInMethod = "manual"
	CheckInMethodQRScan     CheckInMethod = "qr_scan"
	CheckInMethodNFC        CheckInMethod = "nfc"
	CheckInMethodGeoCheckin CheckInMethod = "geo_checkin"
)

// IsValid checks whether the CheckInMethod value is a known enum member.
func (c CheckInMethod) IsValid() bool {
	switch c {
	case CheckInMethodManual, CheckInMethodQRScan, CheckInMethodNFC,
		CheckInMethodGeoCheckin:
		return true
	}
	return false
}

// ApplicationStatus represents the lifecycle of a training application.
type ApplicationStatus string

const (
	ApplicationStatusDraft       ApplicationStatus = "draft"
	ApplicationStatusSubmitted   ApplicationStatus = "submitted"
	ApplicationStatusUnderReview ApplicationStatus = "under_review"
	ApplicationStatusApproved    ApplicationStatus = "approved"
	ApplicationStatusRejected    ApplicationStatus = "rejected"
	ApplicationStatusWaitlisted  ApplicationStatus = "waitlisted"
)

// IsValid checks whether the ApplicationStatus value is a known enum member.
func (a ApplicationStatus) IsValid() bool {
	switch a {
	case ApplicationStatusDraft, ApplicationStatusSubmitted, ApplicationStatusUnderReview,
		ApplicationStatusApproved, ApplicationStatusRejected, ApplicationStatusWaitlisted:
		return true
	}
	return false
}

// RequestType represents the type of trainee request.
type RequestType string

const (
	RequestTypeDeferral   RequestType = "deferral"
	RequestTypeWithdrawal RequestType = "withdrawal"
	RequestTypeMakeup     RequestType = "makeup"
	RequestTypeTransfer   RequestType = "transfer"
)

// IsValid checks whether the RequestType value is a known enum member.
func (r RequestType) IsValid() bool {
	switch r {
	case RequestTypeDeferral, RequestTypeWithdrawal, RequestTypeMakeup,
		RequestTypeTransfer:
		return true
	}
	return false
}

// RequestStatus represents the lifecycle of a trainee request.
type RequestStatus string

const (
	RequestStatusSubmitted   RequestStatus = "submitted"
	RequestStatusUnderReview RequestStatus = "under_review"
	RequestStatusApproved    RequestStatus = "approved"
	RequestStatusRejected    RequestStatus = "rejected"
)

// IsValid checks whether the RequestStatus value is a known enum member.
func (r RequestStatus) IsValid() bool {
	switch r {
	case RequestStatusSubmitted, RequestStatusUnderReview,
		RequestStatusApproved, RequestStatusRejected:
		return true
	}
	return false
}

// ProgramStatus represents the lifecycle state of a certificate program.
type ProgramStatus string

const (
	ProgramStatusDraft    ProgramStatus = "draft"
	ProgramStatusActive   ProgramStatus = "active"
	ProgramStatusArchived ProgramStatus = "archived"
)

// IsValid checks whether the ProgramStatus value is a known enum member.
func (p ProgramStatus) IsValid() bool {
	switch p {
	case ProgramStatusDraft, ProgramStatusActive, ProgramStatusArchived:
		return true
	}
	return false
}

// EnrollmentStatus represents a learner's enrollment state in a certificate program.
type EnrollmentStatus string

const (
	EnrollmentStatusEnrolled   EnrollmentStatus = "enrolled"
	EnrollmentStatusInProgress EnrollmentStatus = "in_progress"
	EnrollmentStatusCompleted  EnrollmentStatus = "completed"
	EnrollmentStatusWithdrawn  EnrollmentStatus = "withdrawn"
)

// IsValid checks whether the EnrollmentStatus value is a known enum member.
func (e EnrollmentStatus) IsValid() bool {
	switch e {
	case EnrollmentStatusEnrolled, EnrollmentStatusInProgress,
		EnrollmentStatusCompleted, EnrollmentStatusWithdrawn:
		return true
	}
	return false
}

// RecurrenceType represents how often a schedule repeats.
type RecurrenceType string

const (
	RecurrenceTypeOneOff   RecurrenceType = "one_off"
	RecurrenceTypeWeekly   RecurrenceType = "weekly"
	RecurrenceTypeBiweekly RecurrenceType = "biweekly"
)

// IsValid checks whether the RecurrenceType value is a known enum member.
func (r RecurrenceType) IsValid() bool {
	switch r {
	case RecurrenceTypeOneOff, RecurrenceTypeWeekly, RecurrenceTypeBiweekly:
		return true
	}
	return false
}

// DayOfWeek represents a day of the week.
type DayOfWeek string

const (
	DayOfWeekMon DayOfWeek = "mon"
	DayOfWeekTue DayOfWeek = "tue"
	DayOfWeekWed DayOfWeek = "wed"
	DayOfWeekThu DayOfWeek = "thu"
	DayOfWeekFri DayOfWeek = "fri"
	DayOfWeekSat DayOfWeek = "sat"
	DayOfWeekSun DayOfWeek = "sun"
)

// IsValid checks whether the DayOfWeek value is a known enum member.
func (d DayOfWeek) IsValid() bool {
	switch d {
	case DayOfWeekMon, DayOfWeekTue, DayOfWeekWed, DayOfWeekThu,
		DayOfWeekFri, DayOfWeekSat, DayOfWeekSun:
		return true
	}
	return false
}

// AssessmentType represents the type of assessment.
type AssessmentType string

const (
	AssessmentTypeQuiz       AssessmentType = "quiz"
	AssessmentTypeExam       AssessmentType = "exam"
	AssessmentTypePractical  AssessmentType = "practical"
	AssessmentTypeAssignment AssessmentType = "assignment"
)

// IsValid checks whether the AssessmentType value is a known enum member.
func (a AssessmentType) IsValid() bool {
	switch a {
	case AssessmentTypeQuiz, AssessmentTypeExam, AssessmentTypePractical,
		AssessmentTypeAssignment:
		return true
	}
	return false
}

// CertificateRequestStatus represents the lifecycle of a certificate request.
type CertificateRequestStatus string

const (
	CertReqStatusSubmitted   CertificateRequestStatus = "submitted"
	CertReqStatusUnderReview CertificateRequestStatus = "under_review"
	CertReqStatusApproved    CertificateRequestStatus = "approved"
	CertReqStatusRejected    CertificateRequestStatus = "rejected"
	CertReqStatusIssued      CertificateRequestStatus = "issued"
)

// IsValid checks whether the CertificateRequestStatus value is a known enum member.
func (c CertificateRequestStatus) IsValid() bool {
	switch c {
	case CertReqStatusSubmitted, CertReqStatusUnderReview, CertReqStatusApproved,
		CertReqStatusRejected, CertReqStatusIssued:
		return true
	}
	return false
}

// AppealType represents the type of student appeal.
type AppealType string

const (
	AppealTypeGradeReview          AppealType = "grade_review"
	AppealTypeAttendanceCorrection AppealType = "attendance_correction"
	AppealTypeLateSubmission       AppealType = "late_submission"
	AppealTypeOther                AppealType = "other"
)

// IsValid checks whether the AppealType value is a known enum member.
func (a AppealType) IsValid() bool {
	switch a {
	case AppealTypeGradeReview, AppealTypeAttendanceCorrection,
		AppealTypeLateSubmission, AppealTypeOther:
		return true
	}
	return false
}

// AppealStatus represents the lifecycle of a student appeal.
type AppealStatus string

const (
	AppealStatusSubmitted   AppealStatus = "submitted"
	AppealStatusUnderReview AppealStatus = "under_review"
	AppealStatusUpheld      AppealStatus = "upheld"
	AppealStatusOverturned  AppealStatus = "overturned"
	AppealStatusDismissed   AppealStatus = "dismissed"
)

// IsValid checks whether the AppealStatus value is a known enum member.
func (a AppealStatus) IsValid() bool {
	switch a {
	case AppealStatusSubmitted, AppealStatusUnderReview, AppealStatusUpheld,
		AppealStatusOverturned, AppealStatusDismissed:
		return true
	}
	return false
}

// DisplayMode represents how a signage device renders content.
type DisplayMode string

const (
	DisplayModeLeaderboard   DisplayMode = "leaderboard"
	DisplayModeTimetable     DisplayMode = "timetable"
	DisplayModeAnnouncements DisplayMode = "announcements"
	DisplayModeMixed         DisplayMode = "mixed"
)

// IsValid checks whether the DisplayMode value is a known enum member.
func (d DisplayMode) IsValid() bool {
	switch d {
	case DisplayModeLeaderboard, DisplayModeTimetable,
		DisplayModeAnnouncements, DisplayModeMixed:
		return true
	}
	return false
}

// DeviceStatus represents the operational state of a signage device.
type DeviceStatus string

const (
	DeviceStatusOnline      DeviceStatus = "online"
	DeviceStatusOffline     DeviceStatus = "offline"
	DeviceStatusMaintenance DeviceStatus = "maintenance"
)

// IsValid checks whether the DeviceStatus value is a known enum member.
func (d DeviceStatus) IsValid() bool {
	switch d {
	case DeviceStatusOnline, DeviceStatusOffline, DeviceStatusMaintenance:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Domain Entities
// ---------------------------------------------------------------------------

// TrainingSession is the aggregate root for instructor-led training sessions.
type TrainingSession struct {
	ID             uuid.UUID      `json:"id"`
	TenantID       uuid.UUID      `json:"tenant_id"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	Status         SessionStatus  `json:"status"`
	DeliveryMode   DeliveryMode   `json:"delivery_mode"`
	LockedPathID   *uuid.UUID     `json:"locked_path_id,omitempty"`
	InstructorGCID uuid.UUID      `json:"instructor_gcid"`
	MaxCapacity    int            `json:"max_capacity"`
	EnrolledCount  int            `json:"enrolled_count"`
	EnrollmentOpen bool           `json:"enrollment_open"`
	StartsAt       *time.Time     `json:"starts_at,omitempty"`
	EndsAt         *time.Time     `json:"ends_at,omitempty"`
	Location       *string        `json:"location,omitempty"`
	ClassSectionID *uuid.UUID     `json:"class_section_id,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	CreatedByGCID  uuid.UUID      `json:"created_by_gcid"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      *time.Time     `json:"deleted_at,omitempty"`
}

// Attendance is an append-only record of a learner's attendance at a session.
type Attendance struct {
	ID                uuid.UUID        `json:"id"`
	TenantID          uuid.UUID        `json:"tenant_id"`
	TrainingSessionID uuid.UUID        `json:"training_session_id"`
	LearnerGCID       uuid.UUID        `json:"learner_gcid"`
	Status            AttendanceStatus `json:"status"`
	CheckInMethod     CheckInMethod    `json:"check_in_method"`
	SessionDate       time.Time        `json:"session_date"`
	MarkedByGCID      uuid.UUID        `json:"marked_by_gcid"`
	Notes             *string          `json:"notes,omitempty"`
	RecordedAt        time.Time        `json:"recorded_at"`
}

// Schedule represents a recurring or one-off schedule for a training session.
type Schedule struct {
	ID                uuid.UUID      `json:"id"`
	TrainingSessionID uuid.UUID      `json:"training_session_id"`
	DayOfWeek         DayOfWeek      `json:"day_of_week"`
	StartTime         string         `json:"start_time"`
	EndTime           string         `json:"end_time"`
	Recurrence        RecurrenceType `json:"recurrence"`
	EffectiveFrom     time.Time      `json:"effective_from"`
	EffectiveUntil    *time.Time     `json:"effective_until,omitempty"`
	Location          *string        `json:"location,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
}

// TrainingApplication represents a learner's application to join a training session.
type TrainingApplication struct {
	ID                uuid.UUID         `json:"id"`
	TenantID          uuid.UUID         `json:"tenant_id"`
	GCID              uuid.UUID         `json:"gcid"`
	TrainingSessionID uuid.UUID         `json:"training_session_id"`
	Status            ApplicationStatus `json:"status"`
	ApplicationText   string            `json:"application_text"`
	SubmittedAt       *time.Time        `json:"submitted_at,omitempty"`
	ReviewedByGCID    *uuid.UUID        `json:"reviewed_by_gcid,omitempty"`
	ReviewedAt        *time.Time        `json:"reviewed_at,omitempty"`
	RejectionReason   *string           `json:"rejection_reason,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// TraineeRequest represents a learner's request (deferral, withdrawal, makeup, transfer).
type TraineeRequest struct {
	ID                  uuid.UUID     `json:"id"`
	TenantID            uuid.UUID     `json:"tenant_id"`
	GCID                uuid.UUID     `json:"gcid"`
	TrainingSessionID   uuid.UUID     `json:"training_session_id"`
	RequestType         RequestType   `json:"request_type"`
	Status              RequestStatus `json:"status"`
	Reason              string        `json:"reason"`
	SupportingDocuments []string      `json:"supporting_documents,omitempty"`
	TargetSessionID     *uuid.UUID    `json:"target_session_id,omitempty"`
	ReviewedByGCID      *uuid.UUID    `json:"reviewed_by_gcid,omitempty"`
	ReviewedAt          *time.Time    `json:"reviewed_at,omitempty"`
	ResolutionNotes     *string       `json:"resolution_notes,omitempty"`
	CreatedAt           time.Time     `json:"created_at"`
	UpdatedAt           time.Time     `json:"updated_at"`
}

// CertificateProgram represents a structured certification path composed of locked paths.
type CertificateProgram struct {
	ID                     uuid.UUID     `json:"id"`
	TenantID               uuid.UUID     `json:"tenant_id"`
	Title                  string        `json:"title"`
	Description            string        `json:"description"`
	Status                 ProgramStatus `json:"status"`
	PathIDs                []uuid.UUID   `json:"path_ids"`
	TotalPaths             int           `json:"total_paths"`
	EstimatedDurationHours *float64      `json:"estimated_duration_hours,omitempty"`
	CertificateTemplate    *string       `json:"certificate_template,omitempty"`
	CreatedByGCID          uuid.UUID     `json:"created_by_gcid"`
	CreatedAt              time.Time     `json:"created_at"`
	UpdatedAt              time.Time     `json:"updated_at"`
	DeletedAt              *time.Time    `json:"deleted_at,omitempty"`
}

// ProgramEnrollment represents a learner's enrollment in a certificate program.
type ProgramEnrollment struct {
	ID                  uuid.UUID        `json:"id"`
	TenantID            uuid.UUID        `json:"tenant_id"`
	GCID                uuid.UUID        `json:"gcid"`
	ProgramID           uuid.UUID        `json:"program_id"`
	Status              EnrollmentStatus `json:"status"`
	CompletedPathIDs    []uuid.UUID      `json:"completed_path_ids,omitempty"`
	TotalPaths          int              `json:"total_paths"`
	ProgressPct         float64          `json:"progress_pct"`
	EnrolledAt          time.Time        `json:"enrolled_at"`
	CompletedAt         *time.Time       `json:"completed_at,omitempty"`
	CertificateIssuedAt *time.Time       `json:"certificate_issued_at,omitempty"`
	CertificateID       *uuid.UUID       `json:"certificate_id,omitempty"`
}

// AttendanceSummary is a read model for attendance statistics.
type AttendanceSummary struct {
	TotalSessions  int     `json:"total_sessions"`
	PresentCount   int     `json:"present_count"`
	LateCount      int     `json:"late_count"`
	AbsentCount    int     `json:"absent_count"`
	ExcusedCount   int     `json:"excused_count"`
	AttendanceRate float64 `json:"attendance_rate"`
}

// ---------------------------------------------------------------------------
// Student Portal Entities
// ---------------------------------------------------------------------------

// LearnerResult stores assessment/exam results for a trainee.
type LearnerResult struct {
	ID             uuid.UUID      `json:"id"`
	TenantID       uuid.UUID      `json:"tenant_id"`
	SessionID      uuid.UUID      `json:"session_id"`
	LearnerID      uuid.UUID      `json:"learner_id"`
	AssessmentType AssessmentType `json:"assessment_type"`
	Score          float64        `json:"score"`
	MaxScore       float64        `json:"max_score"`
	Grade          *string        `json:"grade,omitempty"`
	Remarks        *string        `json:"remarks,omitempty"`
	GradedBy       *uuid.UUID     `json:"graded_by,omitempty"`
	GradedAt       *time.Time     `json:"graded_at,omitempty"`
	Published      bool           `json:"published"`
	PublishedAt    *time.Time     `json:"published_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      *time.Time     `json:"deleted_at,omitempty"`
}

// AttendanceRecord is an aggregated attendance view for a learner in a session.
type AttendanceRecord struct {
	ID             uuid.UUID  `json:"id"`
	TenantID       uuid.UUID  `json:"tenant_id"`
	LearnerID      uuid.UUID  `json:"learner_id"`
	SessionID      uuid.UUID  `json:"session_id"`
	TotalSessions  int        `json:"total_sessions"`
	Attended       int        `json:"attended"`
	Late           int        `json:"late"`
	Absent         int        `json:"absent"`
	Excused        int        `json:"excused"`
	AttendancePct  float64    `json:"attendance_pct"`
	LastRecordedAt *time.Time `json:"last_recorded_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
}

// CertificateRequest represents a learner's request for a certificate.
type CertificateRequest struct {
	ID              uuid.UUID                `json:"id"`
	TenantID        uuid.UUID                `json:"tenant_id"`
	LearnerID       uuid.UUID                `json:"learner_id"`
	ProgramID       uuid.UUID                `json:"program_id"`
	Status          CertificateRequestStatus `json:"status"`
	ReviewerID      *uuid.UUID               `json:"reviewer_id,omitempty"`
	ReviewedAt      *time.Time               `json:"reviewed_at,omitempty"`
	CertificateURL  *string                  `json:"certificate_url,omitempty"`
	RejectionReason *string                  `json:"rejection_reason,omitempty"`
	CreatedAt       time.Time                `json:"created_at"`
	UpdatedAt       time.Time                `json:"updated_at"`
	DeletedAt       *time.Time               `json:"deleted_at,omitempty"`
}

// StudentAppeal represents a learner's appeal against a result.
type StudentAppeal struct {
	ID            uuid.UUID    `json:"id"`
	TenantID      uuid.UUID    `json:"tenant_id"`
	LearnerID     uuid.UUID    `json:"learner_id"`
	ResultID      uuid.UUID    `json:"result_id"`
	AppealType    AppealType   `json:"appeal_type"`
	Reason        string       `json:"reason"`
	EvidenceURLs  []string     `json:"evidence_urls,omitempty"`
	Status        AppealStatus `json:"status"`
	ReviewerID    *uuid.UUID   `json:"reviewer_id,omitempty"`
	ReviewerNotes *string      `json:"reviewer_notes,omitempty"`
	ReviewedAt    *time.Time   `json:"reviewed_at,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	DeletedAt     *time.Time   `json:"deleted_at,omitempty"`
}

// ResultsSummary is a read model for aggregated learner results.
type ResultsSummary struct {
	LearnerID     uuid.UUID                    `json:"learner_id"`
	TotalResults  int                          `json:"total_results"`
	AverageScore  float64                      `json:"average_score"`
	AveragePct    float64                      `json:"average_pct"`
	ResultsByType map[string]ResultTypeSummary `json:"results_by_type,omitempty"`
}

// ResultTypeSummary contains stats for a single assessment type.
type ResultTypeSummary struct {
	Count        int     `json:"count"`
	AverageScore float64 `json:"average_score"`
}

// ---------------------------------------------------------------------------
// Digital Signage Entities
// ---------------------------------------------------------------------------

// SignageDevice represents a digital signage display device.
type SignageDevice struct {
	ID              uuid.UUID      `json:"id"`
	TenantID        uuid.UUID      `json:"tenant_id"`
	DeviceName      string         `json:"device_name"`
	DeviceToken     string         `json:"device_token"`
	VenueID         *uuid.UUID     `json:"venue_id,omitempty"`
	RoomID          *uuid.UUID     `json:"room_id,omitempty"`
	DisplayMode     DisplayMode    `json:"display_mode"`
	Status          DeviceStatus   `json:"status"`
	LastHeartbeatAt *time.Time     `json:"last_heartbeat_at,omitempty"`
	RegisteredBy    uuid.UUID      `json:"registered_by"`
	Config          map[string]any `json:"config,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       *time.Time     `json:"deleted_at,omitempty"`
}

// SignageContent represents aggregated content for a signage device.
type SignageContent struct {
	DeviceToken   string              `json:"device_token"`
	DisplayMode   DisplayMode         `json:"display_mode"`
	Leaderboard   []LeaderboardEntry  `json:"leaderboard,omitempty"`
	Timetable     []TimetableEntry    `json:"timetable,omitempty"`
	Announcements []AnnouncementEntry `json:"announcements,omitempty"`
	GeneratedAt   time.Time           `json:"generated_at"`
}

// LeaderboardEntry is a single entry in a signage leaderboard.
type LeaderboardEntry struct {
	Rank        int       `json:"rank"`
	LearnerID   uuid.UUID `json:"learner_id"`
	DisplayName string    `json:"display_name"`
	Score       float64   `json:"score"`
}

// TimetableEntry is a single entry in a signage timetable.
type TimetableEntry struct {
	SessionTitle string  `json:"session_title"`
	DayOfWeek    string  `json:"day_of_week"`
	StartTime    string  `json:"start_time"`
	EndTime      string  `json:"end_time"`
	Location     *string `json:"location,omitempty"`
}

// AnnouncementEntry is a single announcement for signage display.
type AnnouncementEntry struct {
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// PageInfo contains cursor-based pagination metadata.
type PageInfo struct {
	NextCursor *string `json:"next_cursor,omitempty"`
	HasNext    bool    `json:"has_next"`
}
