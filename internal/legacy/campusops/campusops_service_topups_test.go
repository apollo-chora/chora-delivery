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

// errTestCampus is a shared sentinel error used to assert wrapped repo errors.
var errTestCampus = errors.New("test campus error")

// ---------------------------------------------------------------------------
// BookingService coverage
// ---------------------------------------------------------------------------

func TestGetBooking(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testBookingDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("GetByID", mock.Anything, id, tenantID).Return(&FacilityBooking{ID: id, TenantID: tenantID}, nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrBookingNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)
			},
			wantErr: errTestCampus,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestBookingService()
			tc.setupMocks(d)

			got, err := d.svc.GetBooking(context.Background(), id, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}
			d.bookingR.AssertExpectations(t)
		})
	}
}

func TestDeleteBooking(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testBookingDeps)
		wantErr    error
	}{
		{
			name: "success: deletes and publishes cancelled event",
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("GetByID", mock.Anything, id, tenantID).Return(&FacilityBooking{
					ID: id, TenantID: tenantID, BookedByGCID: uuid.Must(uuid.NewV7()),
					RoomID: uuid.Must(uuid.NewV7()), Title: "Room Booking",
				}, nil)
				d.bookingR.On("Delete", mock.Anything, id, tenantID).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrBookingNotFound,
		},
		{
			name: "fails: get repo error",
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)
			},
			wantErr: errTestCampus,
		},
		{
			name: "fails: delete repo error",
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("GetByID", mock.Anything, id, tenantID).Return(&FacilityBooking{ID: id}, nil)
				d.bookingR.On("Delete", mock.Anything, id, tenantID).Return(errTestCampus)
			},
			wantErr: errTestCampus,
		},
		{
			name: "success: publish error is ignored",
			setupMocks: func(d testBookingDeps) {
				d.bookingR.On("GetByID", mock.Anything, id, tenantID).Return(&FacilityBooking{ID: id}, nil)
				d.bookingR.On("Delete", mock.Anything, id, tenantID).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(errTestCampus)
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestBookingService()
			tc.setupMocks(d)

			err := d.svc.DeleteBooking(context.Background(), id, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
			d.bookingR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

func TestGetRoomAvailability(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	roomID := uuid.Must(uuid.NewV7())

	d := newTestBookingService()
	d.bookingR.On("ListByRoomAndDate", mock.Anything, roomID, tenantID, "2026-01-01").Return([]FacilityBooking{
		{ID: uuid.Must(uuid.NewV7()), RoomID: roomID},
	}, nil)

	got, err := d.svc.GetRoomAvailability(context.Background(), roomID, tenantID, "2026-01-01")
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.bookingR.AssertExpectations(t)
}

func TestCheckConflict(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	roomID := uuid.Must(uuid.NewV7())

	d := newTestBookingService()
	d.bookingR.On("HasOverlap", mock.Anything, roomID, tenantID, mock.AnythingOfType("time.Time"), mock.AnythingOfType("time.Time")).Return(true, nil)

	now := time.Now()
	conflict, err := d.svc.CheckConflict(context.Background(), roomID, tenantID, now, now.Add(time.Hour))
	assert.NoError(t, err)
	assert.True(t, conflict)
	d.bookingR.AssertExpectations(t)
}

func TestCreateBooking_PublishError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	roomID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	d := newTestBookingService()
	d.bookingR.On("HasOverlap", mock.Anything, roomID, tenantID, mock.Anything, mock.Anything).Return(false, nil)
	d.bookingR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.FacilityBooking")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(errTestCampus)

	_, err := d.svc.CreateBooking(context.Background(), &FacilityBooking{
		TenantID: tenantID, RoomID: roomID, Title: "T",
		StartsAt: now, EndsAt: now.Add(time.Hour),
	})
	assert.ErrorIs(t, err, errTestCampus)
	d.bookingR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TermService coverage
// ---------------------------------------------------------------------------

func TestGetTerm_NotFound(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	d := newTestTermService()
	d.termR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)

	_, err := d.svc.GetTerm(context.Background(), id, tenantID)
	assert.ErrorIs(t, err, ErrTermNotFound)
	d.termR.AssertExpectations(t)
}

func TestUpdateTerm_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	term := &AcademicTerm{
		ID: id, TenantID: tenantID, Name: "T2", TermType: TermTypeSemester,
		StartsAt: now, EndsAt: now.Add(30 * 24 * time.Hour),
	}

	t.Run("get repo error", func(t *testing.T) {
		d := newTestTermService()
		d.termR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)
		_, err := d.svc.UpdateTerm(context.Background(), term)
		assert.ErrorIs(t, err, errTestCampus)
		d.termR.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestTermService()
		d.termR.On("GetByID", mock.Anything, id, tenantID).Return(&AcademicTerm{ID: id}, nil)
		d.termR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.AcademicTerm")).Return(errTestCampus)
		_, err := d.svc.UpdateTerm(context.Background(), term)
		assert.ErrorIs(t, err, errTestCampus)
		d.termR.AssertExpectations(t)
	})
}

