package wbl

import "errors"

// Sentinel errors for the WBL domain.
// Error codes use WBL_ prefix per error-handling conventions.
var (
	// ErrInternshipNotFound is returned when an internship cannot be found.
	ErrInternshipNotFound = errors.New("WBL_INTERNSHIP_NOT_FOUND")

	// ErrInternshipNotOpen is returned when an internship is not in open status.
	ErrInternshipNotOpen = errors.New("WBL_INTERNSHIP_NOT_OPEN")

	// ErrInternshipFull is returned when all positions in an internship are filled.
	ErrInternshipFull = errors.New("WBL_INTERNSHIP_FULL")

	// ErrDuplicateApplication is returned when a learner has already applied.
	ErrDuplicateApplication = errors.New("WBL_DUPLICATE_APPLICATION")

	// ErrApplicationNotReviewable is returned when an application is not in a reviewable state.
	ErrApplicationNotReviewable = errors.New("WBL_APPLICATION_NOT_REVIEWABLE")

	// ErrPlacementNotFound is returned when a placement cannot be found.
	ErrPlacementNotFound = errors.New("WBL_PLACEMENT_NOT_FOUND")

	// ErrPlacementNotActive is returned when a placement is not in active status.
	ErrPlacementNotActive = errors.New("WBL_PLACEMENT_NOT_ACTIVE")

	// ErrCapstoneNotFound is returned when a capstone project cannot be found.
	ErrCapstoneNotFound = errors.New("WBL_CAPSTONE_NOT_FOUND")

	// ErrCapstoneNotSubmittable is returned when a capstone is not in a submittable state.
	ErrCapstoneNotSubmittable = errors.New("WBL_CAPSTONE_NOT_SUBMITTABLE")

	// ErrNotModifiable is returned when an entity is in a state that does not allow modifications.
	ErrNotModifiable = errors.New("WBL_NOT_MODIFIABLE")

	// ErrValidationFailed is returned when request validation fails.
	ErrValidationFailed = errors.New("WBL_VALIDATION_FAILED")

	// ErrNotFound is a generic not-found error for the WBL domain.
	ErrNotFound = errors.New("WBL_NOT_FOUND")

	// ErrForbidden is returned when the caller lacks permission.
	ErrForbidden = errors.New("WBL_FORBIDDEN")

	// ErrUnauthorized is returned when authentication is required but missing.
	ErrUnauthorized = errors.New("WBL_UNAUTHORIZED")

	// ErrWBLLogNotFound is returned when a WBL log entry cannot be found.
	ErrWBLLogNotFound = errors.New("WBL_LOG_NOT_FOUND")

	// ErrWBLLogNotPending is returned when a WBL log is not in pending_approval status.
	ErrWBLLogNotPending = errors.New("WBL_LOG_NOT_PENDING")

	// ErrWBLLogNotRejected is returned when a WBL log is not in rejected status.
	ErrWBLLogNotRejected = errors.New("WBL_LOG_NOT_REJECTED")
)
