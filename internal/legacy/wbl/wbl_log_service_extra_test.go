// wbl_log_service_extra_test.go — tops up WBLService coverage: the error
// paths of SubmitLog / ApproveLog / RejectLog / ResubmitLog (repo failure,
// publish failure, missing supervisor, empty/invalid resubmit inputs) plus
// the untested ListLogs and GetLog read methods.
package wbl

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestSubmitLog_ExtraBranches(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	supervisorGCID := uuid.Must(uuid.NewV7())

	// SubmitLog writes ID, Status, SubmittedAt, CreatedAt, UpdatedAt and
	// TopicNodeIDs THROUGH the caller's pointer, so one fixture shared across
	// t.Parallel() sub-tests is written by one sub-test while another reads it
	// (directly, or inside testify's Called argument capture). Each sub-test
	// owns a fresh log.
	newValidLog := func() *WBLLog {
		return &WBLLog{
			TenantID:            tenantID,
			GCID:                gcid,
			SupervisorGCID:      supervisorGCID,
			ActivityDescription: "Work",
			DurationHours:       2.0,
		}
	}

	t.Run("error: missing supervisor gcid", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		log := newValidLog()
		log.SupervisorGCID = uuid.Nil
		got, err := d.svc.SubmitLog(context.Background(), log)
		assert.ErrorIs(t, err, ErrValidationFailed)
		assert.Nil(t, got)
	})

	t.Run("error: repo create failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("db down")
		d.logRepo.On("Create", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(boom)
		got, err := d.svc.SubmitLog(context.Background(), newValidLog())
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})

	t.Run("error: publish failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("pubsub down")
		d.logRepo.On("Create", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(boom)
		got, err := d.svc.SubmitLog(context.Background(), newValidLog())
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})

	t.Run("success: nil topic node ids normalized", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("Create", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(nil)
		got, err := d.svc.SubmitLog(context.Background(), newValidLog())
		assert.NoError(t, err)
		assert.NotNil(t, got)
		assert.NotNil(t, got.TopicNodeIDs)
	})
}

func TestApproveLog_ExtraBranches(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	logID := uuid.Must(uuid.NewV7())

	newPending := func() *WBLLog {
		return &WBLLog{
			ID: logID, TenantID: tenantID, Status: WBLLogStatusPendingApproval,
			GCID: uuid.Must(uuid.NewV7()), SupervisorGCID: uuid.Must(uuid.NewV7()),
		}
	}

	t.Run("error: repo lookup failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("db down")
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(nil, boom)
		got, err := d.svc.ApproveLog(context.Background(), logID, tenantID, nil)
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})

	t.Run("error: update failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("db down")
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(newPending(), nil)
		d.logRepo.On("Update", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(boom)
		got, err := d.svc.ApproveLog(context.Background(), logID, tenantID, nil)
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})

	t.Run("error: publish failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("pubsub down")
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(newPending(), nil)
		d.logRepo.On("Update", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(boom)
		got, err := d.svc.ApproveLog(context.Background(), logID, tenantID, nil)
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})
}

func TestRejectLog_ExtraBranches(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	logID := uuid.Must(uuid.NewV7())

	newPending := func() *WBLLog {
		return &WBLLog{
			ID: logID, TenantID: tenantID, Status: WBLLogStatusPendingApproval,
			GCID: uuid.Must(uuid.NewV7()), SupervisorGCID: uuid.Must(uuid.NewV7()),
		}
	}

	t.Run("error: repo lookup failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("db down")
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(nil, boom)
		got, err := d.svc.RejectLog(context.Background(), logID, tenantID, "nope")
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})

	t.Run("error: not pending", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(&WBLLog{
			ID: logID, TenantID: tenantID, Status: WBLLogStatusApproved,
		}, nil)
		got, err := d.svc.RejectLog(context.Background(), logID, tenantID, "nope")
		assert.ErrorIs(t, err, ErrWBLLogNotPending)
		assert.Nil(t, got)
	})

	t.Run("error: update failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("db down")
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(newPending(), nil)
		d.logRepo.On("Update", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(boom)
		got, err := d.svc.RejectLog(context.Background(), logID, tenantID, "nope")
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})

	t.Run("error: publish failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("pubsub down")
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(newPending(), nil)
		d.logRepo.On("Update", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(boom)
		got, err := d.svc.RejectLog(context.Background(), logID, tenantID, "nope")
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})
}

func TestResubmitLog_ExtraBranches(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	logID := uuid.Must(uuid.NewV7())

	newRejected := func() *WBLLog {
		return &WBLLog{
			ID: logID, TenantID: tenantID, Status: WBLLogStatusRejected,
			GCID: uuid.Must(uuid.NewV7()), SupervisorGCID: uuid.Must(uuid.NewV7()),
		}
	}

	t.Run("error: repo lookup failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("db down")
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(nil, boom)
		got, err := d.svc.ResubmitLog(context.Background(), logID, tenantID, "desc", 1.0)
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})

	t.Run("error: empty description", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(newRejected(), nil)
		got, err := d.svc.ResubmitLog(context.Background(), logID, tenantID, "", 1.0)
		assert.ErrorIs(t, err, ErrValidationFailed)
		assert.Nil(t, got)
	})

	t.Run("error: zero duration", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(newRejected(), nil)
		got, err := d.svc.ResubmitLog(context.Background(), logID, tenantID, "desc", 0)
		assert.ErrorIs(t, err, ErrValidationFailed)
		assert.Nil(t, got)
	})

	t.Run("error: update failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("db down")
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(newRejected(), nil)
		d.logRepo.On("Update", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(boom)
		got, err := d.svc.ResubmitLog(context.Background(), logID, tenantID, "desc", 1.0)
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})

	t.Run("error: publish failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("pubsub down")
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(newRejected(), nil)
		d.logRepo.On("Update", mock.Anything, mock.AnythingOfType("*wbl.WBLLog")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicWBLEvents, mock.Anything).Return(boom)
		got, err := d.svc.ResubmitLog(context.Background(), logID, tenantID, "desc", 1.0)
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})
}

func TestListLogs(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	cursor := uuid.Must(uuid.NewV7())
	rows := []WBLLog{{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID}}

	t.Run("success forwards to repo", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("List", mock.Anything, tenantID, &cursor, 20).Return(rows, nil)
		got, err := d.svc.ListLogs(context.Background(), tenantID, &cursor, 20)
		assert.NoError(t, err)
		assert.Equal(t, rows, got)
	})

	t.Run("error propagates", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("db down")
		d.logRepo.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 0).Return(nil, boom)
		got, err := d.svc.ListLogs(context.Background(), tenantID, nil, 0)
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})
}

func TestGetLog(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	logID := uuid.Must(uuid.NewV7())
	logRow := &WBLLog{ID: logID, TenantID: tenantID}

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(logRow, nil)
		got, err := d.svc.GetLog(context.Background(), logID, tenantID)
		assert.NoError(t, err)
		assert.Equal(t, logRow, got)
	})

	t.Run("error: not found", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(nil, nil)
		got, err := d.svc.GetLog(context.Background(), logID, tenantID)
		assert.ErrorIs(t, err, ErrWBLLogNotFound)
		assert.Nil(t, got)
	})

	t.Run("error: repo failure", func(t *testing.T) {
		t.Parallel()
		d := newTestWBLLogService()
		boom := errors.New("db down")
		d.logRepo.On("GetByID", mock.Anything, logID, tenantID).Return(nil, boom)
		got, err := d.svc.GetLog(context.Background(), logID, tenantID)
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})
}