func TestDeleteTerm(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testTermDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testTermDeps) {
				d.termR.On("GetByID", mock.Anything, id, tenantID).Return(&AcademicTerm{ID: id}, nil)
				d.termR.On("Delete", mock.Anything, id, tenantID).Return(nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testTermDeps) {
				d.termR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrTermNotFound,
		},
		{
			name: "fails: get repo error",
			setupMocks: func(d testTermDeps) {
				d.termR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)
			},
			wantErr: errTestCampus,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestTermService()
			tc.setupMocks(d)

			err := d.svc.DeleteTerm(context.Background(), id, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
			d.termR.AssertExpectations(t)
		})
	}
}

func TestGetCurrentTerm(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testTermDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testTermDeps) {
				d.termR.On("GetCurrent", mock.Anything, tenantID).Return(&AcademicTerm{ID: uuid.Must(uuid.NewV7())}, nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testTermDeps) {
				d.termR.On("GetCurrent", mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrTermNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testTermDeps) {
				d.termR.On("GetCurrent", mock.Anything, tenantID).Return(nil, errTestCampus)
			},
			wantErr: errTestCampus,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestTermService()
			tc.setupMocks(d)

			got, err := d.svc.GetCurrentTerm(context.Background(), tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}
			d.termR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// VenueService coverage
// ---------------------------------------------------------------------------

func TestGetVenue_NotFound_And_Error(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	t.Run("not found", func(t *testing.T) {
		d := newTestVenueService()
		d.venueR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
		_, err := d.svc.GetVenue(context.Background(), id, tenantID)
		assert.ErrorIs(t, err, ErrVenueNotFound)
		d.venueR.AssertExpectations(t)
	})

	t.Run("repo error", func(t *testing.T) {
		d := newTestVenueService()
		d.venueR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)
		_, err := d.svc.GetVenue(context.Background(), id, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		d.venueR.AssertExpectations(t)
	})
}

func TestUpdateVenue_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	venue := &Venue{ID: id, TenantID: tenantID, Name: "Main Hall"}

	t.Run("get repo error", func(t *testing.T) {
		d := newTestVenueService()
		d.venueR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)
		_, err := d.svc.UpdateVenue(context.Background(), venue)
		assert.ErrorIs(t, err, errTestCampus)
		d.venueR.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestVenueService()
		d.venueR.On("GetByID", mock.Anything, id, tenantID).Return(&Venue{ID: id}, nil)
		d.venueR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.Venue")).Return(errTestCampus)
		_, err := d.svc.UpdateVenue(context.Background(), venue)
		assert.ErrorIs(t, err, errTestCampus)
		d.venueR.AssertExpectations(t)
	})
}

