package campusops

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// AcademicTerm: ends_at == starts_at (equal, not after) must fail
// ---------------------------------------------------------------------------

func TestCreateTerm_EqualDates(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	d := newTestTermService()

	got, err := d.svc.CreateTerm(context.Background(), &AcademicTerm{
		TenantID: uuid.Must(uuid.NewV7()),
		Name:     "Equal Dates",
		TermType: TermTypeSemester,
		StartsAt: now,
		EndsAt:   now, // equal, not after
	})

	assert.ErrorIs(t, err, ErrTermDateRange)
	assert.Nil(t, got)
}

// UpdateTerm: invalid date range on update must fail.
func TestUpdateTerm_InvalidDateRange(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	d := newTestTermService()
	d.termR.On("GetByID", mock.Anything, termID, tenantID).Return(&AcademicTerm{
		ID: termID, TenantID: tenantID, Name: "Old",
		TermType: TermTypeSemester, StartsAt: now, EndsAt: now.Add(24 * time.Hour),
	}, nil)

	got, err := d.svc.UpdateTerm(context.Background(), &AcademicTerm{
		ID: termID, TenantID: tenantID, Name: "Updated",
		TermType: TermTypeSemester,
		StartsAt: now.Add(48 * time.Hour),
		EndsAt:   now, // before starts_at
	})

	assert.ErrorIs(t, err, ErrTermDateRange)
	assert.Nil(t, got)
}

