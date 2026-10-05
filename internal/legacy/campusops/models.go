package campusops

import (
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// TermType represents the type of academic term.
type TermType string

const (
	TermTypeSemester  TermType = "semester"
	TermTypeTrimester TermType = "trimester"
	TermTypeQuarter   TermType = "quarter"
	TermTypeSummer    TermType = "summer"
	TermTypeCustom    TermType = "custom"
)

// IsValid checks whether the TermType value is a known enum member.
func (t TermType) IsValid() bool {
	switch t {
	case TermTypeSemester, TermTypeTrimester, TermTypeQuarter,
		TermTypeSummer, TermTypeCustom:
		return true
	}
	return false
}

// RoomType represents the type of room.
type RoomType string

const (
	RoomTypeClassroom  RoomType = "classroom"
	RoomTypeLab        RoomType = "lab"
	RoomTypeAuditorium RoomType = "auditorium"
	RoomTypeConference RoomType = "conference"
	RoomTypeStudio     RoomType = "studio"
	RoomTypeOther      RoomType = "other"
)

// IsValid checks whether the RoomType value is a known enum member.
func (r RoomType) IsValid() bool {
	switch r {
	case RoomTypeClassroom, RoomTypeLab, RoomTypeAuditorium,
		RoomTypeConference, RoomTypeStudio, RoomTypeOther:
		return true
	}
	return false
}

// SectionStatus represents the lifecycle state of a class section.
type SectionStatus string

const (
	SectionStatusActive    SectionStatus = "active"
	SectionStatusCancelled SectionStatus = "cancelled"
	SectionStatusCompleted SectionStatus = "completed"
)

// IsValid checks whether the SectionStatus value is a known enum member.
func (s SectionStatus) IsValid() bool {
	switch s {
	case SectionStatusActive, SectionStatusCancelled, SectionStatusCompleted:
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
	case AttendanceStatusPresent, AttendanceStatusLate,
		AttendanceStatusAbsent, AttendanceStatusExcused:
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
	case CheckInMethodManual, CheckInMethodQRScan,
		CheckInMethodNFC, CheckInMethodGeoCheckin:
		return true
	}
	return false
}

// BookingStatus represents the lifecycle state of a facility booking.
type BookingStatus string

const (
	BookingStatusPending   BookingStatus = "pending"
	BookingStatusConfirmed BookingStatus = "confirmed"
	BookingStatusCancelled BookingStatus = "cancelled"
)

// IsValid checks whether the BookingStatus value is a known enum member.
func (b BookingStatus) IsValid() bool {
	switch b {
	case BookingStatusPending, BookingStatusConfirmed, BookingStatusCancelled:
		return true
	}
	return false
}

// EnrollmentStatus represents the lifecycle state of a section enrollment.
type EnrollmentStatus string

const (
	EnrollmentStatusEnrolled   EnrollmentStatus = "enrolled"
	EnrollmentStatusWaitlisted EnrollmentStatus = "waitlisted"
	EnrollmentStatusDropped    EnrollmentStatus = "dropped"
	EnrollmentStatusCompleted  EnrollmentStatus = "completed"
)

// IsValid checks whether the EnrollmentStatus value is a known enum member.
func (e EnrollmentStatus) IsValid() bool {
	switch e {
	case EnrollmentStatusEnrolled, EnrollmentStatusWaitlisted,
		EnrollmentStatusDropped, EnrollmentStatusCompleted:
		return true
	}
	return false
}

// PublicationStatus represents the lifecycle state of a timetable publication.
type PublicationStatus string

const (
	PublicationStatusDraft      PublicationStatus = "draft"
	PublicationStatusPublished  PublicationStatus = "published"
	PublicationStatusSuperseded PublicationStatus = "superseded"
)

// IsValid checks whether the PublicationStatus value is a known enum member.
func (p PublicationStatus) IsValid() bool {
	switch p {
	case PublicationStatusDraft, PublicationStatusPublished, PublicationStatusSuperseded:
		return true
	}
	return false
}

// RecurrenceType represents the recurrence pattern for a time slot.
type RecurrenceType string

const (
	RecurrenceTypeWeekly   RecurrenceType = "weekly"
	RecurrenceTypeBiweekly RecurrenceType = "biweekly"
	RecurrenceTypeOnce     RecurrenceType = "once"
)

// IsValid checks whether the RecurrenceType value is a known enum member.
func (r RecurrenceType) IsValid() bool {
	switch r {
	case RecurrenceTypeWeekly, RecurrenceTypeBiweekly, RecurrenceTypeOnce:
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

// ---------------------------------------------------------------------------
// Domain Entities
// ---------------------------------------------------------------------------

// AcademicTerm represents a semester, trimester, quarter, or custom term period.
type AcademicTerm struct {
	ID                    uuid.UUID  `json:"id"`
	TenantID              uuid.UUID  `json:"tenant_id"`
	Name                  string     `json:"name"`
	TermType              TermType   `json:"term_type"`
	StartsAt              time.Time  `json:"starts_at"`
	EndsAt                time.Time  `json:"ends_at"`
	EnrollmentWindowStart *time.Time `json:"enrollment_window_start,omitempty"`
	EnrollmentWindowEnd   *time.Time `json:"enrollment_window_end,omitempty"`
	IsActive              bool       `json:"is_active"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	DeletedAt             *time.Time `json:"deleted_at,omitempty"`
}

// Venue represents a physical campus location that contains rooms.
type Venue struct {
	ID        uuid.UUID  `json:"id"`
	TenantID  uuid.UUID  `json:"tenant_id"`
	Name      string     `json:"name"`
	Address   *string    `json:"address,omitempty"`
	Campus    *string    `json:"campus,omitempty"`
	Timezone  *string    `json:"timezone,omitempty"`
	IsActive  bool       `json:"is_active"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// Room represents a room within a venue.
type Room struct {
	ID        uuid.UUID  `json:"id"`
	TenantID  uuid.UUID  `json:"tenant_id"`
	VenueID   uuid.UUID  `json:"venue_id"`
	Name      string     `json:"name"`
	RoomCode  string     `json:"room_code"`
	Capacity  int        `json:"capacity"`
	RoomType  RoomType   `json:"room_type"`
	Floor     *int       `json:"floor,omitempty"`
	Building  *string    `json:"building,omitempty"`
	Amenities []string   `json:"amenities,omitempty"`
	IsActive  bool       `json:"is_active"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// ClassSection links a training session to campus scheduling — scoped to an
// academic term with an assigned room and instructor.
type ClassSection struct {
	ID                uuid.UUID     `json:"id"`
	TenantID          uuid.UUID     `json:"tenant_id"`
	SectionCode       string        `json:"section_code"`
	TrainingSessionID uuid.UUID     `json:"training_session_id"` // Cross-context ref (no FK)
	AcademicTermID    uuid.UUID     `json:"academic_term_id"`
	RoomID            *uuid.UUID    `json:"room_id,omitempty"`
	InstructorGCID    uuid.UUID     `json:"instructor_gcid"` // Cross-context ref (no FK)
	MaxCapacity       int           `json:"max_capacity"`
	EnrolledCount     int           `json:"enrolled_count"`
	Status            SectionStatus `json:"status"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
	DeletedAt         *time.Time    `json:"deleted_at,omitempty"`
}

// SectionTimeSlot represents a recurring schedule entry for a class section.
type SectionTimeSlot struct {
	ID             uuid.UUID       `json:"id"`
	TenantID       uuid.UUID       `json:"tenant_id"`
	SectionID      uuid.UUID       `json:"section_id"`
	DayOfWeek      DayOfWeek       `json:"day_of_week"`
	StartTime      string          `json:"start_time"` // HH:MM format
	EndTime        string          `json:"end_time"`   // HH:MM format
	RoomID         *uuid.UUID      `json:"room_id,omitempty"`
	RecurrenceType *RecurrenceType `json:"recurrence_type,omitempty"`
	EffectiveFrom  time.Time       `json:"effective_from"`
	EffectiveUntil *time.Time      `json:"effective_until,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	DeletedAt      *time.Time      `json:"deleted_at,omitempty"`
}

// Attendance is an append-only record of a learner's attendance at a section session.
type Attendance struct {
	ID            uuid.UUID        `json:"id"`
	TenantID      uuid.UUID        `json:"tenant_id"`
	SectionID     uuid.UUID        `json:"section_id"`
	TimeslotID    *uuid.UUID       `json:"timeslot_id,omitempty"`
	LearnerGCID   uuid.UUID        `json:"learner_gcid"`
	Status        AttendanceStatus `json:"status"`
	CheckInMethod CheckInMethod    `json:"check_in_method"`
	CheckInAt     *time.Time       `json:"check_in_at,omitempty"`
	SessionDate   time.Time        `json:"session_date"`
	MarkedByGCID  uuid.UUID        `json:"marked_by_gcid"`
	Notes         *string          `json:"notes,omitempty"`
	RecordedAt    time.Time        `json:"recorded_at"`
}

// AttendanceRecord is the wire format for a single attendance entry.
type AttendanceRecord = Attendance

// FacilityBooking represents a room reservation.
type FacilityBooking struct {
	ID           uuid.UUID     `json:"id"`
	TenantID     uuid.UUID     `json:"tenant_id"`
	RoomID       uuid.UUID     `json:"room_id"`
	BookedByGCID uuid.UUID     `json:"booked_by_gcid"`
	Title        string        `json:"title"`
	StartsAt     time.Time     `json:"starts_at"`
	EndsAt       time.Time     `json:"ends_at"`
	Status       BookingStatus `json:"status"`
	Notes        *string       `json:"notes,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	DeletedAt    *time.Time    `json:"deleted_at,omitempty"`
}

// TermCalendarEvent represents a notable event within an academic term
// (e.g., holidays, exam periods, registration windows).
type TermCalendarEvent struct {
	ID        uuid.UUID  `json:"id"`
	TenantID  uuid.UUID  `json:"tenant_id"`
	TermID    uuid.UUID  `json:"term_id"`
	Title     string     `json:"title"`
	EventType string     `json:"event_type"` // holiday, exam_period, registration, etc.
	StartsAt  time.Time  `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// RoomEquipment represents equipment available in a room.
type RoomEquipment struct {
	ID        uuid.UUID `json:"id"`
	RoomID    uuid.UUID `json:"room_id"`
	Name      string    `json:"name"`
	Quantity  int       `json:"quantity"`
	CreatedAt time.Time `json:"created_at"`
}

// SectionEnrollment tracks a learner's enrollment or waitlist position in a section.
type SectionEnrollment struct {
	ID               uuid.UUID        `json:"id"`
	TenantID         uuid.UUID        `json:"tenant_id"`
	SectionID        uuid.UUID        `json:"section_id"`
	LearnerGCID      uuid.UUID        `json:"learner_gcid"`
	Status           EnrollmentStatus `json:"status"`
	WaitlistPosition *int             `json:"waitlist_position,omitempty"`
	EnrolledAt       *time.Time       `json:"enrolled_at,omitempty"`
	DroppedAt        *time.Time       `json:"dropped_at,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	DeletedAt        *time.Time       `json:"deleted_at,omitempty"`
}

// TimetablePublication represents a published snapshot of a term's timetable.
type TimetablePublication struct {
	ID          uuid.UUID              `json:"id"`
	TenantID    uuid.UUID              `json:"tenant_id"`
	TermID      uuid.UUID              `json:"term_id"`
	PublishedBy uuid.UUID              `json:"published_by"`
	PublishedAt time.Time              `json:"published_at"`
	Version     int                    `json:"version"`
	Status      PublicationStatus      `json:"status"`
	Snapshot    map[string]interface{} `json:"snapshot,omitempty"`
	CreatedAt   time.Time              `json:"created_at"`
	DeletedAt   *time.Time             `json:"deleted_at,omitempty"`
}

// PersonalTimetableEntry is the wire format for a learner's personal timetable.
type PersonalTimetableEntry struct {
	SectionID      uuid.UUID `json:"section_id"`
	SectionCode    string    `json:"section_code"`
	CourseName     string    `json:"course_name"`
	DayOfWeek      DayOfWeek `json:"day_of_week"`
	StartTime      string    `json:"start_time"`
	EndTime        string    `json:"end_time"`
	RoomName       string    `json:"room_name"`
	VenueName      string    `json:"venue_name"`
	InstructorName string    `json:"instructor_name"`
}

// PageInfo contains cursor-based pagination metadata.
type PageInfo struct {
	NextCursor *string `json:"next_cursor,omitempty"`
	HasNext    bool    `json:"has_next"`
}