func TestCreateRoom_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	venueID := uuid.Must(uuid.NewV7())

	room := &Room{
		TenantID: tenantID, VenueID: venueID, Name: "Room 1",
		RoomCode: "R1", RoomType: RoomTypeClassroom, Capacity: 30,
	}

	t.Run("venue not found", func(t *testing.T) {
		d := newTestVenueService()
		d.venueR.On("GetByID", mock.Anything, venueID, tenantID).Return(nil, nil)
		_, err := d.svc.CreateRoom(context.Background(), room)
		assert.ErrorIs(t, err, ErrVenueNotFound)
		d.venueR.AssertExpectations(t)
	})

	t.Run("venue lookup error", func(t *testing.T) {
		d := newTestVenueService()
		d.venueR.On("GetByID", mock.Anything, venueID, tenantID).Return(nil, errTestCampus)
		_, err := d.svc.CreateRoom(context.Background(), room)
		assert.ErrorIs(t, err, errTestCampus)
		d.venueR.AssertExpectations(t)
	})

	t.Run("create repo error", func(t *testing.T) {
		d := newTestVenueService()
		d.venueR.On("GetByID", mock.Anything, venueID, tenantID).Return(&Venue{ID: venueID}, nil)
		d.roomR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.Room")).Return(errTestCampus)
		_, err := d.svc.CreateRoom(context.Background(), room)
		assert.ErrorIs(t, err, errTestCampus)
		d.venueR.AssertExpectations(t)
		d.roomR.AssertExpectations(t)
	})
}

func TestDeleteVenue(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testVenueDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testVenueDeps) {
				d.venueR.On("GetByID", mock.Anything, id, tenantID).Return(&Venue{ID: id}, nil)
				d.venueR.On("Delete", mock.Anything, id, tenantID).Return(nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testVenueDeps) {
				d.venueR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrVenueNotFound,
		},
		{
			name: "fails: get repo error",
			setupMocks: func(d testVenueDeps) {
				d.venueR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)
			},
			wantErr: errTestCampus,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestVenueService()
			tc.setupMocks(d)

			err := d.svc.DeleteVenue(context.Background(), id, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
			d.venueR.AssertExpectations(t)
		})
	}
}

func TestGetRoom(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testVenueDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(&Room{ID: id, TenantID: tenantID}, nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrRoomNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)
			},
			wantErr: errTestCampus,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestVenueService()
			tc.setupMocks(d)

			got, err := d.svc.GetRoom(context.Background(), id, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}
			d.roomR.AssertExpectations(t)
		})
	}
}

func TestUpdateRoom(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	valid := &Room{
		ID: id, TenantID: tenantID, Name: "Lab 2", RoomCode: "LB2",
		Capacity: 40, RoomType: RoomTypeLab,
	}

	tests := []struct {
		name         string
		input        func() *Room
		setupMocks   func(d testVenueDeps)
		wantErr      error
		assertResult func(t *testing.T, got *Room)
	}{
		{
			name:  "success: applies all mutable fields",
			input: func() *Room { return valid },
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(&Room{ID: id, TenantID: tenantID, Name: "Old"}, nil)
				d.roomR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.Room")).Return(nil)
			},
			assertResult: func(t *testing.T, got *Room) {
				assert.Equal(t, "Lab 2", got.Name)
				assert.Equal(t, 40, got.Capacity)
				assert.Equal(t, RoomTypeLab, got.RoomType)
			},
		},
		{
			name:  "fails: not found",
			input: func() *Room { return valid },
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrRoomNotFound,
		},
		{
			name:  "fails: get repo error",
			input: func() *Room { return valid },
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)
			},
			wantErr: errTestCampus,
		},
		{
			name:  "fails: empty name",
			input: func() *Room { r := *valid; r.Name = ""; return &r },
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(&Room{ID: id}, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:  "fails: empty room code",
			input: func() *Room { r := *valid; r.RoomCode = ""; return &r },
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(&Room{ID: id}, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:  "fails: invalid room type",
			input: func() *Room { r := *valid; r.RoomType = "bogus"; return &r },
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(&Room{ID: id}, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:  "fails: non-positive capacity",
			input: func() *Room { r := *valid; r.Capacity = 0; return &r },
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(&Room{ID: id}, nil)
			},
			wantErr: ErrValidationFailed,
		},
		{
			name:  "fails: update repo error",
			input: func() *Room { return valid },
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(&Room{ID: id}, nil)
				d.roomR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.Room")).Return(errTestCampus)
			},
			wantErr: errTestCampus,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestVenueService()
			tc.setupMocks(d)

			got, err := d.svc.UpdateRoom(context.Background(), tc.input())

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}
			d.roomR.AssertExpectations(t)
		})
	}
}

