// fabric_canonical_test — golden guardrail for the delivery hand-rolled
// encoders that the 2026-07-01 event-fabric audit found deadlettering with
// INVALID_BINARY_PROTO_MESSAGE. Each case marshals via MarshalPayload and
// decodes the bytes into the generated canonical struct: a field-number or
// wire-type drift makes proto.Unmarshal fail (proto3 string UTF-8 validation)
// or lands a value in the wrong field. This is the durable regression gate —
// every encoded event MUST round-trip through its registered schema shape.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protomarshal"
)

func TestSubmissionReleased_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	releasedAt := time.Date(2026, 6, 30, 9, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"submission_id": "sub-r1",
		"assessment_id": "ass-r1",
		"learner_gcid":  "gcid-r1",
		"released_at":   releasedAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.submission.released.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m deliveryv1.SubmissionReleased
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen SubmissionReleased: %v", err)
	}
	if m.GetSubmissionId() != "sub-r1" {
		t.Errorf("submission_id (2) = %q", m.GetSubmissionId())
	}
	if m.GetAssessmentId() != "ass-r1" {
		t.Errorf("assessment_id (3) = %q", m.GetAssessmentId())
	}
	if m.GetLearnerGcid() != "gcid-r1" {
		t.Errorf("learner_gcid (4) = %q", m.GetLearnerGcid())
	}
	if m.GetGradingJobId() != "" {
		t.Errorf("grading_job_id (5) = %q, want empty (producer sets none)", m.GetGradingJobId())
	}
	if m.GetReleasedAt() == nil || !m.GetReleasedAt().AsTime().Equal(releasedAt) {
		t.Errorf("released_at (9) = %v want %v", m.GetReleasedAt(), releasedAt)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() == "" {
		t.Errorf("envelope (1) missing/empty")
	}
}

func TestAssessmentOpened_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	opensAt := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	closesAt := opensAt.Add(2 * time.Hour)
	openedAt := opensAt.Add(time.Minute)
	payload := map[string]any{
		"assessment_id":   "ass-o1",
		"test_set_id":     "ts-o1",
		"instructor_gcid": "gcid-inst",
		"class_id":        "class-o1",
		"state":           int32(3),
		"open_trigger":    "manual_open",
		"opens_at":        opensAt.Format(time.RFC3339Nano),
		"closes_at":       closesAt.Format(time.RFC3339Nano),
		"total_invited":   int32(25),
		"opened_at":       openedAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.assessment.opened.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m deliveryv1.AssessmentOpened
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen AssessmentOpened: %v", err)
	}
	if m.GetAssessmentId() != "ass-o1" {
		t.Errorf("assessment_id (2) = %q", m.GetAssessmentId())
	}
	if m.GetClassId() != "class-o1" {
		t.Errorf("class_id (5) = %q", m.GetClassId())
	}
	if m.GetOpenTrigger() != "manual_open" {
		t.Errorf("open_trigger (7) = %q", m.GetOpenTrigger())
	}
	if m.GetTotalInvited() != 25 {
		t.Errorf("total_invited (10) = %d", m.GetTotalInvited())
	}
	if m.GetOpenedAt() == nil || !m.GetOpenedAt().AsTime().Equal(openedAt) {
		t.Errorf("opened_at (11) = %v want %v", m.GetOpenedAt(), openedAt)
	}
}

func TestAssessmentPublished_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	openAt := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	closeAt := openAt.Add(2 * time.Hour)
	publishedAt := openAt.Add(-24 * time.Hour)
	payload := map[string]any{
		"assessment_id":      "ass-p1",
		"test_set_id":        "ts-p1",
		"instructor_gcid":    "gcid-inst",
		"class_id":           "class-p1",
		"state":              int32(1),
		"title":              "Road Safety Final",
		"scheduled_open_at":  openAt.Format(time.RFC3339Nano),
		"scheduled_close_at": closeAt.Format(time.RFC3339Nano),
		"max_attempts":       int32(3),
		"published_at":       publishedAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.assessment.published.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m deliveryv1.AssessmentPublished
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen AssessmentPublished: %v", err)
	}
	if m.GetClassId() != "class-p1" {
		t.Errorf("class_id (5) = %q", m.GetClassId())
	}
	if m.GetTitle() != "Road Safety Final" {
		t.Errorf("title (7) = %q", m.GetTitle())
	}
	if m.GetMaxAttempts() != 3 {
		t.Errorf("max_attempts (10) = %d", m.GetMaxAttempts())
	}
	if m.GetPublishedAt() == nil || !m.GetPublishedAt().AsTime().Equal(publishedAt) {
		t.Errorf("published_at (11) = %v want %v", m.GetPublishedAt(), publishedAt)
	}
}

