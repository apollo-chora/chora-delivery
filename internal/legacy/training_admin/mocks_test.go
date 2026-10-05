package training_admin

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
)

// Compile-time interface checks.
var (
	_ TrainingSessionRepository    = (*mockSessionRepo)(nil)
	_ AttendanceRepository         = (*mockAttendanceRepo)(nil)
	_ ScheduleRepository           = (*mockScheduleRepo)(nil)
	_ ApplicationRepository        = (*mockAppRepo)(nil)
	_ TraineeRequestRepository     = (*mockRequestRepo)(nil)
	_ CertificateProgramRepository = (*mockProgramRepo)(nil)
	_ ProgramEnrollmentRepository  = (*mockEnrollmentRepo)(nil)
	_ LearnerResultRepository      = (*mockResultRepo)(nil)
	_ AttendanceRecordRepository   = (*mockAttendRecordRepo)(nil)
	_ CertificateRequestRepository = (*mockCertReqRepo)(nil)
	_ StudentAppealRepository      = (*mockAppealRepo)(nil)
	_ SignageDeviceRepository      = (*mockDeviceRepo)(nil)
	_ EventPublisher               = (*mockEventPublisher)(nil)
)

// ---------------------------------------------------------------------------
// mockSessionRepo — TrainingSessionRepository
// ---------------------------------------------------------------------------

type mockSessionRepo struct{ mock.Mock }

func (m *mockSessionRepo) Create(ctx context.Context, session *TrainingSession) error {
	return m.Called(ctx, session).Error(0)
}

func (m *mockSessionRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*TrainingSession, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*TrainingSession), args.Error(1)
}

func (m *mockSessionRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]TrainingSession, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]TrainingSession), args.Error(1)
}

func (m *mockSessionRepo) Update(ctx context.Context, session *TrainingSession) error {
	return m.Called(ctx, session).Error(0)
}

func (m *mockSessionRepo) Delete(ctx context.Context, id, tenantID uuid.UUID) error {
	return m.Called(ctx, id, tenantID).Error(0)
}

// ---------------------------------------------------------------------------
// mockAttendanceRepo — AttendanceRepository
// ---------------------------------------------------------------------------

type mockAttendanceRepo struct{ mock.Mock }

func (m *mockAttendanceRepo) Create(ctx context.Context, records []Attendance) error {
	return m.Called(ctx, records).Error(0)
}

func (m *mockAttendanceRepo) ListBySession(ctx context.Context, sessionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Attendance, error) {
	args := m.Called(ctx, sessionID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]Attendance), args.Error(1)
}

func (m *mockAttendanceRepo) GetSummary(ctx context.Context, sessionID, tenantID uuid.UUID) (*AttendanceSummary, error) {
	args := m.Called(ctx, sessionID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AttendanceSummary), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockScheduleRepo — ScheduleRepository
// ---------------------------------------------------------------------------

type mockScheduleRepo struct{ mock.Mock }

func (m *mockScheduleRepo) Create(ctx context.Context, schedule *Schedule) error {
	return m.Called(ctx, schedule).Error(0)
}

func (m *mockScheduleRepo) ListBySession(ctx context.Context, sessionID uuid.UUID) ([]Schedule, error) {
	args := m.Called(ctx, sessionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]Schedule), args.Error(1)
}

func (m *mockScheduleRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// mockAppRepo — ApplicationRepository
// ---------------------------------------------------------------------------

type mockAppRepo struct{ mock.Mock }

func (m *mockAppRepo) Create(ctx context.Context, application *TrainingApplication) error {
	return m.Called(ctx, application).Error(0)
}

func (m *mockAppRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*TrainingApplication, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*TrainingApplication), args.Error(1)
}

func (m *mockAppRepo) List(ctx context.Context, sessionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]TrainingApplication, error) {
	args := m.Called(ctx, sessionID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]TrainingApplication), args.Error(1)
}

func (m *mockAppRepo) ListByStatus(ctx context.Context, tenantID uuid.UUID, status ApplicationStatus) ([]TrainingApplication, error) {
	args := m.Called(ctx, tenantID, status)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]TrainingApplication), args.Error(1)
}

func (m *mockAppRepo) Update(ctx context.Context, application *TrainingApplication) error {
	return m.Called(ctx, application).Error(0)
}

// ---------------------------------------------------------------------------
// mockRequestRepo — TraineeRequestRepository
// ---------------------------------------------------------------------------

type mockRequestRepo struct{ mock.Mock }

func (m *mockRequestRepo) Create(ctx context.Context, request *TraineeRequest) error {
	return m.Called(ctx, request).Error(0)
}

func (m *mockRequestRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*TraineeRequest, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*TraineeRequest), args.Error(1)
}

func (m *mockRequestRepo) List(ctx context.Context, sessionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]TraineeRequest, error) {
	args := m.Called(ctx, sessionID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]TraineeRequest), args.Error(1)
}

func (m *mockRequestRepo) ListByStatus(ctx context.Context, tenantID uuid.UUID, status RequestStatus) ([]TraineeRequest, error) {
	args := m.Called(ctx, tenantID, status)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]TraineeRequest), args.Error(1)
}

func (m *mockRequestRepo) Update(ctx context.Context, request *TraineeRequest) error {
	return m.Called(ctx, request).Error(0)
}

// ---------------------------------------------------------------------------
// mockProgramRepo — CertificateProgramRepository
// ---------------------------------------------------------------------------

type mockProgramRepo struct{ mock.Mock }

func (m *mockProgramRepo) Create(ctx context.Context, program *CertificateProgram) error {
	return m.Called(ctx, program).Error(0)
}