// UpdateTerm: invalid term_type on update.
func TestUpdateTerm_InvalidTermType(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	d := newTestTermService()
	d.termR.On("GetByID", mock.Anything, termID, tenantID).Return(&AcademicTerm{
		ID: termID, TenantID: tenantID, Name: "Old",
		TermType: TermTypeSemester, StartsAt: now, EndsAt: now.Add(24 * time.Hour),
	}, nil)

	got, err := d.svc.UpdateTerm(context.Background(), &AcademicTerm{
		ID: termID, TenantID: tenantID, Name: "Updated",
		TermType: TermType("biannual"), // invalid
		StartsAt: now,
		EndsAt:   now.Add(24 * time.Hour),
	})

	assert.ErrorIs(t, err, ErrValidationFailed)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// Room: negative capacity must fail
// ---------------------------------------------------------------------------

func TestCreateRoom_NegativeCapacity(t *testing.T) {
	t.Parallel()

	d := newTestVenueService()
	got, err := d.svc.CreateRoom(context.Background(), &Room{
		TenantID: uuid.Must(uuid.NewV7()),
		VenueID:  uuid.Must(uuid.NewV7()),
		Name:     "Room",
		RoomCode: "R200",
		Capacity: -5,
		RoomType: RoomTypeClassroom,
	})

	assert.ErrorIs(t, err, ErrValidationFailed)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// Venue: GetVenue and ListVenues
// ---------------------------------------------------------------------------

func TestGetVenue_Success(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	venueID := uuid.Must(uuid.NewV7())
	d := newTestVenueService()
	d.venueR.On("GetByID", mock.Anything, venueID, tenantID).Return(&Venue{
		ID: venueID, TenantID: tenantID, Name: "Building A",
	}, nil)

	got, err := d.svc.GetVenue(context.Background(), venueID, tenantID)
	assert.NoError(t, err)
	assert.Equal(t, venueID, got.ID)
}

func TestGetVenue_NotFound(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	d := newTestVenueService()
	d.venueR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)

	got, err := d.svc.GetVenue(context.Background(), uuid.Must(uuid.NewV7()), tenantID)
	assert.ErrorIs(t, err, ErrVenueNotFound)
	assert.Nil(t, got)
}

func TestListVenues(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	d := newTestVenueService()
	d.venueR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 20).Return([]Venue{
		{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Name: "V1"},
	}, nil)

	got, err := d.svc.ListVenues(context.Background(), tenantID, nil, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
}

// ---------------------------------------------------------------------------
// Section: ListSections and ListTimeSlots
// ---------------------------------------------------------------------------

func TestListSections(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	d := newTestSectionService()
	d.sectionR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 20).Return([]ClassSection{
		{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, SectionCode: "S1"},
	}, nil)

	got, err := d.svc.ListSections(context.Background(), tenantID, nil, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestListTimeSlots(t *testing.T) {
	t.Parallel()

	sectionID := uuid.Must(uuid.NewV7())
	d := newTestSectionService()
	d.slotR.On("ListBySection", mock.Anything, sectionID).Return([]SectionTimeSlot{
		{ID: uuid.Must(uuid.NewV7()), SectionID: sectionID, DayOfWeek: DayOfWeekMon},
	}, nil)

	got, err := d.svc.ListTimeSlots(context.Background(), sectionID)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
}

// ---------------------------------------------------------------------------
// Booking: HasOverlap repo error propagation
// ---------------------------------------------------------------------------

func TestCreateBooking_HasOverlapRepoError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	roomID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	d := newTestBookingService()
	d.bookingR.On("HasOverlap", mock.Anything, roomID, tenantID, mock.Anything, mock.Anything).
		Return(false, assert.AnError)

	got, err := d.svc.CreateBooking(context.Background(), &FacilityBooking{
		TenantID:     tenantID,
		RoomID:       roomID,
		BookedByGCID: uuid.Must(uuid.NewV7()),
		Title:        "Meeting",
		StartsAt:     now.Add(1 * time.Hour),
		EndsAt:       now.Add(2 * time.Hour),
	})

	assert.Error(t, err)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// Enum IsValid coverage
// ---------------------------------------------------------------------------

func TestEnumIsValid_Comprehensive(t *testing.T) {
	t.Parallel()

	// TermType
	assert.True(t, TermTypeSummer.IsValid())
	assert.True(t, TermTypeCustom.IsValid())
	assert.False(t, TermType("invalid").IsValid())

	// RoomType
	assert.True(t, RoomTypeAuditorium.IsValid())
	assert.True(t, RoomTypeConference.IsValid())
	assert.True(t, RoomTypeStudio.IsValid())
	assert.True(t, RoomTypeOther.IsValid())
	assert.False(t, RoomType("invalid").IsValid())

	// SectionStatus
	assert.True(t, SectionStatusActive.IsValid())
	assert.True(t, SectionStatusCancelled.IsValid())
	assert.True(t, SectionStatusCompleted.IsValid())
	assert.False(t, SectionStatus("invalid").IsValid())

	// AttendanceStatus
	assert.True(t, AttendanceStatusExcused.IsValid())
	assert.False(t, AttendanceStatus("invalid").IsValid())

	// CheckInMethod
	assert.True(t, CheckInMethodNFC.IsValid())
	assert.True(t, CheckInMethodGeoCheckin.IsValid())
	assert.False(t, CheckInMethod("invalid").IsValid())

	// BookingStatus
	assert.True(t, BookingStatusPending.IsValid())
	assert.True(t, BookingStatusConfirmed.IsValid())
	assert.True(t, BookingStatusCancelled.IsValid())
	assert.False(t, BookingStatus("invalid").IsValid())

	// DayOfWeek
	assert.True(t, DayOfWeekSat.IsValid())
	assert.True(t, DayOfWeekSun.IsValid())
	assert.False(t, DayOfWeek("invalid").IsValid())
}

// ListRooms coverage
func TestListRooms(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	venueID := uuid.Must(uuid.NewV7())
	d := newTestVenueService()
	d.roomR.On("ListByVenue", mock.Anything, venueID, tenantID, (*uuid.UUID)(nil), 20).Return([]Room{
		{ID: uuid.Must(uuid.NewV7()), VenueID: venueID, Name: "R1"},
	}, nil)

	got, err := d.svc.ListRooms(context.Background(), venueID, tenantID, nil, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
}

// ListTerms coverage
func TestListTerms(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	d := newTestTermService()
	d.termR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 20).Return([]AcademicTerm{
		{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Name: "Fall"},
	}, nil)

	got, err := d.svc.ListTerms(context.Background(), tenantID, nil, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
}
