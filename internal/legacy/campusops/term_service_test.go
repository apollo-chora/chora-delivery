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

type testTermDeps struct {
	svc       *TermService
	termR     *mockTermRepo
	publisher *mockEventPublisher
}

func newTestTermService() testTermDeps {
	tr := &mockTermRepo{}
	ep := &mockEventPublisher{}
	return testTermDeps{
		svc:       NewTermService(tr, ep),
		termR:     tr,
		publisher: ep,
	}
}

func TestCreateTerm(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	tests := []struct {
		name         string
		input        *AcademicTerm
		setupMocks   func(d testTermDeps)
		wantErr      error
		assertResult func(t *testing.T, got *AcademicTerm)
	}{
		{
			name: "success: creates term with UUIDv7",
			input: &AcademicTerm{
				TenantID: tenantID,
				Name:     "Fall 2026",
				TermType: TermTypeSemester,
				StartsAt: now,
				EndsAt:   now.Add(120 * 24 * time.Hour),
			},
			setupMocks: func(d testTermDeps) {
				d.termR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.AcademicTerm")).Return(nil)
			},
			assertResult: func(t *testing.T, got *AcademicTerm) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, "Fall 2026", got.Name)
				assert.Equal(t, TermTypeSemester, got.TermType)
				assert.True(t, got.IsActive)
			},
		},
		{
			name: "fails: empty name",
			input: &AcademicTerm{
				TenantID: tenantID,
				Name:     "",
				TermType: TermTypeSemester,
				StartsAt: now,
				EndsAt:   now.Add(120 * 24 * time.Hour),
			},
			setupMocks: func(d testTermDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: invalid term type",
			input: &AcademicTerm{
				TenantID: tenantID,
				Name:     "Invalid Term",
				TermType: TermType("biannual"),
				StartsAt: now,
				EndsAt:   now.Add(120 * 24 * time.Hour),
			},
			setupMocks: func(d testTermDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: ends_at before starts_at",
			input: &AcademicTerm{
				TenantID: tenantID,
				Name:     "Bad Dates",
				TermType: TermTypeQuarter,
				StartsAt: now.Add(120 * 24 * time.Hour),
				EndsAt:   now,
			},
			setupMocks: func(d testTermDeps) {},
			wantErr:    ErrTermDateRange,
		},
		{
			name: "fails: repo error propagated",
			input: &AcademicTerm{
				TenantID: tenantID,
				Name:     "Good Term",
				TermType: TermTypeTrimester,
				StartsAt: now,
				EndsAt:   now.Add(90 * 24 * time.Hour),
			},
			setupMocks: func(d testTermDeps) {
				d.termR.On("Create", mock.Anything, mock.AnythingOfType("*campusops.AcademicTerm")).
					Return(errors.New("db connection lost"))
			},
			wantErr: errors.New("db connection lost"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestTermService()
			tc.setupMocks(d)

			got, err := d.svc.CreateTerm(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrValidationFailed) {
					assert.ErrorIs(t, err, ErrValidationFailed)
				}
				if errors.Is(tc.wantErr, ErrTermDateRange) {
					assert.ErrorIs(t, err, ErrTermDateRange)
				}
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.termR.AssertExpectations(t)
		})
	}
}

func TestGetTerm(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		tenantID   uuid.UUID
		setupMocks func(d testTermDeps)
		wantErr    error
	}{
		{
			name:     "success: returns term",
			id:       termID,
			tenantID: tenantID,
			setupMocks: func(d testTermDeps) {
				d.termR.On("GetByID", mock.Anything, termID, tenantID).Return(&AcademicTerm{
					ID: termID, TenantID: tenantID, Name: "Fall 2026",
				}, nil)
			},
		},
		{
			name:     "fails: not found",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupMocks: func(d testTermDeps) {
				d.termR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrTermNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestTermService()
			tc.setupMocks(d)

			got, err := d.svc.GetTerm(context.Background(), tc.id, tc.tenantID)

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

func TestUpdateTerm(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	termID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	tests := []struct {
		name       string
		input      *AcademicTerm
		setupMocks func(d testTermDeps)
		wantErr    error
	}{
		{
			name: "success: updates mutable fields",
			input: &AcademicTerm{
				ID:       termID,
				TenantID: tenantID,
				Name:     "Updated Term",
				TermType: TermTypeQuarter,
				StartsAt: now,
				EndsAt:   now.Add(90 * 24 * time.Hour),
				IsActive: true,
			},
			setupMocks: func(d testTermDeps) {
				d.termR.On("GetByID", mock.Anything, termID, tenantID).Return(&AcademicTerm{
					ID: termID, TenantID: tenantID, Name: "Old Name",
					TermType: TermTypeSemester, StartsAt: now, EndsAt: now.Add(120 * 24 * time.Hour),
				}, nil)
				d.termR.On("Update", mock.Anything, mock.AnythingOfType("*campusops.AcademicTerm")).Return(nil)
			},
		},
		{
			name: "fails: term not found",
			input: &AcademicTerm{
				ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Name: "X",
				TermType: TermTypeSemester, StartsAt: now, EndsAt: now.Add(24 * time.Hour),
			},
			setupMocks: func(d testTermDeps) {
				d.termR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrTermNotFound,
		},
		{
			name: "fails: empty name on update",
			input: &AcademicTerm{
				ID: termID, TenantID: tenantID, Name: "",
				TermType: TermTypeSemester, StartsAt: now, EndsAt: now.Add(24 * time.Hour),
			},
			setupMocks: func(d testTermDeps) {
				d.termR.On("GetByID", mock.Anything, termID, tenantID).Return(&AcademicTerm{
					ID: termID, TenantID: tenantID, Name: "Old",
				}, nil)
			},
			wantErr: ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestTermService()
			tc.setupMocks(d)

			got, err := d.svc.UpdateTerm(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}

			d.termR.AssertExpectations(t)
		})
	}
}