func TestAssessmentArchived_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	archivedAt := time.Date(2026, 6, 30, 11, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"assessment_id":   "ass-a1",
		"test_set_id":     "ts-a1",
		"instructor_gcid": "gcid-inst",
		"class_id":        "class-a1",
		"state":           int32(6),
		"archive_trigger": "instructor_archive",
		"archive_note":    "term ended",
		"archived_at":     archivedAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.assessment.archived.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m deliveryv1.AssessmentArchived
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen AssessmentArchived: %v", err)
	}
	if m.GetArchiveTrigger() != "instructor_archive" {
		t.Errorf("archive_trigger (7) = %q", m.GetArchiveTrigger())
	}
	if m.GetArchiveNote() != "term ended" {
		t.Errorf("archive_note (8) = %q", m.GetArchiveNote())
	}
	if m.GetArchivedAt() == nil || !m.GetArchivedAt().AsTime().Equal(archivedAt) {
		t.Errorf("archived_at (9) = %v want %v", m.GetArchivedAt(), archivedAt)
	}
}

// TestCoursePublished_CanonicalRoundTrip — Class D fabric-repair guard for
// chora.delivery.course.published.v1 (the producer emitted to an unprovisioned
// topic; this asserts the new binary encoder round-trips through the registered
// schema shape, incl. the repeated imda_dimensions list on the message).
func TestCoursePublished_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	publishedAt := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"course_id":             "course-p1",
		"title":                 "Intro to Road Safety",
		"instructor_gcid":       "gcid-instructor",
		"published_at":          publishedAt.Format(time.RFC3339Nano),
		"chora_imda_dimensions": []string{"accountability", "transparency"},
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.course.published.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m deliveryv1.CoursePublished
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CoursePublished: %v", err)
	}
	if m.GetCourseId() != "course-p1" {
		t.Errorf("course_id (2) = %q", m.GetCourseId())
	}
	if m.GetTitle() != "Intro to Road Safety" {
		t.Errorf("title (3) = %q", m.GetTitle())
	}
	if m.GetInstructorGcid() != "gcid-instructor" {
		t.Errorf("instructor_gcid (4) = %q", m.GetInstructorGcid())
	}
	if m.GetPublishedAt() == nil || !m.GetPublishedAt().AsTime().Equal(publishedAt) {
		t.Errorf("published_at (5) = %v want %v", m.GetPublishedAt(), publishedAt)
	}
	if got := m.GetImdaDimensions(); len(got) != 2 || got[0] != "accountability" || got[1] != "transparency" {
		t.Errorf("imda_dimensions (6) = %v want [accountability transparency]", got)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() == "" {
		t.Errorf("envelope (1) missing/empty")
	}
}

// TestGradingMcqSnapshotMissing_CanonicalRoundTrip — Class D fabric-repair guard
// for chora.delivery.grading.mcq_snapshot_missing.v1. The singular
// chora_imda_dimension rides on the ENVELOPE (field 14), NOT the message —
// asserted explicitly so a future field-drift is caught.
func TestGradingMcqSnapshotMissing_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	detectedAt := time.Date(2026, 6, 30, 13, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"submission_id":        "sub-m1",
		"assessment_id":        "ass-m1",
		"learner_gcid":         "gcid-learner",
		"error_message":        "delivery: mcq snapshot missing for test_set_question_id: tsq-9",
		"detected_at":          detectedAt.Format(time.RFC3339Nano),
		"chora_imda_dimension": "accountability",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.grading.mcq_snapshot_missing.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m deliveryv1.GradingMcqSnapshotMissing
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen GradingMcqSnapshotMissing: %v", err)
	}
	if m.GetSubmissionId() != "sub-m1" {
		t.Errorf("submission_id (2) = %q", m.GetSubmissionId())
	}
	if m.GetAssessmentId() != "ass-m1" {
		t.Errorf("assessment_id (3) = %q", m.GetAssessmentId())
	}
	if m.GetLearnerGcid() != "gcid-learner" {
		t.Errorf("learner_gcid (4) = %q", m.GetLearnerGcid())
	}
	if m.GetErrorMessage() != "delivery: mcq snapshot missing for test_set_question_id: tsq-9" {
		t.Errorf("error_message (5) = %q", m.GetErrorMessage())
	}
	if m.GetDetectedAt() == nil || !m.GetDetectedAt().AsTime().Equal(detectedAt) {
		t.Errorf("detected_at (6) = %v want %v", m.GetDetectedAt(), detectedAt)
	}
	if m.GetEnvelope().GetChoraImdaDimension() != "accountability" {
		t.Errorf("envelope.chora_imda_dimension (14) = %q want accountability", m.GetEnvelope().GetChoraImdaDimension())
	}
}
