package campusops

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func newTestSectionServiceWithEnrollment() testSectionDeps {
	d := newTestSectionService()
	er := &mockEnrollmentRepo{}
	d.svc.SetEnrollmentRepo(er)
	return d
}

func TestEnrollLearner_Success(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	er := &mockEnrollmentRepo{}
	d.svc.SetEnrollmentRepo(er)

	d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(&ClassSection{
		ID: sectionID, TenantID: tenantID, MaxCapacity: 30, Status: SectionStatusActive,
	}, nil)
	er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(nil, nil)
	er.On("CountEnrolled", mock.Anything, sectionID, tenantID).Return(10, nil)
	er.On("Create", mock.Anything, mock.AnythingOfType("*campusops.SectionEnrollment")).Return(nil)
	d.sectionR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.ClassSection")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(nil)

	got, err := d.svc.EnrollLearner(context.Background(), sectionID, learnerGCID, tenantID)

	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, EnrollmentStatusEnrolled, got.Status)
	assert.NotNil(t, got.EnrolledAt)
	assert.Nil(t, got.WaitlistPosition)

	er.AssertExpectations(t)
	d.sectionR.AssertExpectations(t)
}

func TestEnrollLearner_Waitlisted(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	er := &mockEnrollmentRepo{}
	d.svc.SetEnrollmentRepo(er)

	d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(&ClassSection{
		ID: sectionID, TenantID: tenantID, MaxCapacity: 2, Status: SectionStatusActive,
	}, nil)
	er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(nil, nil)
	er.On("CountEnrolled", mock.Anything, sectionID, tenantID).Return(2, nil) // full
	er.On("NextWaitlistPosition", mock.Anything, sectionID, tenantID).Return(3, nil)
	er.On("Create", mock.Anything, mock.AnythingOfType("*campusops.SectionEnrollment")).Return(nil)
	d.sectionR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.ClassSection")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(nil)

	got, err := d.svc.EnrollLearner(context.Background(), sectionID, learnerGCID, tenantID)

	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, EnrollmentStatusWaitlisted, got.Status)
	assert.NotNil(t, got.WaitlistPosition)
	assert.Equal(t, 3, *got.WaitlistPosition)

	er.AssertExpectations(t)
}

func TestEnrollLearner_AlreadyEnrolled(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	er := &mockEnrollmentRepo{}
	d.svc.SetEnrollmentRepo(er)

	d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(&ClassSection{
		ID: sectionID, TenantID: tenantID, MaxCapacity: 30,
	}, nil)
	er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(&SectionEnrollment{
		ID: uuid.Must(uuid.NewV7()), Status: EnrollmentStatusEnrolled,
	}, nil)

	got, err := d.svc.EnrollLearner(context.Background(), sectionID, learnerGCID, tenantID)

	assert.ErrorIs(t, err, ErrAlreadyEnrolled)
	assert.Nil(t, got)
}

func TestEnrollLearner_SectionNotFound(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	er := &mockEnrollmentRepo{}
	d.svc.SetEnrollmentRepo(er)

	d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(nil, nil)

	got, err := d.svc.EnrollLearner(context.Background(), sectionID, learnerGCID, tenantID)

	assert.ErrorIs(t, err, ErrSectionNotFound)
	assert.Nil(t, got)
}

func TestDropLearner_Success(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())
	enrollmentID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	er := &mockEnrollmentRepo{}
	d.svc.SetEnrollmentRepo(er)

	er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(&SectionEnrollment{
		ID: enrollmentID, TenantID: tenantID, SectionID: sectionID, LearnerGCID: learnerGCID,
		Status: EnrollmentStatusEnrolled,
	}, nil)
	er.On("Update", mock.Anything, mock.AnythingOfType("*campusops.SectionEnrollment")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(nil)
	// When a learner is dropped, we check for waitlist promotion
	er.On("ListWaitlisted", mock.Anything, sectionID, tenantID).Return([]SectionEnrollment{}, nil)

	got, err := d.svc.DropLearner(context.Background(), sectionID, learnerGCID, tenantID)

	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, EnrollmentStatusDropped, got.Status)
	assert.NotNil(t, got.DroppedAt)

	er.AssertExpectations(t)
}

