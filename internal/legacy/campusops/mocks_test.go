package campusops

import (
	"context"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
)

// Compile-time interface checks.
var (
	_ AcademicTermRepository = (*mockTermRepo)(nil)
	_ VenueRepository        = (*mockVenueRepo)(nil)
	_ RoomRepository         = (*mockRoomRepo)(nil)
	_ ClassSectionRepository = (*mockSectionRepo)(nil)
	_ TimeSlotRepository     = (*mockTimeSlotRepo)(nil)
	_ AttendanceRepository   = (*mockAttendanceRepo)(nil)
	_ BookingRepository      = (*mockBookingRepo)(nil)
	_ EnrollmentRepository   = (*mockEnrollmentRepo)(nil)
	_ TimetableRepository    = (*mockTimetableRepo)(nil)
	_ EventPublisher         = (*mockEventPublisher)(nil)
)

// ---------------------------------------------------------------------------
// mockTermRepo — AcademicTermRepository
// ---------------------------------------------------------------------------

type mockTermRepo struct{ mock.Mock }

func (m *mockTermRepo) Create(ctx context.Context, term *AcademicTerm) error {
	return m.Called(ctx, term).Error(0)
}

func (m *mockTermRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*AcademicTerm, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AcademicTerm), args.Error(1)
}

func (m *mockTermRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]AcademicTerm, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]AcademicTerm), args.Error(1)
}

func (m *mockTermRepo) Update(ctx context.Context, term *AcademicTerm) error {
	return m.Called(ctx, term).Error(0)
}

func (m *mockTermRepo) Delete(ctx context.Context, id, tenantID uuid.UUID) error {
	return m.Called(ctx, id, tenantID).Error(0)
}

func (m *mockTermRepo) GetCurrent(ctx context.Context, tenantID uuid.UUID) (*AcademicTerm, error) {
	args := m.Called(ctx, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AcademicTerm), args.Error(1)
}

func (m *mockTermRepo) HasOverlap(ctx context.Context, tenantID uuid.UUID, startsAt, endsAt interface{}, excludeID *uuid.UUID) (bool, error) {
	args := m.Called(ctx, tenantID, startsAt, endsAt, excludeID)
	return args.Bool(0), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockVenueRepo — VenueRepository
// ---------------------------------------------------------------------------

type mockVenueRepo struct{ mock.Mock }

func (m *mockVenueRepo) Create(ctx context.Context, venue *Venue) error {
	return m.Called(ctx, venue).Error(0)
}

func (m *mockVenueRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*Venue, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Venue), args.Error(1)
}

func (m *mockVenueRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Venue, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]Venue), args.Error(1)
}

func (m *mockVenueRepo) Update(ctx context.Context, venue *Venue) error {
	return m.Called(ctx, venue).Error(0)
}

func (m *mockVenueRepo) Delete(ctx context.Context, id, tenantID uuid.UUID) error {
	return m.Called(ctx, id, tenantID).Error(0)
}

// ---------------------------------------------------------------------------
// mockRoomRepo — RoomRepository
// ---------------------------------------------------------------------------

type mockRoomRepo struct{ mock.Mock }

func (m *mockRoomRepo) Create(ctx context.Context, room *Room) error {
	return m.Called(ctx, room).Error(0)
}

func (m *mockRoomRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*Room, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Room), args.Error(1)
}

func (m *mockRoomRepo) ListByVenue(ctx context.Context, venueID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Room, error) {
	args := m.Called(ctx, venueID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]Room), args.Error(1)
}

func (m *mockRoomRepo) Update(ctx context.Context, room *Room) error {
	return m.Called(ctx, room).Error(0)
}

func (m *mockRoomRepo) Delete(ctx context.Context, id, tenantID uuid.UUID) error {
	return m.Called(ctx, id, tenantID).Error(0)
}

// ---------------------------------------------------------------------------
// mockSectionRepo — ClassSectionRepository
// ---------------------------------------------------------------------------

