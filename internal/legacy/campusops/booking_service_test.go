package campusops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type testBookingDeps struct {
	svc       *BookingService
	bookingR  *mockBookingRepo
	publisher *mockEventPublisher
}

func newTestBookingService() testBookingDeps {
	br := &mockBookingRepo{}
	ep := &mockEventPublisher{}
	return testBookingDeps{
		svc:       NewBookingService(br, ep),
		bookingR:  br,
		publisher: ep,
	}
}

func TestCreateBooking(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	roomID := uuid.Must(uuid.NewV7())
	bookerGCID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	tests := []struct {
		name         string
		input        *FacilityBooking
		setupMocks   func(d testBookingDeps)
		wantErr      error
		assertResult func(t *testing.T, got *FacilityBooking)
	}{
		{
			name: "success: creates booking with confirmed status",
			input: &FacilityBooking{
				TenantID:     tenantID,
				RoomID:       roomID,
				BookedByGCID: bookerGCID,
				Title:        "Team Meeting",
				StartsAt:     now.Add(1 * time.Hour),
				EndsAt:       now.Add(2 * time.Hour),
			},
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("HasOverlap", mock.Anything, roomID, tenantID, mock.Anything, mock.Anything).Return(false, nil)
				d.bookingR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.FacilityBooking")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *FacilityBooking) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, BookingStatusConfirmed, got.Status)
				assert.Equal(t, "Team Meeting", got.Title)
			},
		},
		{
			name: "fails: empty title",
			input: &FacilityBooking{
				TenantID:     tenantID,
				RoomID:       roomID,
				BookedByGCID: bookerGCID,
				Title:        "",
				StartsAt:     now.Add(1 * time.Hour),
				EndsAt:       now.Add(2 * time.Hour),
			},
			setupMocks: func(d testBookingDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: ends_at before starts_at",
			input: &FacilityBooking{
				TenantID:     tenantID,
				RoomID:       roomID,
				BookedByGCID: bookerGCID,
				Title:        "Bad Time",
				StartsAt:     now.Add(2 * time.Hour),
				EndsAt:       now.Add(1 * time.Hour),
			},
			setupMocks: func(d testBookingDeps) {},
			wantErr:    ErrBookingDateRange,
		},
		{
			name: "fails: booking conflict (overlap)",
			input: &FacilityBooking{
				TenantID:     tenantID,
				RoomID:       roomID,
				BookedByGCID: bookerGCID,
				Title:        "Conflicting Booking",
				StartsAt:     now.Add(1 * time.Hour),
				EndsAt:       now.Add(2 * time.Hour),
			},
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("HasOverlap", mock.Anything, roomID, tenantID, mock.Anything, mock.Anything).Return(true, nil)
			},
			wantErr: ErrBookingConflict,
		},
		{
			name: "fails: repo error propagated",
			input: &FacilityBooking{
				TenantID:     tenantID,
				RoomID:       roomID,
				BookedByGCID: bookerGCID,
				Title:        "Good Booking",
				StartsAt:     now.Add(3 * time.Hour),
				EndsAt:       now.Add(4 * time.Hour),
			},
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("HasOverlap", mock.Anything, roomID, tenantID, mock.Anything, mock.Anything).Return(false, nil)
				d.bookingR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.FacilityBooking")).
					Return(errors.New("db timeout"))
			},
			wantErr: errors.New("db timeout"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestBookingService()
			tc.setupMocks(d)

			got, err := d.svc.CreateBooking(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrValidationFailed) {
					assert.ErrorIs(t, err, ErrValidationFailed)
				}
				if errors.Is(tc.wantErr, ErrBookingDateRange) {
					assert.ErrorIs(t, err, ErrBookingDateRange)
				}
				if errors.Is(tc.wantErr, ErrBookingConflict) {
					assert.ErrorIs(t, err, ErrBookingConflict)
				}
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.bookingR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

func TestListBookings(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	roomID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	tests := []struct {
		name       string
		roomID     *uuid.UUID
		setupMocks func(d testBookingDeps)
		wantErr    error
		wantCount  int
	}{
		{
			name:   "success: returns all bookings",
			roomID: nil,
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), (*string)(nil), (*string)(nil), (*uuid.UUID)(nil), 20).
					Return([]FacilityBooking{
						{
							ID: uuid.Must(uuid.NewV7()), TenantID: tenantID,
							RoomID: roomID, Title: "Meeting", StartsAt: now, EndsAt: now.Add(time.Hour),
						},
					}, nil)
			},
			wantCount: 1,
		},
		{
			name:   "success: filters by room_id",
			roomID: &roomID,
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("List", mock.Anything, tenantID, &roomID, (*string)(nil), (*string)(nil), (*uuid.UUID)(nil), 20).
					Return([]FacilityBooking{
						{
							ID: uuid.Must(uuid.NewV7()), TenantID: tenantID,
							RoomID: roomID, Title: "Room Meeting",
						},
					}, nil)
			},
			wantCount: 1,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestBookingService()
			tc.setupMocks(d)

			got, err := d.svc.ListBookings(context.Background(), tenantID, tc.roomID, nil, nil, nil, 20)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.Len(t, got, tc.wantCount)
			}

			d.bookingR.AssertExpectations(t)
		})
	}
}