func TestDeleteRoom(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testVenueDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(&Room{ID: id}, nil)
				d.roomR.On("Delete", mock.Anything, id, tenantID).Return(nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrRoomNotFound,
		},
		{
			name: "fails: get repo error",
			setupMocks: func(d testVenueDeps) {
				d.roomR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)
			},
			wantErr: errTestCampus,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestVenueService()
			tc.setupMocks(d)

			err := d.svc.DeleteRoom(context.Background(), id, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
			d.roomR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// SectionService coverage
// ---------------------------------------------------------------------------

func TestCreateSection_PublishError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	d.sectionR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.ClassSection")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(errTestCampus)

	_, err := d.svc.CreateSection(context.Background(), &ClassSection{
		TenantID: tenantID, SectionCode: "SEC-1", MaxCapacity: 20, InstructorGCID: uuid.Must(uuid.NewV7()),
	})
	assert.ErrorIs(t, err, errTestCampus)
	d.sectionR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestGetSection_RepoError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	d.sectionR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)

	_, err := d.svc.GetSection(context.Background(), id, tenantID)
	assert.ErrorIs(t, err, errTestCampus)
	d.sectionR.AssertExpectations(t)
}

func TestAddTimeSlot_CreateError(t *testing.T) {
	d := newTestSectionService()
	d.slotR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.SectionTimeSlot")).Return(errTestCampus)

	_, err := d.svc.AddTimeSlot(context.Background(), &SectionTimeSlot{
		DayOfWeek: DayOfWeekMon, StartTime: "09:00", EndTime: "10:00",
	})
	assert.ErrorIs(t, err, errTestCampus)
	d.slotR.AssertExpectations(t)
}

func TestRecordAttendance_PublishError(t *testing.T) {
	d := newTestSectionService()
	records := []Attendance{
		{TenantID: uuid.Must(uuid.NewV7()), SectionID: uuid.Must(uuid.NewV7()), Status: AttendanceStatusPresent, CheckInMethod: CheckInMethodManual},
	}
	d.attendR.On("Create", mock.Anything, mock.Anything).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(errTestCampus)

	err := d.svc.RecordAttendance(context.Background(), records)
	assert.ErrorIs(t, err, errTestCampus)
	d.attendR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestUpdateSection_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())
	roomID := uuid.Must(uuid.NewV7())
	instructor := uuid.Must(uuid.NewV7())

	allFields := func() *ClassSection {
		return &ClassSection{
			ID: id, TenantID: tenantID, SectionCode: "SEC-X",
			RoomID: &roomID, InstructorGCID: instructor, MaxCapacity: 50,
			Status: SectionStatusActive,
		}
	}

	t.Run("get repo error", func(t *testing.T) {
		d := newTestSectionService()
		d.sectionR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)
		_, err := d.svc.UpdateSection(context.Background(), allFields())
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
	})

	t.Run("section not found", func(t *testing.T) {
		d := newTestSectionService()
		d.sectionR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
		_, err := d.svc.UpdateSection(context.Background(), allFields())
		assert.ErrorIs(t, err, ErrSectionNotFound)
		d.sectionR.AssertExpectations(t)
	})

	t.Run("not modifiable", func(t *testing.T) {
		d := newTestSectionService()
		d.sectionR.On("GetByID", mock.Anything, id, tenantID).Return(&ClassSection{ID: id, Status: SectionStatusCancelled}, nil)
		_, err := d.svc.UpdateSection(context.Background(), allFields())
		assert.ErrorIs(t, err, ErrSectionNotModifiable)
		d.sectionR.AssertExpectations(t)
	})

	t.Run("success: applies all settable fields", func(t *testing.T) {
		d := newTestSectionService()
		d.sectionR.On("GetByID", mock.Anything, id, tenantID).Return(&ClassSection{ID: id, TenantID: tenantID, Status: SectionStatusActive}, nil)
		d.sectionR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.ClassSection")).Return(nil)
		got, err := d.svc.UpdateSection(context.Background(), allFields())
		assert.NoError(t, err)
		assert.Equal(t, "SEC-X", got.SectionCode)
		assert.Equal(t, roomID, *got.RoomID)
		assert.Equal(t, instructor, got.InstructorGCID)
		assert.Equal(t, 50, got.MaxCapacity)
		d.sectionR.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestSectionService()
		d.sectionR.On("GetByID", mock.Anything, id, tenantID).Return(&ClassSection{ID: id, Status: SectionStatusActive}, nil)
		d.sectionR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.ClassSection")).Return(errTestCampus)
		_, err := d.svc.UpdateSection(context.Background(), allFields())
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
	})
}

