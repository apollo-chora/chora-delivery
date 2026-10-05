package training_admin

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// testSignageDeps holds all mocks wired into a SignageService.
type testSignageDeps struct {
	svc       *SignageService
	deviceR   *mockDeviceRepo
	publisher *mockEventPublisher
}

// newTestSignageService creates a SignageService with fresh mocks.
func newTestSignageService() testSignageDeps {
	dr := &mockDeviceRepo{}
	ep := &mockEventPublisher{}
	return testSignageDeps{
		svc:       NewSignageService(dr, ep),
		deviceR:   dr,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// TestRegisterDevice
// ---------------------------------------------------------------------------

func TestRegisterDevice(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *SignageDevice
		setupMocks   func(d testSignageDeps)
		wantErr      error
		assertResult func(t *testing.T, got *SignageDevice)
	}{
		{
			name: "success: assigns token and offline status",
			input: &SignageDevice{
				TenantID:    tenantID,
				DeviceName:  "Lobby Display",
				DisplayMode: DisplayModeLeaderboard,
			},
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.SignageDevice")).Return(nil)
			},
			assertResult: func(t *testing.T, got *SignageDevice) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.NotEmpty(t, got.DeviceToken)
				assert.Equal(t, 32, len(got.DeviceToken))
				assert.Equal(t, DeviceStatusOffline, got.Status)
			},
		},
		{
			name: "fails: device_name required",
			input: &SignageDevice{
				TenantID:    tenantID,
				DeviceName:  "",
				DisplayMode: DisplayModeLeaderboard,
			},
			setupMocks: func(d testSignageDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: invalid display_mode",
			input: &SignageDevice{
				TenantID:    tenantID,
				DeviceName:  "Display",
				DisplayMode: DisplayMode("bogus"),
			},
			setupMocks: func(d testSignageDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: repo error",
			input: &SignageDevice{
				TenantID:    tenantID,
				DeviceName:  "Display",
				DisplayMode: DisplayModeTimetable,
			},
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.SignageDevice")).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSignageService()
			tc.setupMocks(d)

			got, err := d.svc.RegisterDevice(context.Background(), tc.input)

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
			d.deviceR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetDevice
// ---------------------------------------------------------------------------

func TestGetDevice(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testSignageDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByID", mock.Anything, id, tenantID).Return(&SignageDevice{ID: id}, nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrDeviceNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSignageService()
			tc.setupMocks(d)

			got, err := d.svc.GetDevice(context.Background(), id, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}
			d.deviceR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetDeviceByToken
// ---------------------------------------------------------------------------

func TestGetDeviceByToken(t *testing.T) {
	token := "token-123"

	tests := []struct {
		name       string
		setupMocks func(d testSignageDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, token).Return(&SignageDevice{DeviceToken: token}, nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, token).Return(nil, nil)
			},
			wantErr: ErrDeviceNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, token).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSignageService()
			tc.setupMocks(d)

			got, err := d.svc.GetDeviceByToken(context.Background(), token)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}
			d.deviceR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListDevices
// ---------------------------------------------------------------------------

func TestListDevices(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	d := newTestSignageService()
	d.deviceR.On("List", mock.Anything, tenantID, mock.Anything, 30).Return([]SignageDevice{
		{ID: uuid.Must(uuid.NewV7())},
	}, nil)

	got, err := d.svc.ListDevices(context.Background(), tenantID, nil, 30)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.deviceR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestUpdateDevice
// ---------------------------------------------------------------------------

func TestUpdateDevice(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *SignageDevice
		setupMocks   func(d testSignageDeps)
		wantErr      error
		assertResult func(t *testing.T, got *SignageDevice)
	}{
		{
			name: "success: applies mutable fields including explicit status",
			input: &SignageDevice{
				ID: id, TenantID: tenantID, DeviceName: "New Name",
				Status: DeviceStatusMaintenance,
			},
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByID", mock.Anything, id, tenantID).Return(&SignageDevice{ID: id, TenantID: tenantID, DeviceName: "Old"}, nil)
				d.deviceR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.SignageDevice")).Return(nil)
			},
			assertResult: func(t *testing.T, got *SignageDevice) {
				assert.Equal(t, "New Name", got.DeviceName)
				assert.Equal(t, DeviceStatusMaintenance, got.Status)
			},
		},
		{
			name: "success: empty status preserves existing status",
			input: &SignageDevice{
				ID: id, TenantID: tenantID, DeviceName: "New Name",
			},
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByID", mock.Anything, id, tenantID).Return(&SignageDevice{ID: id, TenantID: tenantID, Status: DeviceStatusOnline}, nil)
				d.deviceR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.SignageDevice")).Return(nil)
			},
			assertResult: func(t *testing.T, got *SignageDevice) {
				assert.Equal(t, DeviceStatusOnline, got.Status)
			},
		},
		{
			name:  "fails: not found",
			input: &SignageDevice{ID: id, TenantID: tenantID},
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrDeviceNotFound,
		},
		{
			name:  "fails: get repo error",
			input: &SignageDevice{ID: id, TenantID: tenantID},
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
		{
			name:  "fails: update repo error",
			input: &SignageDevice{ID: id, TenantID: tenantID, DeviceName: "X"},
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByID", mock.Anything, id, tenantID).Return(&SignageDevice{ID: id}, nil)
				d.deviceR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.SignageDevice")).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSignageService()
			tc.setupMocks(d)

			got, err := d.svc.UpdateDevice(context.Background(), tc.input)

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
			d.deviceR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestDeleteDevice
// ---------------------------------------------------------------------------

func TestDeleteDevice(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testSignageDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByID", mock.Anything, id, tenantID).Return(&SignageDevice{ID: id}, nil)
				d.deviceR.On("Delete", mock.Anything, id, tenantID).Return(nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrDeviceNotFound,
		},
		{
			name: "fails: get repo error",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSignageService()
			tc.setupMocks(d)

			err := d.svc.DeleteDevice(context.Background(), id, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
			d.deviceR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestRecordHeartbeat
// ---------------------------------------------------------------------------

func TestRecordHeartbeat(t *testing.T) {
	token := "token-abc"

	tests := []struct {
		name         string
		setupMocks   func(d testSignageDeps)
		wantErr      error
		assertResult func(t *testing.T, got *SignageDevice)
	}{
		{
			name: "success: sets status online and last heartbeat",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, token).Return(&SignageDevice{DeviceToken: token, Status: DeviceStatusOffline}, nil)
				d.deviceR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.SignageDevice")).Return(nil)
			},
			assertResult: func(t *testing.T, got *SignageDevice) {
				assert.Equal(t, DeviceStatusOnline, got.Status)
				assert.NotNil(t, got.LastHeartbeatAt)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, token).Return(nil, nil)
			},
			wantErr: ErrDeviceNotFound,
		},
		{
			name: "fails: get repo error",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, token).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
		{
			name: "fails: update repo error",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, token).Return(&SignageDevice{DeviceToken: token}, nil)
				d.deviceR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.SignageDevice")).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSignageService()
			tc.setupMocks(d)

			got, err := d.svc.RecordHeartbeat(context.Background(), token)

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
			d.deviceR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestMarkStaleDevicesOffline
// ---------------------------------------------------------------------------

func TestMarkStaleDevicesOffline(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	stale := 5 * time.Minute

	tests := []struct {
		name       string
		setupMocks func(d testSignageDeps)
		wantErr    error
		wantCount  int
	}{
		{
			name: "success: marks stale devices offline",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("ListStaleDevices", mock.Anything, tenantID, stale).Return([]SignageDevice{
					{ID: uuid.Must(uuid.NewV7()), Status: DeviceStatusOnline},
					{ID: uuid.Must(uuid.NewV7()), Status: DeviceStatusOnline},
				}, nil)
				d.deviceR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.SignageDevice")).Return(nil).Twice()
			},
			wantCount: 2,
		},
		{
			name: "success: no stale devices",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("ListStaleDevices", mock.Anything, tenantID, stale).Return([]SignageDevice{}, nil)
			},
			wantCount: 0,
		},
		{
			name: "fails: list repo error",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("ListStaleDevices", mock.Anything, tenantID, stale).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
		{
			name: "fails: update error returns partial count",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("ListStaleDevices", mock.Anything, tenantID, stale).Return([]SignageDevice{
					{ID: uuid.Must(uuid.NewV7())},
					{ID: uuid.Must(uuid.NewV7())},
				}, nil)
				d.deviceR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.SignageDevice")).Return(errTestRepo).Once()
			},
			wantErr:   errTestRepo,
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSignageService()
			tc.setupMocks(d)

			count, err := d.svc.MarkStaleDevicesOffline(context.Background(), tenantID, stale)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Equal(t, tc.wantCount, count)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.wantCount, count)
			}
			d.deviceR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetContent
// ---------------------------------------------------------------------------

func TestGetContent(t *testing.T) {
	tests := []struct {
		name         string
		displayMode  DisplayMode
		setupMocks   func(d testSignageDeps)
		wantErr      error
		assertResult func(t *testing.T, got *SignageContent)
	}{
		{
			name:        "success: leaderboard mode",
			displayMode: DisplayModeLeaderboard,
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, "tok").Return(&SignageDevice{DeviceToken: "tok", DisplayMode: DisplayModeLeaderboard}, nil)
			},
			assertResult: func(t *testing.T, got *SignageContent) {
				assert.NotNil(t, got.Leaderboard)
			},
		},
		{
			name:        "success: timetable mode",
			displayMode: DisplayModeTimetable,
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, "tok").Return(&SignageDevice{DeviceToken: "tok", DisplayMode: DisplayModeTimetable}, nil)
			},
			assertResult: func(t *testing.T, got *SignageContent) {
				assert.NotNil(t, got.Timetable)
			},
		},
		{
			name:        "success: announcements mode",
			displayMode: DisplayModeAnnouncements,
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, "tok").Return(&SignageDevice{DeviceToken: "tok", DisplayMode: DisplayModeAnnouncements}, nil)
			},
			assertResult: func(t *testing.T, got *SignageContent) {
				assert.NotNil(t, got.Announcements)
			},
		},
		{
			name:        "success: mixed mode",
			displayMode: DisplayModeMixed,
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, "tok").Return(&SignageDevice{DeviceToken: "tok", DisplayMode: DisplayModeMixed}, nil)
			},
			assertResult: func(t *testing.T, got *SignageContent) {
				assert.NotNil(t, got.Leaderboard)
				assert.NotNil(t, got.Timetable)
				assert.NotNil(t, got.Announcements)
			},
		},
		{
			name:        "success: unknown mode returns empty content",
			displayMode: DisplayMode("custom"),
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, "tok").Return(&SignageDevice{DeviceToken: "tok", DisplayMode: DisplayMode("custom")}, nil)
			},
			assertResult: func(t *testing.T, got *SignageContent) {
				assert.Nil(t, got.Leaderboard)
				assert.Nil(t, got.Timetable)
				assert.Nil(t, got.Announcements)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, "tok").Return(nil, nil)
			},
			wantErr: ErrDeviceNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, "tok").Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSignageService()
			tc.setupMocks(d)

			got, err := d.svc.GetContent(context.Background(), "tok")

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
			d.deviceR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetDisplayURL
