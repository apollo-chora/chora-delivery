package campusops

import "errors"

// Sentinel errors for the campus operations domain.
// Error codes use CAMPUS_ prefix per error-handling conventions.
var (
	// ErrTermNotFound is returned when an academic term cannot be found.
	ErrTermNotFound = errors.New("CAMPUS_TERM_NOT_FOUND")

	// ErrVenueNotFound is returned when a venue cannot be found.
	ErrVenueNotFound = errors.New("CAMPUS_VENUE_NOT_FOUND")

	// ErrRoomNotFound is returned when a room cannot be found.
	ErrRoomNotFound = errors.New("CAMPUS_ROOM_NOT_FOUND")

	// ErrSectionNotFound is returned when a class section cannot be found.
	ErrSectionNotFound = errors.New("CAMPUS_SECTION_NOT_FOUND")

	// ErrBookingNotFound is returned when a facility booking cannot be found.
	ErrBookingNotFound = errors.New("CAMPUS_BOOKING_NOT_FOUND")

	// ErrBookingConflict is returned when a booking overlaps with an existing one.
	ErrBookingConflict = errors.New("CAMPUS_BOOKING_CONFLICT")

	// ErrSectionNotModifiable is returned when a section is in a terminal state.
	ErrSectionNotModifiable = errors.New("CAMPUS_SECTION_NOT_MODIFIABLE")

	// ErrInvalidStateTransition is returned when an invalid status change is attempted.
	ErrInvalidStateTransition = errors.New("CAMPUS_INVALID_STATE_TRANSITION")

	// ErrTermDateRange is returned when term end date is before start date.
	ErrTermDateRange = errors.New("CAMPUS_TERM_INVALID_DATE_RANGE")

	// ErrBookingDateRange is returned when booking end is before start.
	ErrBookingDateRange = errors.New("CAMPUS_BOOKING_INVALID_DATE_RANGE")

	// ErrValidationFailed is returned when request validation fails.
	ErrValidationFailed = errors.New("CAMPUS_VALIDATION_FAILED")

	// ErrNotFound is a generic not-found error for the campus domain.
	ErrNotFound = errors.New("CAMPUS_NOT_FOUND")

	// ErrForbidden is returned when the caller lacks permission.
	ErrForbidden = errors.New("CAMPUS_FORBIDDEN")

	// ErrUnauthorized is returned when authentication is required but missing.
	ErrUnauthorized = errors.New("CAMPUS_UNAUTHORIZED")

	// ErrEnrollmentNotFound is returned when an enrollment cannot be found.
	ErrEnrollmentNotFound = errors.New("CAMPUS_ENROLLMENT_NOT_FOUND")

	// ErrAlreadyEnrolled is returned when a learner is already enrolled in the section.
	ErrAlreadyEnrolled = errors.New("CAMPUS_ALREADY_ENROLLED")

	// ErrSectionFull is returned when a section has reached max capacity (informational only;
	// the domain service auto-waitlists in this case).
	ErrSectionFull = errors.New("CAMPUS_SECTION_FULL")

	// ErrTermOverlap is returned when creating a term that overlaps with existing terms.
	ErrTermOverlap = errors.New("CAMPUS_TERM_OVERLAP")

	// ErrRoomConflict is returned when a room booking conflicts with existing bookings.
	ErrRoomConflict = errors.New("CAMPUS_ROOM_CONFLICT")

	// ErrTimeSlotNotFound is returned when a time slot cannot be found.
	ErrTimeSlotNotFound = errors.New("CAMPUS_TIMESLOT_NOT_FOUND")

	// ErrTimetableNotFound is returned when a timetable publication cannot be found.
	ErrTimetableNotFound = errors.New("CAMPUS_TIMETABLE_NOT_FOUND")

	// ErrTokenExpired is returned when a QR token has expired.
	ErrTokenExpired = errors.New("CAMPUS_TOKEN_EXPIRED")

	// ErrInvalidToken is returned when a QR token is invalid.
	ErrInvalidToken = errors.New("CAMPUS_INVALID_TOKEN")
)