func TestDeleteSection_GetError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	d := newTestSectionService()
	d.sectionR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)

	err := d.svc.DeleteSection(context.Background(), id, tenantID)
	assert.ErrorIs(t, err, errTestCampus)
	d.sectionR.AssertExpectations(t)
}

func TestUpdateTimeSlot_ErrorPaths(t *testing.T) {
	id := uuid.Must(uuid.NewV7())

	valid := func() *SectionTimeSlot {
		return &SectionTimeSlot{ID: id, DayOfWeek: DayOfWeekTue, StartTime: "10:00", EndTime: "11:00"}
	}

	t.Run("get repo error", func(t *testing.T) {
		d := newTestSectionService()
		d.slotR.On("GetByID", mock.Anything, id).Return(nil, errTestCampus)
		_, err := d.svc.UpdateTimeSlot(context.Background(), valid())
		assert.ErrorIs(t, err, errTestCampus)
		d.slotR.AssertExpectations(t)
	})

	t.Run("invalid day of week", func(t *testing.T) {
		d := newTestSectionService()
		d.slotR.On("GetByID", mock.Anything, id).Return(&SectionTimeSlot{ID: id}, nil)
		slot := valid()
		slot.DayOfWeek = "bogus"
		_, err := d.svc.UpdateTimeSlot(context.Background(), slot)
		assert.ErrorIs(t, err, ErrValidationFailed)
		d.slotR.AssertExpectations(t)
	})

	t.Run("end time before start time", func(t *testing.T) {
		d := newTestSectionService()
		d.slotR.On("GetByID", mock.Anything, id).Return(&SectionTimeSlot{ID: id}, nil)
		slot := valid()
		slot.EndTime = "09:00"
		_, err := d.svc.UpdateTimeSlot(context.Background(), slot)
		assert.ErrorIs(t, err, ErrValidationFailed)
		d.slotR.AssertExpectations(t)
	})

	t.Run("update repo error", func(t *testing.T) {
		d := newTestSectionService()
		d.slotR.On("GetByID", mock.Anything, id).Return(&SectionTimeSlot{ID: id}, nil)
		d.slotR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.SectionTimeSlot")).Return(errTestCampus)
		_, err := d.svc.UpdateTimeSlot(context.Background(), valid())
		assert.ErrorIs(t, err, errTestCampus)
		d.slotR.AssertExpectations(t)
	})
}

