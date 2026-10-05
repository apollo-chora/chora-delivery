// submission_graded_payload_edges_test.go — the builder branches the
// canonical tests do not reach: nil submission, unset Passed, and an
// ungraded submission (GradedAt nil -> UpdatedAt fallback).
package events_test

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func TestSubmissionGradedPayload_NilSubmissionReturnsNil(t *testing.T) {
	if got := events.SubmissionGradedPayload(nil, "title", "graduate", "tp-1"); got != nil {
		t.Fatalf("nil submission must produce nil payload, got %v", got)
	}
}

func TestSubmissionGradedPayload_UnsetPassedOmitsKey(t *testing.T) {
	sub := &domain.Submission{
		ID:             "sub-1",
		AssessmentID:   "ass-1",
		TenantID:       "tenant-1",
		LearnerGCID:    "gcid-1",
		TotalScore:     4,
		MaxScore:       10,
		PassingPercent: 70,
		// Passed left nil -> the key must be ABSENT (proto3 default omission).
	}
	payload := events.SubmissionGradedPayload(sub, "Algebra Midterm", "graduate", "tp-1")
	if _, present := payload["passed"]; present {
		t.Fatalf("passed must be absent when sub.Passed is nil, got %v", payload["passed"])
	}
}

func TestSubmissionGradedPayload_GradedAtDefaultsToUpdatedAt(t *testing.T) {
	updated := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	sub := &domain.Submission{
		ID:             "sub-1",
		AssessmentID:   "ass-1",
		TenantID:       "tenant-1",
		LearnerGCID:    "gcid-1",
		TotalScore:     4,
		MaxScore:       10,
		PassingPercent: 70,
		UpdatedAt:      updated,
		// GradedAt nil -> the builder falls back to UpdatedAt.
	}
	payload := events.SubmissionGradedPayload(sub, "Algebra Midterm", "graduate", "tp-1")
	want := updated.Format(time.RFC3339Nano)
	if got := payload["graded_at"]; got != want {
		t.Fatalf("graded_at = %v, want %q (UpdatedAt fallback)", got, want)
	}
}