func (m *mockProgramRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*CertificateProgram, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*CertificateProgram), args.Error(1)
}

func (m *mockProgramRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]CertificateProgram, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]CertificateProgram), args.Error(1)
}

func (m *mockProgramRepo) Update(ctx context.Context, program *CertificateProgram) error {
	return m.Called(ctx, program).Error(0)
}

func (m *mockProgramRepo) Delete(ctx context.Context, id, tenantID uuid.UUID) error {
	return m.Called(ctx, id, tenantID).Error(0)
}

// ---------------------------------------------------------------------------
// mockEnrollmentRepo — ProgramEnrollmentRepository
// ---------------------------------------------------------------------------

type mockEnrollmentRepo struct{ mock.Mock }

func (m *mockEnrollmentRepo) Create(ctx context.Context, enrollment *ProgramEnrollment) error {
	return m.Called(ctx, enrollment).Error(0)
}

func (m *mockEnrollmentRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*ProgramEnrollment, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ProgramEnrollment), args.Error(1)
}

func (m *mockEnrollmentRepo) ListByProgram(ctx context.Context, programID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]ProgramEnrollment, error) {
	args := m.Called(ctx, programID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]ProgramEnrollment), args.Error(1)
}

func (m *mockEnrollmentRepo) Update(ctx context.Context, enrollment *ProgramEnrollment) error {
	return m.Called(ctx, enrollment).Error(0)
}

// ---------------------------------------------------------------------------
// mockResultRepo — LearnerResultRepository
// ---------------------------------------------------------------------------

type mockResultRepo struct{ mock.Mock }

func (m *mockResultRepo) Create(ctx context.Context, result *LearnerResult) error {
	return m.Called(ctx, result).Error(0)
}

func (m *mockResultRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*LearnerResult, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*LearnerResult), args.Error(1)
}

func (m *mockResultRepo) ListByLearner(ctx context.Context, learnerID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]LearnerResult, error) {
	args := m.Called(ctx, learnerID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]LearnerResult), args.Error(1)
}

func (m *mockResultRepo) ListBySession(ctx context.Context, sessionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]LearnerResult, error) {
	args := m.Called(ctx, sessionID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]LearnerResult), args.Error(1)
}

func (m *mockResultRepo) ListUnpublishedBySession(ctx context.Context, sessionID, tenantID uuid.UUID) ([]LearnerResult, error) {
	args := m.Called(ctx, sessionID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]LearnerResult), args.Error(1)
}

func (m *mockResultRepo) Update(ctx context.Context, result *LearnerResult) error {
	return m.Called(ctx, result).Error(0)
}

// ---------------------------------------------------------------------------
// mockAttendRecordRepo — AttendanceRecordRepository
// ---------------------------------------------------------------------------

type mockAttendRecordRepo struct{ mock.Mock }

func (m *mockAttendRecordRepo) ListByLearner(ctx context.Context, learnerID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]AttendanceRecord, error) {
	args := m.Called(ctx, learnerID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]AttendanceRecord), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockCertReqRepo — CertificateRequestRepository
// ---------------------------------------------------------------------------

type mockCertReqRepo struct{ mock.Mock }

func (m *mockCertReqRepo) Create(ctx context.Context, request *CertificateRequest) error {
	return m.Called(ctx, request).Error(0)
}

func (m *mockCertReqRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*CertificateRequest, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*CertificateRequest), args.Error(1)
}

func (m *mockCertReqRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]CertificateRequest, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]CertificateRequest), args.Error(1)
}

func (m *mockCertReqRepo) Update(ctx context.Context, request *CertificateRequest) error {
	return m.Called(ctx, request).Error(0)
}

// ---------------------------------------------------------------------------
// mockAppealRepo — StudentAppealRepository
// ---------------------------------------------------------------------------

type mockAppealRepo struct{ mock.Mock }

func (m *mockAppealRepo) Create(ctx context.Context, appeal *StudentAppeal) error {
	return m.Called(ctx, appeal).Error(0)
}

func (m *mockAppealRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*StudentAppeal, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*StudentAppeal), args.Error(1)
}

func (m *mockAppealRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]StudentAppeal, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]StudentAppeal), args.Error(1)
}

func (m *mockAppealRepo) Update(ctx context.Context, appeal *StudentAppeal) error {
	return m.Called(ctx, appeal).Error(0)
}

// ---------------------------------------------------------------------------
// mockDeviceRepo — SignageDeviceRepository
// ---------------------------------------------------------------------------

type mockDeviceRepo struct{ mock.Mock }

func (m *mockDeviceRepo) Create(ctx context.Context, device *SignageDevice) error {
	return m.Called(ctx, device).Error(0)
}

func (m *mockDeviceRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*SignageDevice, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*SignageDevice), args.Error(1)
}

func (m *mockDeviceRepo) GetByToken(ctx context.Context, token string) (*SignageDevice, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*SignageDevice), args.Error(1)
}

func (m *mockDeviceRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SignageDevice, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SignageDevice), args.Error(1)
}

func (m *mockDeviceRepo) Update(ctx context.Context, device *SignageDevice) error {
	return m.Called(ctx, device).Error(0)
}

func (m *mockDeviceRepo) Delete(ctx context.Context, id, tenantID uuid.UUID) error {
	return m.Called(ctx, id, tenantID).Error(0)
}

func (m *mockDeviceRepo) ListStaleDevices(ctx context.Context, tenantID uuid.UUID, staleDuration time.Duration) ([]SignageDevice, error) {
	args := m.Called(ctx, tenantID, staleDuration)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SignageDevice), args.Error(1)
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
