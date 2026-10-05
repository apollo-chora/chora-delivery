package wbl

import (
	"context"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
)

// Compile-time interface checks.
var (
	_ InternshipRepository  = (*mockInternshipRepo)(nil)
	_ ApplicationRepository = (*mockApplicationRepo)(nil)
	_ PlacementRepository   = (*mockPlacementRepo)(nil)
	_ WorkLogRepository     = (*mockWorkLogRepo)(nil)
	_ EndorsementRepository = (*mockEndorsementRepo)(nil)
	_ CapstoneRepository    = (*mockCapstoneRepo)(nil)
	_ SubmissionRepository  = (*mockSubmissionRepo)(nil)
	_ PartnerRepository     = (*mockPartnerRepo)(nil)
	_ WBLLogRepository      = (*mockWBLLogRepo)(nil)
	_ EventPublisher        = (*mockEventPublisher)(nil)
)

// ---------------------------------------------------------------------------
// mockInternshipRepo — InternshipRepository
// ---------------------------------------------------------------------------

type mockInternshipRepo struct{ mock.Mock }

func (m *mockInternshipRepo) Create(ctx context.Context, internship *Internship) error {
	return m.Called(ctx, internship).Error(0)
}

func (m *mockInternshipRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*Internship, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Internship), args.Error(1)
}

func (m *mockInternshipRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Internship, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]Internship), args.Error(1)
}

func (m *mockInternshipRepo) Update(ctx context.Context, internship *Internship) error {
	return m.Called(ctx, internship).Error(0)
}

// ---------------------------------------------------------------------------
// mockApplicationRepo — ApplicationRepository
// ---------------------------------------------------------------------------

type mockApplicationRepo struct{ mock.Mock }

func (m *mockApplicationRepo) Create(ctx context.Context, application *InternshipApplication) error {
	return m.Called(ctx, application).Error(0)
}

func (m *mockApplicationRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*InternshipApplication, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*InternshipApplication), args.Error(1)
}

func (m *mockApplicationRepo) ListByInternship(ctx context.Context, internshipID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]InternshipApplication, error) {
	args := m.Called(ctx, internshipID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]InternshipApplication), args.Error(1)
}

func (m *mockApplicationRepo) ListByApplicant(ctx context.Context, internshipID, tenantID, applicantGCID uuid.UUID) ([]InternshipApplication, error) {
	args := m.Called(ctx, internshipID, tenantID, applicantGCID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]InternshipApplication), args.Error(1)
}

func (m *mockApplicationRepo) Update(ctx context.Context, application *InternshipApplication) error {
	return m.Called(ctx, application).Error(0)
}

// ---------------------------------------------------------------------------
// mockPlacementRepo — PlacementRepository
// ---------------------------------------------------------------------------

type mockPlacementRepo struct{ mock.Mock }

func (m *mockPlacementRepo) Create(ctx context.Context, placement *Placement) error {
	return m.Called(ctx, placement).Error(0)
}

func (m *mockPlacementRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*Placement, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Placement), args.Error(1)
}

func (m *mockPlacementRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Placement, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]Placement), args.Error(1)
}

func (m *mockPlacementRepo) Update(ctx context.Context, placement *Placement) error {
	return m.Called(ctx, placement).Error(0)
}

// ---------------------------------------------------------------------------
// mockWorkLogRepo — WorkLogRepository
// ---------------------------------------------------------------------------

type mockWorkLogRepo struct{ mock.Mock }

func (m *mockWorkLogRepo) Create(ctx context.Context, entry *WorkLogEntry) error {
	return m.Called(ctx, entry).Error(0)
}

func (m *mockWorkLogRepo) ListByPlacement(ctx context.Context, placementID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]WorkLogEntry, error) {
	args := m.Called(ctx, placementID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]WorkLogEntry), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockEndorsementRepo — EndorsementRepository
// ---------------------------------------------------------------------------

type mockEndorsementRepo struct{ mock.Mock }

func (m *mockEndorsementRepo) Create(ctx context.Context, endorsement *SkillEndorsement) error {
	return m.Called(ctx, endorsement).Error(0)
}

func (m *mockEndorsementRepo) ListByPlacement(ctx context.Context, placementID, tenantID uuid.UUID) ([]SkillEndorsement, error) {
	args := m.Called(ctx, placementID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SkillEndorsement), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockCapstoneRepo — CapstoneRepository
// ---------------------------------------------------------------------------

type mockCapstoneRepo struct{ mock.Mock }

func (m *mockCapstoneRepo) Create(ctx context.Context, capstone *CapstoneProject) error {
	return m.Called(ctx, capstone).Error(0)
}

func (m *mockCapstoneRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*CapstoneProject, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*CapstoneProject), args.Error(1)
}

func (m *mockCapstoneRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]CapstoneProject, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]CapstoneProject), args.Error(1)
}

func (m *mockCapstoneRepo) Update(ctx context.Context, capstone *CapstoneProject) error {
	return m.Called(ctx, capstone).Error(0)
}

// ---------------------------------------------------------------------------
// mockSubmissionRepo — SubmissionRepository
// ---------------------------------------------------------------------------

type mockSubmissionRepo struct{ mock.Mock }

func (m *mockSubmissionRepo) Create(ctx context.Context, submission *CapstoneSubmission) error {
	return m.Called(ctx, submission).Error(0)
}

func (m *mockSubmissionRepo) ListByCapstone(ctx context.Context, capstoneID, tenantID uuid.UUID) ([]CapstoneSubmission, error) {
	args := m.Called(ctx, capstoneID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]CapstoneSubmission), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockPartnerRepo — PartnerRepository
// ---------------------------------------------------------------------------

type mockPartnerRepo struct{ mock.Mock }

func (m *mockPartnerRepo) Create(ctx context.Context, partner *IndustryPartner) error {
	return m.Called(ctx, partner).Error(0)
}

func (m *mockPartnerRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*IndustryPartner, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*IndustryPartner), args.Error(1)
}

func (m *mockPartnerRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]IndustryPartner, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]IndustryPartner), args.Error(1)
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

// ---------------------------------------------------------------------------
// mockWBLLogRepo — WBLLogRepository
// ---------------------------------------------------------------------------

type mockWBLLogRepo struct{ mock.Mock }

func (m *mockWBLLogRepo) Create(ctx context.Context, log *WBLLog) error {
	return m.Called(ctx, log).Error(0)
}

func (m *mockWBLLogRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*WBLLog, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*WBLLog), args.Error(1)
}

func (m *mockWBLLogRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]WBLLog, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]WBLLog), args.Error(1)
}

func (m *mockWBLLogRepo) Update(ctx context.Context, log *WBLLog) error {
	return m.Called(ctx, log).Error(0)
}