// ---------------------------------------------------------------------------

func TestGetDisplayURL(t *testing.T) {
	tests := []struct {
		name       string
		setupMocks func(d testSignageDeps)
		wantErr    error
		wantURL    string
		wantMode   DisplayMode
	}{
		{
			name: "success",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, "tok-1").Return(&SignageDevice{DeviceToken: "tok-1", DisplayMode: DisplayModeTimetable}, nil)
			},
			wantURL:  "/api/v1/training/signage/devices/tok-1/content",
			wantMode: DisplayModeTimetable,
		},
		{
			name: "fails: not found",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, "tok-1").Return(nil, nil)
			},
			wantErr: ErrDeviceNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testSignageDeps) {
				d.deviceR.On("GetByToken", mock.Anything, "tok-1").Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestSignageService()
			tc.setupMocks(d)

			url, mode, err := d.svc.GetDisplayURL(context.Background(), "tok-1")

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.wantURL, url)
				assert.Equal(t, tc.wantMode, mode)
			}
			d.deviceR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGenerateDeviceToken
// ---------------------------------------------------------------------------

func TestGenerateDeviceToken(t *testing.T) {
	tok1 := generateDeviceToken()
	tok2 := generateDeviceToken()

	assert.Len(t, tok1, 32)
	assert.Len(t, tok2, 32)
	assert.NotEqual(t, tok1, tok2)
	// Tokens must be valid lowercase hex (crypto/rand path).
	_, err := hex.DecodeString(tok1)
	assert.NoError(t, err)
}
