package campusops

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type testTimetableDeps struct {
	svc        *TimetableService
	timetableR *mockTimetableRepo
	sectionR   *mockSectionRepo
	slotR      *mockTimeSlotRepo
	publisher  *mockEventPublisher
}

func newTestTimetableService() testTimetableDeps {
	tr := &mockTimetableRepo{}
	sr := &mockSectionRepo{}
	tsr := &mockTimeSlotRepo{}
	ep := &mockEventPublisher{}
	return testTimetableDeps{
		svc:        NewTimetableService(tr, sr, tsr, ep),
		timetableR: tr,
		sectionR:   sr,
		slotR:      tsr,
		publisher:  ep,
	}
}

func TestPublishTimetable_Success(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())
	publisherGCID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	slotID := uuid.Must(uuid.NewV7())

	d := newTestTimetableService()

	d.sectionR.On("ListByTerm", mock.Anything, termID, tenantID).Return([]ClassSection{
		{
			ID: sectionID, TenantID: tenantID, SectionCode: "CS101-A",
			InstructorGCID: uuid.Must(uuid.NewV7()), MaxCapacity: 30,
			EnrolledCount: 15, Status: SectionStatusActive,
		},
	}, nil)
	d.slotR.On("ListBySection", mock.Anything, sectionID).Return([]SectionTimeSlot{
		{
			ID: slotID, SectionID: sectionID, DayOfWeek: DayOfWeekMon,
			StartTime: "09:00", EndTime: "10:30",
		},
	}, nil)
	d.timetableR.On("GetMaxVersion", mock.Anything, termID, tenantID).Return(0, nil)
	d.timetableR.On("SupersedeAll", mock.Anything, termID, tenantID).Return(nil)
	d.timetableR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.TimetablePublication")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(nil)

	got, err := d.svc.PublishTimetable(context.Background(), termID, publisherGCID, tenantID)

	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, 1, got.Version)
	assert.Equal(t, PublicationStatusPublished, got.Status)
	assert.NotNil(t, got.Snapshot)

	d.timetableR.AssertExpectations(t)
	d.sectionR.AssertExpectations(t)
	d.slotR.AssertExpectations(t)
}

func TestPublishTimetable_VersionIncrement(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())
	publisherGCID := uuid.Must(uuid.NewV7())

	d := newTestTimetableService()

	d.sectionR.On("ListByTerm", mock.Anything, termID, tenantID).Return([]ClassSection{}, nil)
	d.timetableR.On("GetMaxVersion", mock.Anything, termID, tenantID).Return(3, nil) // existing v3
	d.timetableR.On("SupersedeAll", mock.Anything, termID, tenantID).Return(nil)
	d.timetableR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.TimetablePublication")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(nil)

	got, err := d.svc.PublishTimetable(context.Background(), termID, publisherGCID, tenantID)

	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, 4, got.Version) // should be v4
}

func TestGetTimetable_Success(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())
	pubID := uuid.Must(uuid.NewV7())

	d := newTestTimetableService()
	d.timetableR.On("GetLatest", mock.Anything, termID, tenantID).Return(&TimetablePublication{
		ID: pubID, TenantID: tenantID, TermID: termID, Version: 1,
		Status: PublicationStatusPublished,
	}, nil)

	got, err := d.svc.GetTimetable(context.Background(), termID, tenantID)

	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, pubID, got.ID)
}

func TestGetTimetable_NotFound(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())

	d := newTestTimetableService()
	d.timetableR.On("GetLatest", mock.Anything, termID, tenantID).Return(nil, nil)

	got, err := d.svc.GetTimetable(context.Background(), termID, tenantID)

	assert.ErrorIs(t, err, ErrTimetableNotFound)
	assert.Nil(t, got)
}

func TestListVersions(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())

	d := newTestTimetableService()
	d.timetableR.On("ListByTerm", mock.Anything, termID, tenantID).Return([]TimetablePublication{
		{ID: uuid.Must(uuid.NewV7()), Version: 2, Status: PublicationStatusPublished},
		{ID: uuid.Must(uuid.NewV7()), Version: 1, Status: PublicationStatusSuperseded},
	}, nil)

	got, err := d.svc.ListVersions(context.Background(), termID, tenantID)

	assert.NoError(t, err)
	assert.Len(t, got, 2)
}

// ---------------------------------------------------------------------------
// Additional coverage: new enum IsValid tests
// ---------------------------------------------------------------------------

func TestEnrollmentStatusIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, EnrollmentStatusEnrolled.IsValid())
	assert.True(t, EnrollmentStatusWaitlisted.IsValid())
	assert.True(t, EnrollmentStatusDropped.IsValid())
	assert.True(t, EnrollmentStatusCompleted.IsValid())
	assert.False(t, EnrollmentStatus("invalid").IsValid())
}

func TestPublicationStatusIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, PublicationStatusDraft.IsValid())
	assert.True(t, PublicationStatusPublished.IsValid())
	assert.True(t, PublicationStatusSuperseded.IsValid())
	assert.False(t, PublicationStatus("invalid").IsValid())
}

func TestRecurrenceTypeIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, RecurrenceTypeWeekly.IsValid())
	assert.True(t, RecurrenceTypeBiweekly.IsValid())
	assert.True(t, RecurrenceTypeOnce.IsValid())
	assert.False(t, RecurrenceType("invalid").IsValid())
}
