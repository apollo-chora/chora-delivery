package training_admin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSessionStatusIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, SessionStatusDraft.IsValid())
	assert.True(t, SessionStatusScheduled.IsValid())
	assert.True(t, SessionStatusInProgress.IsValid())
	assert.True(t, SessionStatusCompleted.IsValid())
	assert.True(t, SessionStatusCancelled.IsValid())
	assert.False(t, SessionStatus("invalid").IsValid())
}

func TestDeliveryModeIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, DeliveryModePhysical.IsValid())
	assert.True(t, DeliveryModeVirtual.IsValid())
	assert.True(t, DeliveryModeHybrid.IsValid())
	assert.False(t, DeliveryMode("invalid").IsValid())
}

func TestAttendanceStatusIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, AttendanceStatusPresent.IsValid())
	assert.True(t, AttendanceStatusLate.IsValid())
	assert.True(t, AttendanceStatusAbsent.IsValid())
	assert.True(t, AttendanceStatusExcused.IsValid())
	assert.False(t, AttendanceStatus("invalid").IsValid())
}

func TestCheckInMethodIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, CheckInMethodManual.IsValid())
	assert.True(t, CheckInMethodQRScan.IsValid())
	assert.True(t, CheckInMethodNFC.IsValid())
	assert.True(t, CheckInMethodGeoCheckin.IsValid())
	assert.False(t, CheckInMethod("invalid").IsValid())
}

func TestApplicationStatusIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, ApplicationStatusDraft.IsValid())
	assert.True(t, ApplicationStatusSubmitted.IsValid())
	assert.True(t, ApplicationStatusUnderReview.IsValid())
	assert.True(t, ApplicationStatusApproved.IsValid())
	assert.True(t, ApplicationStatusRejected.IsValid())
	assert.True(t, ApplicationStatusWaitlisted.IsValid())
	assert.False(t, ApplicationStatus("invalid").IsValid())
}

func TestRequestTypeIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, RequestTypeDeferral.IsValid())
	assert.True(t, RequestTypeWithdrawal.IsValid())
	assert.True(t, RequestTypeMakeup.IsValid())
	assert.True(t, RequestTypeTransfer.IsValid())
	assert.False(t, RequestType("invalid").IsValid())
}

func TestRequestStatusIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, RequestStatusSubmitted.IsValid())
	assert.True(t, RequestStatusUnderReview.IsValid())
	assert.True(t, RequestStatusApproved.IsValid())
	assert.True(t, RequestStatusRejected.IsValid())
	assert.False(t, RequestStatus("invalid").IsValid())
}

func TestProgramStatusIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, ProgramStatusDraft.IsValid())
	assert.True(t, ProgramStatusActive.IsValid())
	assert.True(t, ProgramStatusArchived.IsValid())
	assert.False(t, ProgramStatus("invalid").IsValid())
}

func TestEnrollmentStatusIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, EnrollmentStatusEnrolled.IsValid())
	assert.True(t, EnrollmentStatusInProgress.IsValid())
	assert.True(t, EnrollmentStatusCompleted.IsValid())
	assert.True(t, EnrollmentStatusWithdrawn.IsValid())
	assert.False(t, EnrollmentStatus("invalid").IsValid())
}

func TestRecurrenceTypeIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, RecurrenceTypeOneOff.IsValid())
	assert.True(t, RecurrenceTypeWeekly.IsValid())
	assert.True(t, RecurrenceTypeBiweekly.IsValid())
	assert.False(t, RecurrenceType("invalid").IsValid())
}

func TestDayOfWeekIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, DayOfWeekMon.IsValid())
	assert.True(t, DayOfWeekTue.IsValid())
	assert.True(t, DayOfWeekWed.IsValid())
	assert.True(t, DayOfWeekThu.IsValid())
	assert.True(t, DayOfWeekFri.IsValid())
	assert.True(t, DayOfWeekSat.IsValid())
	assert.True(t, DayOfWeekSun.IsValid())
	assert.False(t, DayOfWeek("invalid").IsValid())
}
func TestAssessmentTypeIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, AssessmentTypeQuiz.IsValid())
	assert.True(t, AssessmentTypeExam.IsValid())
	assert.True(t, AssessmentTypePractical.IsValid())
	assert.True(t, AssessmentTypeAssignment.IsValid())
	assert.False(t, AssessmentType("invalid").IsValid())
}

func TestCertificateRequestStatusIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, CertReqStatusSubmitted.IsValid())
	assert.True(t, CertReqStatusUnderReview.IsValid())
	assert.True(t, CertReqStatusApproved.IsValid())
	assert.True(t, CertReqStatusRejected.IsValid())
	assert.True(t, CertReqStatusIssued.IsValid())
	assert.False(t, CertificateRequestStatus("invalid").IsValid())
}

func TestAppealTypeIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, AppealTypeGradeReview.IsValid())
	assert.True(t, AppealTypeAttendanceCorrection.IsValid())
	assert.True(t, AppealTypeLateSubmission.IsValid())
	assert.True(t, AppealTypeOther.IsValid())
	assert.False(t, AppealType("invalid").IsValid())
}

func TestAppealStatusIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, AppealStatusSubmitted.IsValid())
	assert.True(t, AppealStatusUnderReview.IsValid())
	assert.True(t, AppealStatusUpheld.IsValid())
	assert.True(t, AppealStatusOverturned.IsValid())
	assert.True(t, AppealStatusDismissed.IsValid())
	assert.False(t, AppealStatus("invalid").IsValid())
}

func TestDisplayModeIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, DisplayModeLeaderboard.IsValid())
	assert.True(t, DisplayModeTimetable.IsValid())
	assert.True(t, DisplayModeAnnouncements.IsValid())
	assert.True(t, DisplayModeMixed.IsValid())
	assert.False(t, DisplayMode("invalid").IsValid())
}

func TestDeviceStatusIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, DeviceStatusOnline.IsValid())
	assert.True(t, DeviceStatusOffline.IsValid())
	assert.True(t, DeviceStatusMaintenance.IsValid())
	assert.False(t, DeviceStatus("invalid").IsValid())
}
