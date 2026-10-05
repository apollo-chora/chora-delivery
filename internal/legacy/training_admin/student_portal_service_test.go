package training_admin

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// errTestRepo is a shared sentinel error used to assert wrapped repo errors.
var errTestRepo = errors.New("test repo error")

// testStudentPortalDeps holds all mocks wired into a StudentPortalService.
type testStudentPortalDeps struct {
	svc       *StudentPortalService
	resultR   *mockResultRepo
	attendR   *mockAttendRecordRepo
	certReqR  *mockCertReqRepo
	appealR   *mockAppealRepo
	appR      *mockAppRepo
	publisher *mockEventPublisher
}

// newTestStudentPortalService creates a StudentPortalService with fresh mocks.
func newTestStudentPortalService() testStudentPortalDeps {
	rr := &mockResultRepo{}
	ar := &mockAttendRecordRepo{}
	cr := &mockCertReqRepo{}
	ap := &mockAppealRepo{}
	app := &mockAppRepo{}
	ep := &mockEventPublisher{}
	return testStudentPortalDeps{
		svc:       NewStudentPortalService(rr, ar, cr, ap, app, ep),
		resultR:   rr,
		attendR:   ar,
		certReqR:  cr,
		appealR:   ap,
		appR:      app,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// TestCreateResult
// ---------------------------------------------------------------------------

func TestCreateResult(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	learnerID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *LearnerResult
		setupMocks   func(d testStudentPortalDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LearnerResult)
	}{
		{
			name: "success: assigns IDs and defaults published=false",
			input: &LearnerResult{
				TenantID:       tenantID,
				SessionID:      sessionID,
				LearnerID:      learnerID,
				AssessmentType: AssessmentTypeQuiz,
				Score:          85,
				MaxScore:       100,
			},
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.LearnerResult")).Return(nil)
			},
			assertResult: func(t *testing.T, got *LearnerResult) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.False(t, got.Published)
				assert.Equal(t, sessionID, got.SessionID)
			},
		},
		{
			name: "fails: session_id required",
			input: &LearnerResult{
				SessionID:      uuid.Nil,
				LearnerID:      learnerID,
				AssessmentType: AssessmentTypeQuiz,
				MaxScore:       100,
			},
			setupMocks: func(d testStudentPortalDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: learner_id required",
			input: &LearnerResult{
				SessionID:      sessionID,
				LearnerID:      uuid.Nil,
				AssessmentType: AssessmentTypeQuiz,
				MaxScore:       100,
			},
			setupMocks: func(d testStudentPortalDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: invalid assessment_type",
			input: &LearnerResult{
				SessionID:      sessionID,
				LearnerID:      learnerID,
				AssessmentType: AssessmentType("bogus"),
				MaxScore:       100,
			},
			setupMocks: func(d testStudentPortalDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: max_score must be greater than 0",
			input: &LearnerResult{
				SessionID:      sessionID,
				LearnerID:      learnerID,
				AssessmentType: AssessmentTypeQuiz,
				MaxScore:       0,
			},
			setupMocks: func(d testStudentPortalDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: repo error",
			input: &LearnerResult{
				TenantID:       tenantID,
				SessionID:      sessionID,
				LearnerID:      learnerID,
				AssessmentType: AssessmentTypeQuiz,
				MaxScore:       100,
			},
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.LearnerResult")).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			got, err := d.svc.CreateResult(context.Background(), tc.input)

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
			d.resultR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetResult
// ---------------------------------------------------------------------------

func TestGetResult(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	resultID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testStudentPortalDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LearnerResult)
	}{
		{
			name: "success: returns existing result",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("GetByID", mock.Anything, resultID, tenantID).Return(&LearnerResult{ID: resultID, TenantID: tenantID}, nil)
			},
			assertResult: func(t *testing.T, got *LearnerResult) {
				assert.Equal(t, resultID, got.ID)
			},
		},
		{
			name: "fails: not found returns ErrResultNotFound",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("GetByID", mock.Anything, resultID, tenantID).Return(nil, nil)
			},
			wantErr: ErrResultNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("GetByID", mock.Anything, resultID, tenantID).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			got, err := d.svc.GetResult(context.Background(), resultID, tenantID)

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
			d.resultR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListResults
// ---------------------------------------------------------------------------

func TestListResults(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	learnerID := uuid.Must(uuid.NewV7())

	d := newTestStudentPortalService()
	d.resultR.On("ListByLearner", mock.Anything, learnerID, tenantID, mock.Anything, 50).Return([]LearnerResult{
		{ID: uuid.Must(uuid.NewV7()), LearnerID: learnerID},
	}, nil)

	got, err := d.svc.ListResults(context.Background(), learnerID, tenantID, nil, 50)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.resultR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestGetResultsSummary
// ---------------------------------------------------------------------------

func TestGetResultsSummary(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	learnerID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testStudentPortalDeps)
		wantErr      error
		assertResult func(t *testing.T, got *ResultsSummary)
	}{
		{
			name: "success: aggregates published results by type",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("ListByLearner", mock.Anything, learnerID, tenantID, mock.Anything, 1000).Return([]LearnerResult{
					{AssessmentType: AssessmentTypeQuiz, Published: true, Score: 80, MaxScore: 100},
					{AssessmentType: AssessmentTypeQuiz, Published: true, Score: 60, MaxScore: 100},
					{AssessmentType: AssessmentTypeExam, Published: true, Score: 90, MaxScore: 100},
					{AssessmentType: AssessmentTypeExam, Published: false, Score: 10, MaxScore: 100},
				}, nil)
			},
			assertResult: func(t *testing.T, got *ResultsSummary) {
				assert.Equal(t, learnerID, got.LearnerID)
				assert.Equal(t, 3, got.TotalResults)
				assert.InDelta(t, (80+60+90)/3.0, got.AverageScore, 0.001)
				assert.Equal(t, 2, got.ResultsByType[string(AssessmentTypeQuiz)].Count)
				assert.Equal(t, 70.0, got.ResultsByType[string(AssessmentTypeQuiz)].AverageScore)
				assert.Equal(t, 1, got.ResultsByType[string(AssessmentTypeExam)].Count)
			},
		},
		{
			name: "success: no results returns empty summary",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("ListByLearner", mock.Anything, learnerID, tenantID, mock.Anything, 1000).Return([]LearnerResult{}, nil)
			},
			assertResult: func(t *testing.T, got *ResultsSummary) {
				assert.Equal(t, learnerID, got.LearnerID)
				assert.Equal(t, 0, got.TotalResults)
				assert.Empty(t, got.ResultsByType)
			},
		},
		{
			name: "success: all unpublished results count as zero",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("ListByLearner", mock.Anything, learnerID, tenantID, mock.Anything, 1000).Return([]LearnerResult{
					{AssessmentType: AssessmentTypeQuiz, Published: false, Score: 80, MaxScore: 100},
				}, nil)
			},
			assertResult: func(t *testing.T, got *ResultsSummary) {
				assert.Equal(t, 0, got.TotalResults)
				assert.Equal(t, 0.0, got.AverageScore)
			},
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("ListByLearner", mock.Anything, learnerID, tenantID, mock.Anything, 1000).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			got, err := d.svc.GetResultsSummary(context.Background(), learnerID, tenantID)

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
			d.resultR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestUpdateResult
// ---------------------------------------------------------------------------

func TestUpdateResult(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	resultID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testStudentPortalDeps)
		wantErr      error
		assertResult func(t *testing.T, got *LearnerResult)
	}{
		{
			name: "success: applies mutable fields",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("GetByID", mock.Anything, resultID, tenantID).Return(&LearnerResult{ID: resultID, TenantID: tenantID, Score: 10}, nil)
				d.resultR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.LearnerResult")).Return(nil)
			},
			assertResult: func(t *testing.T, got *LearnerResult) {
				assert.Equal(t, 95.0, got.Score)
				assert.Equal(t, 100.0, got.MaxScore)
			},
		},
		{
			name: "fails: result not found",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("GetByID", mock.Anything, resultID, tenantID).Return(nil, nil)
			},
			wantErr: ErrResultNotFound,
		},
		{
			name: "fails: get repo error",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("GetByID", mock.Anything, resultID, tenantID).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
		{
			name: "fails: update repo error",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("GetByID", mock.Anything, resultID, tenantID).Return(&LearnerResult{ID: resultID, TenantID: tenantID}, nil)
				d.resultR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.LearnerResult")).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			grade := "A"
			got, err := d.svc.UpdateResult(context.Background(), &LearnerResult{
				ID: resultID, TenantID: tenantID, Score: 95, MaxScore: 100, Grade: &grade,
			})

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
			d.resultR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestPublishSessionResults
// ---------------------------------------------------------------------------

func TestPublishSessionResults(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionID := uuid.Must(uuid.NewV7())
	publisherGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testStudentPortalDeps)
		wantErr    error
		wantCount  int
	}{
		{
			name: "success: publishes results and emits event",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("ListUnpublishedBySession", mock.Anything, sessionID, tenantID).Return([]LearnerResult{
					{ID: uuid.Must(uuid.NewV7()), Published: false},
					{ID: uuid.Must(uuid.NewV7()), Published: false},
				}, nil)
				d.resultR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.LearnerResult")).Return(nil).Twice()
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
			wantCount: 2,
		},
		{
			name: "success: no unpublished results means no event",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("ListUnpublishedBySession", mock.Anything, sessionID, tenantID).Return([]LearnerResult{}, nil)
			},
			wantCount: 0,
		},
		{
			name: "fails: list repo error",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("ListUnpublishedBySession", mock.Anything, sessionID, tenantID).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
		{
			name: "fails: update error mid-publish returns partial count",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("ListUnpublishedBySession", mock.Anything, sessionID, tenantID).Return([]LearnerResult{
					{ID: uuid.Must(uuid.NewV7())},
					{ID: uuid.Must(uuid.NewV7())},
				}, nil)
				d.resultR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.LearnerResult")).Return(errTestRepo).Once()
			},
			wantErr:   errTestRepo,
			wantCount: 0,
		},
		{
			name: "fails: event publish error returns count with error",
			setupMocks: func(d testStudentPortalDeps) {
				d.resultR.On("ListUnpublishedBySession", mock.Anything, sessionID, tenantID).Return([]LearnerResult{
					{ID: uuid.Must(uuid.NewV7())},
				}, nil)
				d.resultR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.LearnerResult")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)
			},
			wantErr:   errTestRepo,
			wantCount: 1,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			count, err := d.svc.PublishSessionResults(context.Background(), sessionID, tenantID, publisherGCID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Equal(t, tc.wantCount, count)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.wantCount, count)
			}
			d.resultR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListAttendanceRecords
// ---------------------------------------------------------------------------

func TestListAttendanceRecords(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	learnerID := uuid.Must(uuid.NewV7())

	d := newTestStudentPortalService()
	d.attendR.On("ListByLearner", mock.Anything, learnerID, tenantID, mock.Anything, 20).Return([]AttendanceRecord{
		{ID: uuid.Must(uuid.NewV7()), LearnerID: learnerID, Attended: 5},
	}, nil)

	got, err := d.svc.ListAttendanceRecords(context.Background(), learnerID, tenantID, nil, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, 5, got[0].Attended)
	d.attendR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestCreateCertificateRequest
// ---------------------------------------------------------------------------

func TestCreateCertificateRequest(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	programID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *CertificateRequest
		setupMocks   func(d testStudentPortalDeps)
		wantErr      error
		assertResult func(t *testing.T, got *CertificateRequest)
	}{
		{
			name: "success: assigns submitted status",
			input: &CertificateRequest{
				TenantID:  tenantID,
				LearnerID: uuid.Must(uuid.NewV7()),
				ProgramID: programID,
			},
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.CertificateRequest")).Return(nil)
			},
			assertResult: func(t *testing.T, got *CertificateRequest) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, CertReqStatusSubmitted, got.Status)
			},
		},
		{
			name: "fails: program_id required",
			input: &CertificateRequest{
				TenantID:  tenantID,
				ProgramID: uuid.Nil,
			},
			setupMocks: func(d testStudentPortalDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: repo error",
			input: &CertificateRequest{
				TenantID:  tenantID,
				ProgramID: programID,
			},
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.CertificateRequest")).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			got, err := d.svc.CreateCertificateRequest(context.Background(), tc.input)

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
			d.certReqR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetCertificateRequest
// ---------------------------------------------------------------------------

func TestGetCertificateRequest(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testStudentPortalDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(&CertificateRequest{ID: id}, nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrCertificateRequestNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			got, err := d.svc.GetCertificateRequest(context.Background(), id, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}
			d.certReqR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListCertificateRequests
// ---------------------------------------------------------------------------

func TestListCertificateRequests(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	d := newTestStudentPortalService()
	d.certReqR.On("List", mock.Anything, tenantID, mock.Anything, 25).Return([]CertificateRequest{
		{ID: uuid.Must(uuid.NewV7())},
	}, nil)

	got, err := d.svc.ListCertificateRequests(context.Background(), tenantID, nil, 25)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.certReqR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestReviewCertificateRequest
// ---------------------------------------------------------------------------

func TestReviewCertificateRequest(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())
	reviewerGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		approved     bool
		setupMocks   func(d testStudentPortalDeps)
		wantErr      error
		assertResult func(t *testing.T, got *CertificateRequest)
	}{
		{
			name:     "success: approve",
			approved: true,
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(&CertificateRequest{ID: id, Status: CertReqStatusSubmitted}, nil)
				d.certReqR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.CertificateRequest")).Return(nil)
			},
			assertResult: func(t *testing.T, got *CertificateRequest) {
				assert.Equal(t, CertReqStatusApproved, got.Status)
				assert.NotNil(t, got.ReviewerID)
				assert.NotNil(t, got.ReviewedAt)
			},
		},
		{
			name:     "success: reject with reason",
			approved: false,
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(&CertificateRequest{ID: id, Status: CertReqStatusUnderReview}, nil)
				d.certReqR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.CertificateRequest")).Return(nil)
			},
			assertResult: func(t *testing.T, got *CertificateRequest) {
				assert.Equal(t, CertReqStatusRejected, got.Status)
				assert.NotNil(t, got.RejectionReason)
			},
		},
		{
			name:     "fails: not reviewable",
			approved: true,
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(&CertificateRequest{ID: id, Status: CertReqStatusIssued}, nil)
			},
			wantErr: ErrCertificateRequestNotReviewable,
		},
		{
			name:     "fails: not found",
			approved: true,
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrCertificateRequestNotFound,
		},
		{
			name:     "fails: repo error",
			approved: true,
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
		{
			name:     "fails: update error",
			approved: true,
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(&CertificateRequest{ID: id, Status: CertReqStatusSubmitted}, nil)
				d.certReqR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.CertificateRequest")).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			reason := ptrString("missing transcript")
			got, err := d.svc.ReviewCertificateRequest(context.Background(), id, tenantID, tc.approved, reason, reviewerGCID)

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
			d.certReqR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestIssueCertificate
// ---------------------------------------------------------------------------

func TestIssueCertificate(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testStudentPortalDeps)
		wantErr      error
		assertResult func(t *testing.T, got *CertificateRequest)
	}{
		{
			name: "success: issues certificate and publishes event",
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(&CertificateRequest{
					ID: id, TenantID: tenantID, LearnerID: uuid.Must(uuid.NewV7()), ProgramID: uuid.Must(uuid.NewV7()), Status: CertReqStatusApproved,
				}, nil)
				d.certReqR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.CertificateRequest")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *CertificateRequest) {
				assert.Equal(t, CertReqStatusIssued, got.Status)
				assert.NotNil(t, got.CertificateURL)
			},
		},
		{
			name: "fails: not approved",
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(&CertificateRequest{ID: id, Status: CertReqStatusRejected}, nil)
			},
			wantErr: ErrCertificateRequestNotIssuable,
		},
		{
			name: "fails: not found",
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrCertificateRequestNotFound,
		},
		{
			name: "fails: update error",
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(&CertificateRequest{ID: id, Status: CertReqStatusApproved}, nil)
				d.certReqR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.CertificateRequest")).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
		{
			name: "fails: event publish error",
			setupMocks: func(d testStudentPortalDeps) {
				d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(&CertificateRequest{ID: id, Status: CertReqStatusApproved}, nil)
				d.certReqR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.CertificateRequest")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			got, err := d.svc.IssueCertificate(context.Background(), id, tenantID)

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
			d.certReqR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestCreateAppeal
// ---------------------------------------------------------------------------

func TestCreateAppeal(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *StudentAppeal
		setupMocks   func(d testStudentPortalDeps)
		wantErr      error
		assertResult func(t *testing.T, got *StudentAppeal)
	}{
		{
			name: "success: creates appeal and publishes event",
			input: &StudentAppeal{
				TenantID:   tenantID,
				LearnerID:  uuid.Must(uuid.NewV7()),
				ResultID:   uuid.Must(uuid.NewV7()),
				AppealType: AppealTypeGradeReview,
				Reason:     "Wrongly graded",
			},
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.StudentAppeal")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *StudentAppeal) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, AppealStatusSubmitted, got.Status)
			},
		},
		{
			name: "fails: result_id required",
			input: &StudentAppeal{
				ResultID:   uuid.Nil,
				AppealType: AppealTypeOther,
				Reason:     "because",
			},
			setupMocks: func(d testStudentPortalDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: invalid appeal type",
			input: &StudentAppeal{
				ResultID:   uuid.Must(uuid.NewV7()),
				AppealType: AppealType("nope"),
				Reason:     "because",
			},
			setupMocks: func(d testStudentPortalDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: reason required",
			input: &StudentAppeal{
				ResultID:   uuid.Must(uuid.NewV7()),
				AppealType: AppealTypeOther,
				Reason:     "",
			},
			setupMocks: func(d testStudentPortalDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: repo error",
			input: &StudentAppeal{
				ResultID: uuid.Must(uuid.NewV7()), AppealType: AppealTypeOther, Reason: "because",
			},
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.StudentAppeal")).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
		{
			name: "fails: publish error",
			input: &StudentAppeal{
				ResultID: uuid.Must(uuid.NewV7()), AppealType: AppealTypeOther, Reason: "because",
			},
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("Create", mock.Anything, mock.AnythingOfType("*training_admin.StudentAppeal")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			got, err := d.svc.CreateAppeal(context.Background(), tc.input)

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
			d.appealR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetAppeal
// ---------------------------------------------------------------------------

func TestGetAppeal(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		setupMocks func(d testStudentPortalDeps)
		wantErr    error
	}{
		{
			name: "success",
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(&StudentAppeal{ID: id}, nil)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrAppealNotFound,
		},
		{
			name: "fails: repo error",
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			got, err := d.svc.GetAppeal(context.Background(), id, tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}
			d.appealR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListAppeals
// ---------------------------------------------------------------------------

func TestListAppeals(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	d := newTestStudentPortalService()
	d.appealR.On("List", mock.Anything, tenantID, mock.Anything, 10).Return([]StudentAppeal{
		{ID: uuid.Must(uuid.NewV7())},
	}, nil)

	got, err := d.svc.ListAppeals(context.Background(), tenantID, nil, 10)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.appealR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestAddEvidence
// ---------------------------------------------------------------------------

func TestAddEvidence(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		setupMocks   func(d testStudentPortalDeps)
		wantErr      error
		assertResult func(t *testing.T, got *StudentAppeal)
	}{
		{
			name: "success: appends evidence URLs",
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(&StudentAppeal{ID: id, EvidenceURLs: []string{"a"}}, nil)
				d.appealR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.StudentAppeal")).Return(nil)
			},
			assertResult: func(t *testing.T, got *StudentAppeal) {
				assert.Equal(t, []string{"a", "b", "c"}, got.EvidenceURLs)
			},
		},
		{
			name: "fails: not found",
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrAppealNotFound,
		},
		{
			name: "fails: update error",
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(&StudentAppeal{ID: id}, nil)
				d.appealR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.StudentAppeal")).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			got, err := d.svc.AddEvidence(context.Background(), id, tenantID, []string{"b", "c"})

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
			d.appealR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestReviewAppeal
// ---------------------------------------------------------------------------

func TestReviewAppeal(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())
	reviewerGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		decision     AppealStatus
		setupMocks   func(d testStudentPortalDeps)
		wantErr      error
		assertResult func(t *testing.T, got *StudentAppeal)
	}{
		{
			name:     "success: upheld decision publishes event",
			decision: AppealStatusUpheld,
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(&StudentAppeal{ID: id, Status: AppealStatusSubmitted}, nil)
				d.appealR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.StudentAppeal")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *StudentAppeal) {
				assert.Equal(t, AppealStatusUpheld, got.Status)
				assert.NotNil(t, got.ReviewerID)
				assert.NotNil(t, got.ReviewedAt)
			},
		},
		{
			name:     "success: dismissed decision",
			decision: AppealStatusDismissed,
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(&StudentAppeal{ID: id, Status: AppealStatusUnderReview}, nil)
				d.appealR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.StudentAppeal")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *StudentAppeal) {
				assert.Equal(t, AppealStatusDismissed, got.Status)
			},
		},
		{
			name:     "fails: not reviewable",
			decision: AppealStatusUpheld,
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(&StudentAppeal{ID: id, Status: AppealStatusUpheld}, nil)
			},
			wantErr: ErrAppealNotReviewable,
		},
		{
			name:     "fails: not found",
			decision: AppealStatusUpheld,
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(nil, nil)
			},
			wantErr: ErrAppealNotFound,
		},
		{
			name:     "fails: update error",
			decision: AppealStatusUpheld,
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(&StudentAppeal{ID: id, Status: AppealStatusSubmitted}, nil)
				d.appealR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.StudentAppeal")).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
		{
			name:     "fails: publish error",
			decision: AppealStatusUpheld,
			setupMocks: func(d testStudentPortalDeps) {
				d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(&StudentAppeal{ID: id, Status: AppealStatusSubmitted}, nil)
				d.appealR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.StudentAppeal")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicTrainingEvents, mock.Anything).Return(errTestRepo)
			},
			wantErr: errTestRepo,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			notes := ptrString("resolved")
			got, err := d.svc.ReviewAppeal(context.Background(), id, tenantID, tc.decision, notes, reviewerGCID)

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
			d.appealR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListApplicationsByStatus
// ---------------------------------------------------------------------------

func TestListApplicationsByStatus(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())

	d := newTestStudentPortalService()
	d.appR.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusWaitlisted).Return([]TrainingApplication{
		{ID: uuid.Must(uuid.NewV7()), Status: ApplicationStatusWaitlisted},
	}, nil)

	got, err := d.svc.ListApplicationsByStatus(context.Background(), tenantID, ApplicationStatusWaitlisted)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.appR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestBatchDecideApplications
// ---------------------------------------------------------------------------

func TestBatchDecideApplications(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	okID := uuid.Must(uuid.NewV7())
	missingID := uuid.Must(uuid.NewV7())
	nonReviewableID := uuid.Must(uuid.NewV7())
	failUpdateID := uuid.Must(uuid.NewV7())

	d := newTestStudentPortalService()
	d.appR.On("GetByID", mock.Anything, okID, tenantID).Return(&TrainingApplication{ID: okID, Status: ApplicationStatusSubmitted}, nil)
	d.appR.On("GetByID", mock.Anything, missingID, tenantID).Return(nil, nil)
	d.appR.On("GetByID", mock.Anything, nonReviewableID, tenantID).Return(&TrainingApplication{ID: nonReviewableID, Status: ApplicationStatusApproved}, nil)
	d.appR.On("GetByID", mock.Anything, failUpdateID, tenantID).Return(&TrainingApplication{ID: failUpdateID, Status: ApplicationStatusUnderReview}, nil)
	d.appR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil).Once()
	d.appR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(errTestRepo).Once()

	processed, failed, errs := d.svc.BatchDecideApplications(context.Background(), tenantID,
		[]uuid.UUID{okID, missingID, nonReviewableID, failUpdateID}, true, nil)

	assert.Equal(t, 1, processed)
	assert.Equal(t, 3, failed)
	assert.Len(t, errs, 3)
	d.appR.AssertExpectations(t)
}

func TestBatchDecideApplications_Reject(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	rejectID := uuid.Must(uuid.NewV7())

	d := newTestStudentPortalService()
	d.appR.On("GetByID", mock.Anything, rejectID, tenantID).Return(&TrainingApplication{ID: rejectID, Status: ApplicationStatusSubmitted}, nil)
	d.appR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil)

	reason := ptrString("quota reached")
	processed, failed, errs := d.svc.BatchDecideApplications(context.Background(), tenantID, []uuid.UUID{rejectID}, false, reason)

	assert.Equal(t, 1, processed)
	assert.Equal(t, 0, failed)
	assert.Empty(t, errs)
	assert.Equal(t, reason, d.appR.Calls[1].Arguments.Get(1).(*TrainingApplication).RejectionReason)
	d.appR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TestPromoteWaitlist
// ---------------------------------------------------------------------------

func TestPromoteWaitlist(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	sessionA := uuid.Must(uuid.NewV7())
	sessionB := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		count      int
		setupMocks func(d testStudentPortalDeps)
		wantErr    error
		wantCount  int
	}{
		{
			name:  "success: promotes matching session applications up to count",
			count: 2,
			setupMocks: func(d testStudentPortalDeps) {
				d.appR.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusWaitlisted).Return([]TrainingApplication{
					{ID: uuid.Must(uuid.NewV7()), TrainingSessionID: sessionA, Status: ApplicationStatusWaitlisted},
					{ID: uuid.Must(uuid.NewV7()), TrainingSessionID: sessionA, Status: ApplicationStatusWaitlisted},
					{ID: uuid.Must(uuid.NewV7()), TrainingSessionID: sessionA, Status: ApplicationStatusWaitlisted},
					{ID: uuid.Must(uuid.NewV7()), TrainingSessionID: sessionB, Status: ApplicationStatusWaitlisted},
				}, nil)
				d.appR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil).Twice()
			},
			wantCount: 2,
		},
		{
			name:  "success: count larger than matches promotes all matching",
			count: 10,
			setupMocks: func(d testStudentPortalDeps) {
				d.appR.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusWaitlisted).Return([]TrainingApplication{
					{ID: uuid.Must(uuid.NewV7()), TrainingSessionID: sessionA, Status: ApplicationStatusWaitlisted},
				}, nil)
				d.appR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(nil)
			},
			wantCount: 1,
		},
		{
			name:  "fails: list error",
			count: 1,
			setupMocks: func(d testStudentPortalDeps) {
				d.appR.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusWaitlisted).Return(nil, errTestRepo)
			},
			wantErr: errTestRepo,
		},
		{
			name:  "fails: update error returns promoted so far",
			count: 5,
			setupMocks: func(d testStudentPortalDeps) {
				d.appR.On("ListByStatus", mock.Anything, tenantID, ApplicationStatusWaitlisted).Return([]TrainingApplication{
					{ID: uuid.Must(uuid.NewV7()), TrainingSessionID: sessionA},
					{ID: uuid.Must(uuid.NewV7()), TrainingSessionID: sessionA},
				}, nil)
				d.appR.On("Update", mock.Anything, mock.AnythingOfType("*training_admin.TrainingApplication")).Return(errTestRepo).Once()
			},
			wantErr:   errTestRepo,
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := newTestStudentPortalService()
			tc.setupMocks(d)

			count, err := d.svc.PromoteWaitlist(context.Background(), tenantID, sessionA, tc.count)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Equal(t, tc.wantCount, count)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.wantCount, count)
			}
			d.appR.AssertExpectations(t)
		})
	}
} // ---------------------------------------------------------------------------
// Remaining branch top-ups
// ---------------------------------------------------------------------------

func TestIssueCertificate_GetRepoError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	d := newTestStudentPortalService()
	d.certReqR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestRepo)

	_, err := d.svc.IssueCertificate(context.Background(), id, tenantID)
	assert.ErrorIs(t, err, errTestRepo)
	d.certReqR.AssertExpectations(t)
}

func TestAddEvidence_GetRepoError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	d := newTestStudentPortalService()
	d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestRepo)

	_, err := d.svc.AddEvidence(context.Background(), id, tenantID, []string{"x"})
	assert.ErrorIs(t, err, errTestRepo)
	d.appealR.AssertExpectations(t)
}

func TestReviewAppeal_GetRepoError(t *testing.T) {
	tenantID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())

	d := newTestStudentPortalService()
	d.appealR.On("GetByID", mock.Anything, id, tenantID).Return(nil, errTestRepo)

	_, err := d.svc.ReviewAppeal(context.Background(), id, tenantID, AppealStatusUpheld, nil, uuid.Must(uuid.NewV7()))
	assert.ErrorIs(t, err, errTestRepo)
	d.appealR.AssertExpectations(t)
}
