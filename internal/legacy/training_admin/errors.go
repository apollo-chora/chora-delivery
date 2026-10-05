package training_admin

import "errors"

// Sentinel errors for the training-admin domain.
// Error codes use TRAINING_ prefix per error-handling conventions.
var (
	// ErrSessionNotFound is returned when a training session cannot be found.
	ErrSessionNotFound = errors.New("TRAINING_SESSION_NOT_FOUND")

	// ErrSessionNotModifiable is returned when a training session is in a state
	// that does not allow modifications (e.g., completed or cancelled).
	ErrSessionNotModifiable = errors.New("TRAINING_SESSION_NOT_MODIFIABLE")

	// ErrSessionNotDeletable is returned when a training session cannot be deleted
	// (e.g., in progress with active enrollments).
	ErrSessionNotDeletable = errors.New("TRAINING_SESSION_NOT_DELETABLE")

	// ErrInvalidStateTransition is returned when an invalid status transition is attempted.
	ErrInvalidStateTransition = errors.New("TRAINING_INVALID_STATE_TRANSITION")

	// ErrEnrollmentClosed is returned when enrollment is not open for the session.
	ErrEnrollmentClosed = errors.New("TRAINING_ENROLLMENT_CLOSED")

	// ErrSessionFull is returned when the training session has reached max capacity.
	ErrSessionFull = errors.New("TRAINING_SESSION_FULL")

	// ErrDuplicateApplication is returned when a learner has already applied to the session.
	ErrDuplicateApplication = errors.New("TRAINING_DUPLICATE_APPLICATION")

	// ErrAlreadyEnrolled is returned when a learner is already enrolled in the program.
	ErrAlreadyEnrolled = errors.New("TRAINING_ALREADY_ENROLLED")

	// ErrApplicationNotReviewable is returned when an application is not in a reviewable state.
	ErrApplicationNotReviewable = errors.New("TRAINING_APPLICATION_NOT_REVIEWABLE")

	// ErrRequestNotDecidable is returned when a trainee request is not in a decidable state.
	ErrRequestNotDecidable = errors.New("TRAINING_REQUEST_NOT_DECIDABLE")

	// ErrPathNotInProgram is returned when a path ID is not part of the certificate program.
	ErrPathNotInProgram = errors.New("TRAINING_PATH_NOT_IN_PROGRAM")

	// ErrTransferSameSession is returned when a transfer request targets the same session.
	ErrTransferSameSession = errors.New("TRAINING_TRANSFER_SAME_SESSION")

	// ErrValidationFailed is returned when request validation fails.
	ErrValidationFailed = errors.New("TRAINING_VALIDATION_FAILED")

	// ErrNotFound is a generic not-found error for the training domain.
	ErrNotFound = errors.New("TRAINING_NOT_FOUND")

	// ErrForbidden is returned when the caller lacks permission.
	ErrForbidden = errors.New("TRAINING_FORBIDDEN")

	// ErrUnauthorized is returned when authentication is required but missing.
	ErrUnauthorized = errors.New("TRAINING_UNAUTHORIZED")

	// ErrResultNotFound is returned when a learner result cannot be found.
	ErrResultNotFound = errors.New("TRAINING_RESULT_NOT_FOUND")

	// ErrResultAlreadyPublished is returned when attempting to modify a published result.
	ErrResultAlreadyPublished = errors.New("TRAINING_RESULT_ALREADY_PUBLISHED")

	// ErrCertificateRequestNotFound is returned when a certificate request cannot be found.
	ErrCertificateRequestNotFound = errors.New("TRAINING_CERTIFICATE_REQUEST_NOT_FOUND")

	// ErrCertificateRequestNotReviewable is returned when a certificate request is not in a reviewable state.
	ErrCertificateRequestNotReviewable = errors.New("TRAINING_CERTIFICATE_REQUEST_NOT_REVIEWABLE")

	// ErrCertificateRequestNotIssuable is returned when a certificate request is not in approved state.
	ErrCertificateRequestNotIssuable = errors.New("TRAINING_CERTIFICATE_REQUEST_NOT_ISSUABLE")

	// ErrAppealNotFound is returned when a student appeal cannot be found.
	ErrAppealNotFound = errors.New("TRAINING_APPEAL_NOT_FOUND")

	// ErrAppealNotReviewable is returned when a student appeal is not in a reviewable state.
	ErrAppealNotReviewable = errors.New("TRAINING_APPEAL_NOT_REVIEWABLE")

	// ErrDeviceNotFound is returned when a signage device cannot be found.
	ErrDeviceNotFound = errors.New("TRAINING_DEVICE_NOT_FOUND")
)
