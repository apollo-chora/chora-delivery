package campusops

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type testVenueDeps struct {
	svc       *VenueService
	venueR    *mockVenueRepo
	roomR     *mockRoomRepo
	publisher *mockEventPublisher
}

func newTestVenueService() testVenueDeps {
	vr := &mockVenueRepo{}
	rr := &mockRoomRepo{}
	ep := &mockEventPublisher{}
	return testVenueDeps{
		svc:       NewVenueService(vr, rr, ep),
		venueR:    vr,
		roomR:     rr,
		publisher: ep,
	}
}

func TestCreateVenue(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *Venue
		setupMocks   func(d testVenueDeps)
		wantErr      error
		assertResult func(t *testing.T, got *Venue)
	}{
		{
			name: "success: creates venue with UUIDv7",
			input: &Venue{
				TenantID: tenantID,
				Name:     "Main Campus Building",
			},
			setupMocks: func(d testVenueDeps) {
				d.venueR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.Venue")).Return(nil)
			},
			assertResult: func(t *testing.T, got *Venue) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, "Main Campus Building", got.Name)
				assert.True(t, got.IsActive)
			},
		},
		{
			name: "fails: empty name",
			input: &Venue{
				TenantID: tenantID,
				Name:     "",
			},
			setupMocks: func(d testVenueDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: repo error propagated",
			input: &Venue{
				TenantID: tenantID,
				Name:     "Good Venue",
			},
			setupMocks: func(d testVenueDeps) {
				d.venueR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.Venue")).
					Return(errors.New("db down"))
			},
			wantErr: errors.New("db down"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestVenueService()
			tc.setupMocks(d)

			got, err := d.svc.CreateVenue(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.venueR.AssertExpectations(t)
		})
	}
}

func TestCreateRoom(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	venueID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *Room
		setupMocks   func(d testVenueDeps)
		wantErr      error
		assertResult func(t *testing.T, got *Room)
	}{
		{
			name: "success: creates room in venue",
			input: &Room{
				TenantID: tenantID,
				VenueID:  venueID,
				Name:     "Room 101",
				RoomCode: "R101",
				Capacity: 30,
				RoomType: RoomTypeClassroom,
			},
			setupMocks: func(d testVenueDeps) {
				d.venueR.On("GetByID", mock.Anything, venueID, tenantID).Return(&Venue{
					ID: venueID, TenantID: tenantID, Name: "Building A",
				}, nil)
				d.roomR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.Room")).Return(nil)
			},
			assertResult: func(t *testing.T, got *Room) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, "Room 101", got.Name)
				assert.Equal(t, venueID, got.VenueID)
				assert.True(t, got.IsActive)
			},
		},
		{
			name: "fails: empty name",
			input: &Room{
				TenantID: tenantID, VenueID: venueID,
				Name: "", RoomCode: "R102", Capacity: 20, RoomType: RoomTypeLab,
			},
			setupMocks: func(d testVenueDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: empty room_code",
			input: &Room{
				TenantID: tenantID, VenueID: venueID,
				Name: "Room", RoomCode: "", Capacity: 20, RoomType: RoomTypeLab,
			},
			setupMocks: func(d testVenueDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: invalid room_type",
			input: &Room{
				TenantID: tenantID, VenueID: venueID,
				Name: "Room", RoomCode: "R103", Capacity: 20, RoomType: RoomType("hangar"),
			},
			setupMocks: func(d testVenueDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: zero capacity",
			input: &Room{
				TenantID: tenantID, VenueID: venueID,
				Name: "Room", RoomCode: "R104", Capacity: 0, RoomType: RoomTypeClassroom,
			},
			setupMocks: func(d testVenueDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: venue not found",
			input: &Room{
				TenantID: tenantID, VenueID: uuid.Must(uuid.NewV7()),
				Name: "Room", RoomCode: "R105", Capacity: 20, RoomType: RoomTypeClassroom,
			},
			setupMocks: func(d testVenueDeps) {
				d.venueR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrVenueNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestVenueService()
			tc.setupMocks(d)

			got, err := d.svc.CreateRoom(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.venueR.AssertExpectations(t)
			d.roomR.AssertExpectations(t)
		})
	}
}

func TestUpdateVenue(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	venueID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		input      *Venue
		setupMocks func(d testVenueDeps)
		wantErr    error
	}{
		{
			name: "success: updates venue",
			input: &Venue{
				ID: venueID, TenantID: tenantID, Name: "Updated Building",
				IsActive: true,
			},
			setupMocks: func(d testVenueDeps) {
				d.venueR.On("GetByID", mock.Anything, venueID, tenantID).Return(&Venue{
					ID: venueID, TenantID: tenantID, Name: "Old Building",
				}, nil)
				d.venueR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.Venue")).Return(nil)
			},
		},
		{
			name: "fails: venue not found",
			input: &Venue{
				ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Name: "X",
			},
			setupMocks: func(d testVenueDeps) {
				d.venueR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrVenueNotFound,
		},
		{
			name: "fails: empty name on update",
			input: &Venue{
				ID: venueID, TenantID: tenantID, Name: "",
			},
			setupMocks: func(d testVenueDeps) {
				d.venueR.On("GetByID", mock.Anything, venueID, tenantID).Return(&Venue{
					ID: venueID, TenantID: tenantID, Name: "Old",
				}, nil)
			},
			wantErr: ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestVenueService()
			tc.setupMocks(d)

			got, err := d.svc.UpdateVenue(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}

			d.venueR.AssertExpectations(t)
		})
	}
}