type mockSectionRepo struct{ mock.Mock }

func (m *mockSectionRepo) Create(ctx context.Context, section *ClassSection) error {
	return m.Called(ctx, section).Error(0)
}

func (m *mockSectionRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*ClassSection, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ClassSection), args.Error(1)
}

func (m *mockSectionRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]ClassSection, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]ClassSection), args.Error(1)
}

func (m *mockSectionRepo) ListByTerm(ctx context.Context, termID, tenantID uuid.UUID) ([]ClassSection, error) {
	args := m.Called(ctx, termID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]ClassSection), args.Error(1)
}

func (m *mockSectionRepo) Update(ctx context.Context, section *ClassSection) error {
	return m.Called(ctx, section).Error(0)
}

func (m *mockSectionRepo) Delete(ctx context.Context, id, tenantID uuid.UUID) error {
	return m.Called(ctx, id, tenantID).Error(0)
}

// ---------------------------------------------------------------------------
// mockTimeSlotRepo — TimeSlotRepository
// ---------------------------------------------------------------------------

type mockTimeSlotRepo struct{ mock.Mock }

func (m *mockTimeSlotRepo) Create(ctx context.Context, slot *SectionTimeSlot) error {
	return m.Called(ctx, slot).Error(0)
}

func (m *mockTimeSlotRepo) GetByID(ctx context.Context, id uuid.UUID) (*SectionTimeSlot, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*SectionTimeSlot), args.Error(1)
}

func (m *mockTimeSlotRepo) ListBySection(ctx context.Context, sectionID uuid.UUID) ([]SectionTimeSlot, error) {
	args := m.Called(ctx, sectionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SectionTimeSlot), args.Error(1)
}

func (m *mockTimeSlotRepo) Update(ctx context.Context, slot *SectionTimeSlot) error {
	return m.Called(ctx, slot).Error(0)
}

func (m *mockTimeSlotRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// mockAttendanceRepo — AttendanceRepository
// ---------------------------------------------------------------------------

type mockAttendanceRepo struct{ mock.Mock }

func (m *mockAttendanceRepo) Create(ctx context.Context, records []Attendance) error {
	return m.Called(ctx, records).Error(0)
}

func (m *mockAttendanceRepo) ListBySection(ctx context.Context, sectionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Attendance, error) {
	args := m.Called(ctx, sectionID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]Attendance), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockBookingRepo — BookingRepository
// ---------------------------------------------------------------------------

type mockBookingRepo struct{ mock.Mock }

func (m *mockBookingRepo) Create(ctx context.Context, booking *FacilityBooking) error {
	return m.Called(ctx, booking).Error(0)
}

func (m *mockBookingRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*FacilityBooking, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*FacilityBooking), args.Error(1)
}

func (m *mockBookingRepo) List(ctx context.Context, tenantID uuid.UUID, roomID *uuid.UUID, from, to *string, cursor *uuid.UUID, limit int) ([]FacilityBooking, error) {
	args := m.Called(ctx, tenantID, roomID, from, to, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]FacilityBooking), args.Error(1)
}

func (m *mockBookingRepo) ListByRoomAndDate(ctx context.Context, roomID, tenantID uuid.UUID, date string) ([]FacilityBooking, error) {
	args := m.Called(ctx, roomID, tenantID, date)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]FacilityBooking), args.Error(1)
}

func (m *mockBookingRepo) HasOverlap(ctx context.Context, roomID, tenantID uuid.UUID, startsAt, endsAt interface{}) (bool, error) {
	args := m.Called(ctx, roomID, tenantID, startsAt, endsAt)
	return args.Bool(0), args.Error(1)
}

func (m *mockBookingRepo) Delete(ctx context.Context, id, tenantID uuid.UUID) error {
	return m.Called(ctx, id, tenantID).Error(0)
}

// ---------------------------------------------------------------------------
// mockEnrollmentRepo — EnrollmentRepository
// ---------------------------------------------------------------------------