func TestDeleteTimeSlot_ErrorPaths(t *testing.T) {
	id := uuid.Must(uuid.NewV7())

	t.Run("get repo error", func(t *testing.T) {
		d := newTestSectionService()
		d.slotR.On("GetByID", mock.Anything, id).Return(nil, errTestCampus)
		err := d.svc.DeleteTimeSlot(context.Background(), id)
		assert.ErrorIs(t, err, errTestCampus)
		d.slotR.AssertExpectations(t)
	})

	t.Run("not found", func(t *testing.T) {
		d := newTestSectionService()
		d.slotR.On("GetByID", mock.Anything, id).Return(nil, nil)
		err := d.svc.DeleteTimeSlot(context.Background(), id)
		assert.ErrorIs(t, err, ErrTimeSlotNotFound)
		d.slotR.AssertExpectations(t)
	})
}

func TestEnrollLearner_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	withEnrollment := func() (testSectionDeps, *mockEnrollmentRepo) {
		d := newTestSectionService()
		er := &mockEnrollmentRepo{}
		d.svc.SetEnrollmentRepo(er)
		return d, er
	}

	t.Run("section get error", func(t *testing.T) {
		d, _ := withEnrollment()
		d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(nil, errTestCampus)
		_, err := d.svc.EnrollLearner(context.Background(), sectionID, learnerGCID, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
	})

	t.Run("existing enrollment check error", func(t *testing.T) {
		d, er := withEnrollment()
		d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(&ClassSection{ID: sectionID, MaxCapacity: 30, Status: SectionStatusActive}, nil)
		er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(nil, errTestCampus)
		_, err := d.svc.EnrollLearner(context.Background(), sectionID, learnerGCID, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
		er.AssertExpectations(t)
	})

	t.Run("count enrolled error", func(t *testing.T) {
		d, er := withEnrollment()
		d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(&ClassSection{ID: sectionID, MaxCapacity: 30, Status: SectionStatusActive}, nil)
		er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(nil, nil)
		er.On("CountEnrolled", mock.Anything, sectionID, tenantID).Return(0, errTestCampus)
		_, err := d.svc.EnrollLearner(context.Background(), sectionID, learnerGCID, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
		er.AssertExpectations(t)
	})

	t.Run("waitlist position error", func(t *testing.T) {
		d, er := withEnrollment()
		d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(&ClassSection{ID: sectionID, MaxCapacity: 1, Status: SectionStatusActive}, nil)
		er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(nil, nil)
		er.On("CountEnrolled", mock.Anything, sectionID, tenantID).Return(1, nil)
		er.On("NextWaitlistPosition", mock.Anything, sectionID, tenantID).Return(0, errTestCampus)
		_, err := d.svc.EnrollLearner(context.Background(), sectionID, learnerGCID, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
		er.AssertExpectations(t)
	})

	t.Run("create enrollment error", func(t *testing.T) {
		d, er := withEnrollment()
		d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(&ClassSection{ID: sectionID, MaxCapacity: 30, Status: SectionStatusActive}, nil)
		er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(nil, nil)
		er.On("CountEnrolled", mock.Anything, sectionID, tenantID).Return(5, nil)
		er.On("Create", mock.Anything, mock.AnythingOfType("*campusops.SectionEnrollment")).Return(errTestCampus)
		_, err := d.svc.EnrollLearner(context.Background(), sectionID, learnerGCID, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
		er.AssertExpectations(t)
	})
}

func TestDropLearner_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	t.Run("lookup error", func(t *testing.T) {
		d := newTestSectionService()
		er := &mockEnrollmentRepo{}
		d.svc.SetEnrollmentRepo(er)
		er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(nil, errTestCampus)
		_, err := d.svc.DropLearner(context.Background(), sectionID, learnerGCID, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		er.AssertExpectations(t)
	})

	t.Run("update error", func(t *testing.T) {
		d := newTestSectionService()
		er := &mockEnrollmentRepo{}
		d.svc.SetEnrollmentRepo(er)
		er.On("GetByLearnerAndSection", mock.Anything, learnerGCID, sectionID, tenantID).Return(&SectionEnrollment{
			ID: uuid.Must(uuid.NewV7()), Status: EnrollmentStatusEnrolled,
		}, nil)
		er.On("Update", mock.Anything, mock.AnythingOfType("*campusops.SectionEnrollment")).Return(errTestCampus)
		_, err := d.svc.DropLearner(context.Background(), sectionID, learnerGCID, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		er.AssertExpectations(t)
	})
}

func TestSectionListFunctions(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())

	t.Run("ListEnrollments", func(t *testing.T) {
		d := newTestSectionService()
		er := &mockEnrollmentRepo{}
		d.svc.SetEnrollmentRepo(er)
		er.On("ListBySection", mock.Anything, sectionID, tenantID, mock.Anything, 50).Return([]SectionEnrollment{
			{ID: uuid.Must(uuid.NewV7()), SectionID: sectionID},
		}, nil)
		got, err := d.svc.ListEnrollments(context.Background(), sectionID, tenantID, nil, 50)
		assert.NoError(t, err)
		assert.Len(t, got, 1)
		er.AssertExpectations(t)
	})

	t.Run("ListWaitlist", func(t *testing.T) {
		d := newTestSectionService()
		er := &mockEnrollmentRepo{}
		d.svc.SetEnrollmentRepo(er)
		er.On("ListWaitlisted", mock.Anything, sectionID, tenantID).Return([]SectionEnrollment{
			{ID: uuid.Must(uuid.NewV7()), Status: EnrollmentStatusWaitlisted},
		}, nil)
		got, err := d.svc.ListWaitlist(context.Background(), sectionID, tenantID)
		assert.NoError(t, err)
		assert.Len(t, got, 1)
		er.AssertExpectations(t)
	})

	t.Run("ListEnrolledByLearnerAndTerm", func(t *testing.T) {
		d := newTestSectionService()
		er := &mockEnrollmentRepo{}
		d.svc.SetEnrollmentRepo(er)
		er.On("ListByLearnerAndTerm", mock.Anything, learnerGCID, termID, tenantID).Return([]SectionEnrollment{
			{ID: uuid.Must(uuid.NewV7()), LearnerGCID: learnerGCID},
		}, nil)
		got, err := d.svc.ListEnrolledByLearnerAndTerm(context.Background(), learnerGCID, termID, tenantID)
		assert.NoError(t, err)
		assert.Len(t, got, 1)
		er.AssertExpectations(t)
	})

	t.Run("ListSectionsByTerm", func(t *testing.T) {
		d := newTestSectionService()
		d.sectionR.On("ListByTerm", mock.Anything, termID, tenantID).Return([]ClassSection{
			{ID: uuid.Must(uuid.NewV7()), AcademicTermID: termID},
		}, nil)
		got, err := d.svc.ListSectionsByTerm(context.Background(), termID, tenantID)
		assert.NoError(t, err)
		assert.Len(t, got, 1)
		d.sectionR.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// TimetableService coverage
// ---------------------------------------------------------------------------

func TestPublishTimetable_ErrorPaths(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())
	publishedBy := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())

	t.Run("list sections error", func(t *testing.T) {
		d := newTestTimetableService()
		d.sectionR.On("ListByTerm", mock.Anything, termID, tenantID).Return(nil, errTestCampus)
		_, err := d.svc.PublishTimetable(context.Background(), termID, publishedBy, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
	})

	t.Run("list timeslots error", func(t *testing.T) {
		d := newTestTimetableService()
		d.sectionR.On("ListByTerm", mock.Anything, termID, tenantID).Return([]ClassSection{
			{ID: sectionID, SectionCode: "SEC-1"},
		}, nil)
		d.slotR.On("ListBySection", mock.Anything, sectionID).Return(nil, errTestCampus)
		_, err := d.svc.PublishTimetable(context.Background(), termID, publishedBy, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
		d.slotR.AssertExpectations(t)
	})

	t.Run("get max version error", func(t *testing.T) {
		d := newTestTimetableService()
		d.sectionR.On("ListByTerm", mock.Anything, termID, tenantID).Return([]ClassSection{}, nil)
		d.timetableR.On("GetMaxVersion", mock.Anything, termID, tenantID).Return(0, errTestCampus)
		_, err := d.svc.PublishTimetable(context.Background(), termID, publishedBy, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
		d.timetableR.AssertExpectations(t)
	})

	t.Run("supersede error", func(t *testing.T) {
		d := newTestTimetableService()
		d.sectionR.On("ListByTerm", mock.Anything, termID, tenantID).Return([]ClassSection{}, nil)
		d.timetableR.On("GetMaxVersion", mock.Anything, termID, tenantID).Return(2, nil)
		d.timetableR.On("SupersedeAll", mock.Anything, termID, tenantID).Return(errTestCampus)
		_, err := d.svc.PublishTimetable(context.Background(), termID, publishedBy, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
		d.timetableR.AssertExpectations(t)
	})

	t.Run("create publication error", func(t *testing.T) {
		d := newTestTimetableService()
		d.sectionR.On("ListByTerm", mock.Anything, termID, tenantID).Return([]ClassSection{}, nil)
		d.timetableR.On("GetMaxVersion", mock.Anything, termID, tenantID).Return(0, nil)
		d.timetableR.On("SupersedeAll", mock.Anything, termID, tenantID).Return(nil)
		d.timetableR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.TimetablePublication")).Return(errTestCampus)
		_, err := d.svc.PublishTimetable(context.Background(), termID, publishedBy, tenantID)
		assert.ErrorIs(t, err, errTestCampus)
		d.sectionR.AssertExpectations(t)
		d.timetableR.AssertExpectations(t)
	})
}

func TestPublishTimetable_WithRoomID(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())
	publishedBy := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	roomID := uuid.Must(uuid.NewV7())

	d := newTestTimetableService()
	d.sectionR.On("ListByTerm", mock.Anything, termID, tenantID).Return([]ClassSection{
		{ID: sectionID, SectionCode: "SEC-1", InstructorGCID: uuid.Must(uuid.NewV7()), MaxCapacity: 30, EnrolledCount: 5, Status: SectionStatusActive},
	}, nil)
	d.slotR.On("ListBySection", mock.Anything, sectionID).Return([]SectionTimeSlot{
		{ID: uuid.Must(uuid.NewV7()), DayOfWeek: DayOfWeekMon, StartTime: "09:00", EndTime: "10:00", RoomID: &roomID},
	}, nil)
	d.timetableR.On("GetMaxVersion", mock.Anything, termID, tenantID).Return(1, nil)
	d.timetableR.On("SupersedeAll", mock.Anything, termID, tenantID).Return(nil)
	d.timetableR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.TimetablePublication")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(nil)

	got, err := d.svc.PublishTimetable(context.Background(), termID, publishedBy, tenantID)
	assert.NoError(t, err)
	assert.Equal(t, 2, got.Version)
	assert.Equal(t, 1, len(snapshotSections(got)))
	d.sectionR.AssertExpectations(t)
	d.slotR.AssertExpectations(t)
	d.timetableR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func snapshotSections(pub *TimetablePublication) []map[string]interface{} {
	sections, _ := pub.Snapshot["sections"].([]map[string]interface{})
	return sections
}

func TestGetTimetable_RepoError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())

	d := newTestTimetableService()
	d.timetableR.On("GetLatest", mock.Anything, termID, tenantID).Return(nil, errTestCampus)

	_, err := d.svc.GetTimetable(context.Background(), termID, tenantID)
	assert.ErrorIs(t, err, errTestCampus)
	d.timetableR.AssertExpectations(t)
}
func TestGetTerm_RepoError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	d := newTestTermService()
	d.termR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestCampus)

	_, err := d.svc.GetTerm(context.Background(), id, tenantID)
	assert.ErrorIs(t, err, errTestCampus)
	d.termR.AssertExpectations(t)
}
