package wbl

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type testWBLLogDeps struct {
	svc       *WBLService
	logRepo   *mockWBLLogRepo
	publisher *mockEventPublisher
}

func newTestWBLLogService() testWBLLogDeps {
	lr := &mockWBLLogRepo{}
	p := &mockEventPublisher{}
	return testWBLLogDeps{
		svc:       NewWBLService(lr, p),
		logRepo:   lr,
		publisher: p,
	}
}

func TestSubmitLog(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	supervisorGCID := uuid.Must(uuid.NewV7())

	t.Run("success: submits log", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("Create", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)

		log := &WBLLog{
			TenantID:            tenantID,
			GCID:                gcid,
			SupervisorGCID:      supervisorGCID,
			ActivityDescription: "Configured CI/CD pipeline",
			DurationHours:       4.0,
		}

		got, err := d.svc.SubmitLog(context.Background(), log)
		assert.NoError(t, err)
		assert.NotNil(t, got)
		assert.Equal(t, WBLLogStatusPendingApproval, got.Status)
		assert.NotEqual(t, uuid.Nil, got.ID)
	})

	t.Run("error: empty description", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		log := &WBLLog{TenantID: tenantID, GCID: gcid, SupervisorGCID: supervisorGCID, DurationHours: 1.0}
		got, err := d.svc.SubmitLog(context.Background(), log)
		assert.ErrorIs(t, err, ErrValidationFailed)
		assert.Nil(t, got)
	})

	t.Run("error: zero duration", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		log := &WBLLog{TenantID: tenantID, GCID: gcid, SupervisorGCID: supervisorGCID, ActivityDescription: "Work", DurationHours: 0}
		got, err := d.svc.SubmitLog(context.Background(), log)
		assert.ErrorIs(t, err, ErrValidationFailed)
		assert.Nil(t, got)
	})
}

func TestApproveLog(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	logID := uuid.Must(uuid.NewV7())

	t.Run("success: approves log", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(&WBLLog{
			ID: logID, TenantID: tenantID, Status: WBLLogStatusPendingApproval,
			GCID: uuid.Must(uuid.NewV7()), SupervisorGCID: uuid.Must(uuid.NewV7()),
		}, nil)
		d.logRepo.On("Update", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)

		feedback := "Good work"
		got, err := d.svc.ApproveLog(context.Background(), logID, tenantID, &feedback)
		assert.NoError(t, err)
		assert.Equal(t, WBLLogStatusApproved, got.Status)
		assert.NotNil(t, got.ReviewedAt)
	})

	t.Run("error: log not found", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(nil, nil)
		got, err := d.svc.ApproveLog(context.Background(), logID, tenantID, nil)
		assert.ErrorIs(t, err, ErrWBLLogNotFound)
		assert.Nil(t, got)
	})

	t.Run("error: not pending", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(&WBLLog{
			ID: logID, TenantID: tenantID, Status: WBLLogStatusApproved,
		}, nil)
		got, err := d.svc.ApproveLog(context.Background(), logID, tenantID, nil)
		assert.ErrorIs(t, err, ErrWBLLogNotPending)
		assert.Nil(t, got)
	})
}

func TestRejectLog(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	logID := uuid.Must(uuid.NewV7())

	t.Run("success: rejects with feedback", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(&WBLLog{
			ID: logID, TenantID: tenantID, Status: WBLLogStatusPendingApproval,
			GCID: uuid.Must(uuid.NewV7()), SupervisorGCID: uuid.Must(uuid.NewV7()),
		}, nil)
		d.logRepo.On("Update", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)

		got, err := d.svc.RejectLog(context.Background(), logID, tenantID, "Needs more detail")
		assert.NoError(t, err)
		assert.Equal(t, WBLLogStatusRejected, got.Status)
	})

	t.Run("error: empty feedback", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(&WBLLog{
			ID: logID, TenantID: tenantID, Status: WBLLogStatusPendingApproval,
		}, nil)
		got, err := d.svc.RejectLog(context.Background(), logID, tenantID, "")
		assert.ErrorIs(t, err, ErrValidationFailed)
		assert.Nil(t, got)
	})
}

func TestResubmitLog(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	logID := uuid.Must(uuid.NewV7())

	t.Run("success: resubmits rejected log", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(&WBLLog{
			ID: logID, TenantID: tenantID, Status: WBLLogStatusRejected,
			GCID: uuid.Must(uuid.NewV7()), SupervisorGCID: uuid.Must(uuid.NewV7()),
		}, nil)
		d.logRepo.On("Update", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)

		got, err := d.svc.ResubmitLog(context.Background(), logID, tenantID, "Updated description", 3.0)
		assert.NoError(t, err)
		assert.Equal(t, WBLLogStatusPendingApproval, got.Status)
		assert.Equal(t, "Updated description", got.ActivityDescription)
	})

	t.Run("error: not rejected", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(&WBLLog{
			ID: logID, TenantID: tenantID, Status: WBLLogStatusApproved,
		}, nil)
		got, err := d.svc.ResubmitLog(context.Background(), logID, tenantID, "desc", 1.0)
		assert.ErrorIs(t, err, ErrWBLLogNotRejected)
		assert.Nil(t, got)
	})
}
