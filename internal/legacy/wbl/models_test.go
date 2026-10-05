package wbl

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Enum IsValid tests — covers InternshipStatus, ApplicationStatus,
// PlacementStatus, CapstoneStatus, PartnerStatus validators.
// ---------------------------------------------------------------------------

func TestInternshipStatusIsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status InternshipStatus
		want   bool
	}{
		{InternshipStatusDraft, true},
		{InternshipStatusOpen, true},
		{InternshipStatusClosed, true},
		{InternshipStatusArchived, true},
		{InternshipStatus("unknown"), false},
		{InternshipStatus(""), false},
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.status.IsValid())
		})
	}
}

func TestApplicationStatusIsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status ApplicationStatus
		want   bool
	}{
		{ApplicationStatusSubmitted, true},
		{ApplicationStatusUnderReview, true},
		{ApplicationStatusApproved, true},
		{ApplicationStatusRejected, true},
		{ApplicationStatus("pending"), false},
		{ApplicationStatus(""), false},
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.status.IsValid())
		})
	}
}

func TestPlacementStatusIsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status PlacementStatus
		want   bool
	}{
		{PlacementStatusActive, true},
		{PlacementStatusCompleted, true},
		{PlacementStatusWithdrawn, true},
		{PlacementStatus("cancelled"), false},
		{PlacementStatus(""), false},
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.status.IsValid())
		})
	}
}

func TestCapstoneStatusIsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status CapstoneStatus
		want   bool
	}{
		{CapstoneStatusDraft, true},
		{CapstoneStatusActive, true},
		{CapstoneStatusCompleted, true},
		{CapstoneStatusArchived, true},
		{CapstoneStatus("suspended"), false},
		{CapstoneStatus(""), false},
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.status.IsValid())
		})
	}
}

func TestPartnerStatusIsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status PartnerStatus
		want   bool
	}{
		{PartnerStatusActive, true},
		{PartnerStatusInactive, true},
		{PartnerStatus("suspended"), false},
		{PartnerStatus(""), false},
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.status.IsValid())
		})
	}
}