type mockEnrollmentRepo struct{ mock.Mock }

func (m *mockEnrollmentRepo) Create(ctx context.Context, enrollment *SectionEnrollment) error {
	return m.Called(ctx, enrollment).Error(0)
}

func (m *mockEnrollmentRepo) GetByLearnerAndSection(ctx context.Context, learnerGCID, sectionID, tenantID uuid.UUID) (*SectionEnrollment, error) {
	args := m.Called(ctx, learnerGCID, sectionID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*SectionEnrollment), args.Error(1)
}

func (m *mockEnrollmentRepo) ListBySection(ctx context.Context, sectionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SectionEnrollment, error) {
	args := m.Called(ctx, sectionID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SectionEnrollment), args.Error(1)
}

func (m *mockEnrollmentRepo) ListWaitlisted(ctx context.Context, sectionID, tenantID uuid.UUID) ([]SectionEnrollment, error) {
	args := m.Called(ctx, sectionID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SectionEnrollment), args.Error(1)
}

func (m *mockEnrollmentRepo) ListBySectionAndStatus(ctx context.Context, sectionID, tenantID uuid.UUID, status EnrollmentStatus) ([]SectionEnrollment, error) {
	args := m.Called(ctx, sectionID, tenantID, status)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SectionEnrollment), args.Error(1)
}

func (m *mockEnrollmentRepo) ListByLearnerAndTerm(ctx context.Context, learnerGCID, termID, tenantID uuid.UUID) ([]SectionEnrollment, error) {
	args := m.Called(ctx, learnerGCID, termID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SectionEnrollment), args.Error(1)
}

func (m *mockEnrollmentRepo) Update(ctx context.Context, enrollment *SectionEnrollment) error {
	return m.Called(ctx, enrollment).Error(0)
}

func (m *mockEnrollmentRepo) CountEnrolled(ctx context.Context, sectionID, tenantID uuid.UUID) (int, error) {
	args := m.Called(ctx, sectionID, tenantID)
	return args.Int(0), args.Error(1)
}

func (m *mockEnrollmentRepo) NextWaitlistPosition(ctx context.Context, sectionID, tenantID uuid.UUID) (int, error) {
	args := m.Called(ctx, sectionID, tenantID)
	return args.Int(0), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockTimetableRepo — TimetableRepository
// ---------------------------------------------------------------------------

type mockTimetableRepo struct{ mock.Mock }

func (m *mockTimetableRepo) Create(ctx context.Context, pub *TimetablePublication) error {
	return m.Called(ctx, pub).Error(0)
}

func (m *mockTimetableRepo) GetLatest(ctx context.Context, termID, tenantID uuid.UUID) (*TimetablePublication, error) {
	args := m.Called(ctx, termID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*TimetablePublication), args.Error(1)
}

func (m *mockTimetableRepo) ListByTerm(ctx context.Context, termID, tenantID uuid.UUID) ([]TimetablePublication, error) {
	args := m.Called(ctx, termID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]TimetablePublication), args.Error(1)
}

func (m *mockTimetableRepo) GetMaxVersion(ctx context.Context, termID, tenantID uuid.UUID) (int, error) {
	args := m.Called(ctx, termID, tenantID)
	return args.Int(0), args.Error(1)
}

func (m *mockTimetableRepo) SupersedeAll(ctx context.Context, termID, tenantID uuid.UUID) error {
	return m.Called(ctx, termID, tenantID).Error(0)
}

// ---------------------------------------------------------------------------
// mockEventPublisher — EventPublisher
// ---------------------------------------------------------------------------

type mockEventPublisher struct{ mock.Mock }

func (m *mockEventPublisher) Publish(ctx context.Context, topic string, event interface{}) error {
	return m.Called(ctx, topic, event).Error(0)
}

func (m *mockEventPublisher) Close() error {
	return m.Called().Error(0)
}