func TestDropLearner_WithPromotion(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())
	waitlistedGCID := uuid.Must(uuid.NewV7())
	enrollmentID := uuid.Must(uuid.NewV7())
	waitlistEnrollmentID := uuid.Must(uuid.NewV7())
	pos := 1

	d := newTestSectionService()
	er := &mockEnrollmentRepo{}
	d.svc.SetEnrollmentRepo(er)

	er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(&SectionEnrollment{
		ID: enrollmentID, TenantID: tenantID, SectionID: sectionID, LearnerGCID: learnerGCID,
		Status: EnrollmentStatusEnrolled,
	}, nil)
	er.On("Update", mock.Anything, mock.AnythingOfType("*campusops.SectionEnrollment")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(nil)
	er.On("ListWaitlisted", mock.Anything, sectionID, tenantID).Return([]SectionEnrollment{
		{
			ID: waitlistEnrollmentID, TenantID: tenantID, SectionID: sectionID,
			LearnerGCID: waitlistedGCID, Status: EnrollmentStatusWaitlisted,
			WaitlistPosition: &pos,
		},
	}, nil)

	got, err := d.svc.DropLearner(context.Background(), sectionID, learnerGCID, tenantID)

	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, EnrollmentStatusDropped, got.Status)

	er.AssertExpectations(t)
}

func TestDropLearner_NotFound(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	er := &mockEnrollmentRepo{}
	d.svc.SetEnrollmentRepo(er)

	er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(nil, nil)

	got, err := d.svc.DropLearner(context.Background(), sectionID, learnerGCID, tenantID)

	assert.ErrorIs(t, err, ErrEnrollmentNotFound)
	assert.Nil(t, got)
}

func TestUpdateSection_Success(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(&ClassSection{
		ID: sectionID, TenantID: tenantID, SectionCode: "CS101-A",
		Status: SectionStatusActive, MaxCapacity: 30,
	}, nil)
	d.sectionR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.ClassSection")).Return(nil)

	got, err := d.svc.UpdateSection(context.Background(), &ClassSection{
		ID: sectionID, TenantID: tenantID, SectionCode: "CS101-B", MaxCapacity: 40,
	})

	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, "CS101-B", got.SectionCode)
	assert.Equal(t, 40, got.MaxCapacity)
}

func TestUpdateSection_NotModifiable(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(&ClassSection{
		ID: sectionID, TenantID: tenantID, Status: SectionStatusCancelled,
	}, nil)

	got, err := d.svc.UpdateSection(context.Background(), &ClassSection{
		ID: sectionID, TenantID: tenantID, SectionCode: "X",
	})

	assert.ErrorIs(t, err, ErrSectionNotModifiable)
	assert.Nil(t, got)
}

func TestDeleteSection_Success(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(&ClassSection{
		ID: sectionID, TenantID: tenantID,
	}, nil)
	d.sectionR.On("Delete", mock.Anything, sectionID, tenantID).Return(nil)

	err := d.svc.DeleteSection(context.Background(), sectionID, tenantID)
	assert.NoError(t, err)
}

func TestDeleteSection_NotFound(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(nil, nil)

	err := d.svc.DeleteSection(context.Background(), sectionID, tenantID)
	assert.ErrorIs(t, err, ErrSectionNotFound)
}

func TestUpdateTimeSlot_Success(t *testing.T) {
	t.Parallel()

	slotID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	d := newTestSectionService()
	d.slotR.On("GetByID", mock.Anything, slotID).Return(&SectionTimeSlot{
		ID: slotID, SectionID: sectionID, DayOfWeek: DayOfWeekMon,
		StartTime: "09:00", EndTime: "10:00", EffectiveFrom: now,
	}, nil)
	d.slotR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.SectionTimeSlot")).Return(nil)

	got, err := d.svc.UpdateTimeSlot(context.Background(), &SectionTimeSlot{
		ID: slotID, SectionID: sectionID, DayOfWeek: DayOfWeekTue,
		StartTime: "10:00", EndTime: "11:30", EffectiveFrom: now,
	})

	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, DayOfWeekTue, got.DayOfWeek)
	assert.Equal(t, "10:00", got.StartTime)
	assert.Equal(t, "11:30", got.EndTime)
}

func TestUpdateTimeSlot_NotFound(t *testing.T) {
	t.Parallel()

	slotID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	d.slotR.On("GetByID", mock.Anything, slotID).Return(nil, nil)

	got, err := d.svc.UpdateTimeSlot(context.Background(), &SectionTimeSlot{
		ID: slotID, DayOfWeek: DayOfWeekMon, StartTime: "09:00", EndTime: "10:00",
		EffectiveFrom: time.Now(),
	})

	assert.ErrorIs(t, err, ErrTimeSlotNotFound)
	assert.Nil(t, got)
}

func TestDeleteTimeSlot_Success(t *testing.T) {
	t.Parallel()

	slotID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	d.slotR.On("GetByID", mock.Anything, slotID).Return(&SectionTimeSlot{
		ID: slotID,
	}, nil)
	d.slotR.On("Delete", mock.Anything, slotID).Return(nil)

	err := d.svc.DeleteTimeSlot(context.Background(), slotID)
	assert.NoError(t, err)
}
