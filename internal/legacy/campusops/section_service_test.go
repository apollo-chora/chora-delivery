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

type testSectionDeps struct {
	svc       *SectionService
	sectionR  *mockSectionRepo
	slotR     *mockTimeSlotRepo
	attendR   *mockAttendanceRepo
	publisher *mockEventPublisher
}

func newTestSectionService() testSectionDeps {
	sr := &mockSectionRepo{}
	tr := &mockTimeSlotRepo{}
	ar := &mockAttendanceRepo{}
	ep := &mockEventPublisher{}
	return testSectionDeps{
		svc:       NewSectionService(sr, tr, ar, ep),
		sectionR:  sr,
		slotR:     tr,
		attendR:   ar,
		publisher: ep,
	}
}

func TestCreateSection(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())
	instructorGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *ClassSection
		setupMocks   func(d testSectionDeps)
		wantErr      error
		assertResult func(t *testing.T, got *ClassSection)
	}{
		{
			name: "success: creates section with UUIDv7 and active status",
			input: &ClassSection{
				TenantID:          tenantID,
				SectionCode:       "CS101-A",
				TrainingSessionID: sessionID,
				AcademicTermID:    termID,
				InstructorGCID:    instructorGCID,
				MaxCapacity:       30,
			},
			setupMocks: func(d testSectionDeps) {
				d.sectionR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.ClassSection")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *ClassSection) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, SectionStatusActive, got.Status)
				assert.Equal(t, "CS101-A", got.SectionCode)
				assert.Equal(t, 0, got.EnrolledCount)
			},
		},
		{
			name: "fails: empty section_code",
			input: &ClassSection{
				TenantID:          tenantID,
				SectionCode:       "",
				TrainingSessionID: sessionID,
				AcademicTermID:    termID,
				InstructorGCID:    instructorGCID,
				MaxCapacity:       30,
			},
			setupMocks: func(d testSectionDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: zero max_capacity",
			input: &ClassSection{
				TenantID:          tenantID,
				SectionCode:       "CS102-A",
				TrainingSessionID: sessionID,
				AcademicTermID:    termID,
				InstructorGCID:    instructorGCID,
				MaxCapacity:       0,
			},
			setupMocks: func(d testSectionDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: repo error propagated",
			input: &ClassSection{
				TenantID:          tenantID,
				SectionCode:       "CS103-A",
				TrainingSessionID: sessionID,
				AcademicTermID:    termID,
				InstructorGCID:    instructorGCID,
				MaxCapacity:       25,
			},
			setupMocks: func(d testSectionDeps) {
				d.sectionR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.ClassSection")).
					Return(errors.New("unique violation"))
			},
			wantErr: errors.New("unique violation"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSectionService()
			tc.setupMocks(d)

			got, err := d.svc.CreateSection(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrValidationFailed) {
					assert.ErrorIs(t, err, ErrValidationFailed)
				}
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.sectionR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

func TestGetSection(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		tenantID   uuid.UUID
		setupMocks func(d testSectionDeps)
		wantErr    error
	}{
		{
			name:     "success: returns section",
			id:       sectionID,
			tenantID: tenantID,
			setupMocks: func(d testSectionDeps) {
				d.sectionR.On("GetByID", mock.Anything, sectionID, tenantID).Return(&ClassSection{
					ID: sectionID, TenantID: tenantID, SectionCode: "CS101-A",
				}, nil)
			},
		},
		{
			name:     "fails: not found",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupMocks: func(d testSectionDeps) {
				d.sectionR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrSectionNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSectionService()
			tc.setupMocks(d)

			got, err := d.svc.GetSection(context.Background(), tc.id, tc.tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}

			d.sectionR.AssertExpectations(t)
		})
	}
}

func TestAddTimeSlot(t *testing.T) {
	t.Parallel()

	sectionID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	tests := []struct {
		name         string
		input        *SectionTimeSlot
		setupMocks   func(d testSectionDeps)
		wantErr      error
		assertResult func(t *testing.T, got *SectionTimeSlot)
	}{
		{
			name: "success: adds time slot",
			input: &SectionTimeSlot{
				SectionID:     sectionID,
				DayOfWeek:     DayOfWeekMon,
				StartTime:     "09:00",
				EndTime:       "10:30",
				EffectiveFrom: now,
			},
			setupMocks: func(d testSectionDeps) {
				d.slotR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.SectionTimeSlot")).Return(nil)
			},
			assertResult: func(t *testing.T, got *SectionTimeSlot) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, sectionID, got.SectionID)
			},
		},
		{
			name: "fails: end_time before start_time",
			input: &SectionTimeSlot{
				SectionID:     sectionID,
				DayOfWeek:     DayOfWeekTue,
				StartTime:     "17:00",
				EndTime:       "09:00",
				EffectiveFrom: now,
			},
			setupMocks: func(d testSectionDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: invalid day_of_week",
			input: &SectionTimeSlot{
				SectionID:     sectionID,
				DayOfWeek:     DayOfWeek("funday"),
				StartTime:     "09:00",
				EndTime:       "10:00",
				EffectiveFrom: now,
			},
			setupMocks: func(d testSectionDeps) {},
			wantErr:    ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSectionService()
			tc.setupMocks(d)

			got, err := d.svc.AddTimeSlot(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrValidationFailed) {
					assert.ErrorIs(t, err, ErrValidationFailed)
				}
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.slotR.AssertExpectations(t)
		})
	}
}

func TestRecordAttendance(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())
	markerGCID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	tests := []struct {
		name       string
		records    []Attendance
		setupMocks func(d testSectionDeps)
		wantErr    error
	}{
		{
			name: "success: records batch attendance",
			records: []Attendance{
				{
					TenantID:      tenantID,
					SectionID:     sectionID,
					LearnerGCID:   learnerGCID,
					Status:        AttendanceStatusPresent,
					CheckInMethod: CheckInMethodQRScan,
					SessionDate:   now,
					MarkedByGCID:  markerGCID,
				},
				{
					TenantID:      tenantID,
					SectionID:     sectionID,
					LearnerGCID:   uuid.Must(uuid.NewV7()),
					Status:        AttendanceStatusLate,
					CheckInMethod: CheckInMethodManual,
					SessionDate:   now,
					MarkedByGCID:  markerGCID,
				},
			},
			setupMocks: func(d testSectionDeps) {
				d.attendR.On("Create", mock.Anything, mock.AnythingOfType("[]campusops.Attendance")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicCampusEvents, mock.Anything).Return(nil)
			},
		},
		{
			name: "fails: invalid attendance status",
			records: []Attendance{
				{
					TenantID:      tenantID,
					SectionID:     sectionID,
					LearnerGCID:   learnerGCID,
					Status:        AttendanceStatus("teleported"),
					CheckInMethod: CheckInMethodManual,
					SessionDate:   now,
					MarkedByGCID:  markerGCID,
				},
			},
			setupMocks: func(d testSectionDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: invalid check_in_method",
			records: []Attendance{
				{
					TenantID:      tenantID,
					SectionID:     sectionID,
					LearnerGCID:   learnerGCID,
					Status:        AttendanceStatusPresent,
					CheckInMethod: CheckInMethod("telepathy"),
					SessionDate:   now,
					MarkedByGCID:  markerGCID,
				},
			},
			setupMocks: func(d testSectionDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: repo error propagated",
			records: []Attendance{
				{
					TenantID:      tenantID,
					SectionID:     sectionID,
					LearnerGCID:   learnerGCID,
					Status:        AttendanceStatusPresent,
					CheckInMethod: CheckInMethodManual,
					SessionDate:   now,
					MarkedByGCID:  markerGCID,
				},
			},
			setupMocks: func(d testSectionDeps) {
				d.attendR.On("Create", mock.Anything, mock.AnythingOfType("[]campusops.Attendance")).
					Return(errors.New("write conflict"))
			},
			wantErr: errors.New("write conflict"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSectionService()
			tc.setupMocks(d)

			err := d.svc.RecordAttendance(context.Background(), tc.records)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrValidationFailed) {
					assert.ErrorIs(t, err, ErrValidationFailed)
				}
			} else {
				assert.NoError(t, err)
			}

			d.attendR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

func TestListAttendance(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	sectionID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	tests := []struct {
		name       string
		setupMocks func(d testSectionDeps)
		wantErr    error
		wantCount  int
	}{
		{
			name: "success: returns paginated list",
			setupMocks: func(d testSectionDeps) {
				d.attendR.On("ListBySection", mock.Anything, sectionID, tenantID, (*uuid.UUID)(nil), 20).
					Return([]Attendance{
						{
							ID: uuid.Must(uuid.NewV7()), TenantID: tenantID,
							SectionID: sectionID, LearnerGCID: uuid.Must(uuid.NewV7()),
							Status: AttendanceStatusPresent, SessionDate: now,
						},
					}, nil)
			},
			wantCount: 1,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testSectionDeps) {
				d.attendR.On("ListBySection", mock.Anything, sectionID, tenantID, (*uuid.UUID)(nil), 20).
					Return(nil, errors.New("connection refused"))
			},
			wantErr: errors.New("connection refused"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSectionService()
			tc.setupMocks(d)

			got, err := d.svc.ListAttendance(context.Background(), sectionID, tenantID, nil, 20)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.Len(t, got, tc.wantCount)
			}

			d.attendR.AssertExpectations(t)
		})
	}
}
