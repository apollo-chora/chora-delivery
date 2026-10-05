// branch_coverage_test.go — statement-coverage completion for the
// protomarshal encoders, driven purely through the EXPORTED API
// (protomarshal.MarshalPayload / MarshalAssessmentPayload /
// IsUnsupportedTopic). Each case exercises the payload-shape branches the
// canonical/round-trip tests leave unrun:
//
//   - full payloads for every assessment lifecycle topic (non-zero numeric
//     slots, present timestamps, repeated batch_submissions) so the append
//     statements execute;
//   - nil payloads so the envelope-only early-return executes;
//   - wrong-typed Timestamp / string slots so the fail-loud error returns run;
//   - the loose-typed coercion helpers (asInt32 / asInt64 / asFloat64 /
//     asTime / asStringSlice / readString / examResultOutcomeEnum) driven
//     through encoder keys, one value type per branch.
//
// No production code is touched; these tests only add coverage.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protomarshal"
)

// -----------------------------------------------------------------------------
// Dispatcher edges
// -----------------------------------------------------------------------------

// TestBranch_MarshalAssessmentPayload_UnknownTopic drives the DEFAULT branch of
// the inner dispatcher (the outer MarshalPayload default is already covered by
// TestMarshal_UnknownTopic_FailsLoud). The wrapped error must still be
// recognized by IsUnsupportedTopic.
func TestBranch_MarshalAssessmentPayload_UnknownTopic(t *testing.T) {
	_, err := protomarshal.MarshalAssessmentPayload("chora.delivery.some.unwired.v1", fixedEnvelope(), nil)
	if err == nil {
		t.Fatal("expected ErrUnsupportedTopic; got nil")
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		t.Fatalf("expected IsUnsupportedTopic(true); got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Assessment lifecycle — full payloads
// -----------------------------------------------------------------------------

// TestBranch_AssessmentLifecycle_FullPayloads drives every assessment /
// submission / grading lifecycle encoder with ALL typed slots populated
// (non-zero ints, present timestamps, all string keys, true bools, repeated
// batch_submissions). The generated wire bytes must parse under
// walkWireTagsAllTypes (proves no malformed TLV is emitted).
func TestBranch_AssessmentLifecycle_FullPayloads(t *testing.T) {
	env := fixedEnvelope()
	ts := env.OccurredAt.Format(time.RFC3339Nano)

	cases := []struct {
		topic   string
		payload map[string]any
	}{
		{
			"chora.delivery.assessment.created.v1",
			map[string]any{
				"assessment_id":              "ass-1",
				"test_set_id":                "ts-1",
				"test_set_revision_snapshot": int32(2),
				"instructor_gcid":            "gcid-inst",
				"class_id":                   "cls-1",
				"state":                      int32(1),
				"title":                      "Midterm",
				"scheduled_open_at":          ts,
				"scheduled_close_at":         ts,
				"max_attempts":               int32(3),
				"question_count":             int32(10),
				"total_points":               int32(100),
				"created_at":                 ts,
			},
		},
		{
			"chora.delivery.assessment.published.v1",
			map[string]any{
				"assessment_id":      "ass-1",
				"test_set_id":        "ts-1",
				"instructor_gcid":    "gcid-inst",
				"class_id":           "cls-1",
				"state":              int32(1),
				"title":              "Midterm",
				"scheduled_open_at":  ts,
				"scheduled_close_at": ts,
				"max_attempts":       int32(2),
				"published_at":       ts,
			},
		},
		{
			"chora.delivery.assessment.opened.v1",
			map[string]any{
				"assessment_id":   "ass-1",
				"test_set_id":     "ts-1",
				"instructor_gcid": "gcid-inst",
				"class_id":        "cls-1",
				"state":           int32(3),
				"open_trigger":    "scheduled_auto_open",
				"opens_at":        ts,
				"closes_at":       ts,
				"total_invited":   int32(25),
				"opened_at":       ts,
			},
		},
		{
			"chora.delivery.assessment.closed.v1",
			map[string]any{
				"assessment_id":   "ass-1",
				"test_set_id":     "ts-1",
				"instructor_gcid": "gcid-inst",
				"class_id":        "cls-1",
				"state":           int32(4),
				"close_reason":    "window elapsed",
				"total_invited":   int32(25),
				"total_started":   int32(20),
				"total_submitted": int32(18),
				"closed_at":       ts,
			},
		},
		{
			"chora.delivery.assessment.grading_started.v1",
			map[string]any{
				"assessment_id":                "ass-1",
				"test_set_id":                  "ts-1",
				"instructor_gcid":              "gcid-inst",
				"class_id":                     "cls-1",
				"state":                        int32(5),
				"submissions_dispatched":       int32(18),
				"oe_question_submission_count": int32(3),
				"grading_started_at":           ts,
			},
		},
		{
			"chora.delivery.assessment.graded.v1",
			map[string]any{
				"assessment_id":            "ass-1",
				"test_set_id":              "ts-1",
				"instructor_gcid":          "gcid-inst",
				"class_id":                 "cls-1",
				"state":                    int32(6),
				"total_invited":            int32(25),
				"total_submitted":          int32(18),
				"total_graded":             int32(17),
				"passed_count":             int32(12),
				"failed_count":             int32(5),
				"total_grading_latency_ms": int64(45_000),
				"graded_at":                ts,
			},
		},
		{
			"chora.delivery.assessment.released.v1",
			map[string]any{
				"assessment_id":        "ass-1",
				"test_set_id":          "ts-1",
				"instructor_gcid":      "gcid-inst",
				"class_id":             "cls-1",
				"state":                int32(7),
				"release_method":       "auto",
				"total_invited":        int32(25),
				"total_submitted":      int32(18),
				"total_graded":         int32(17),
				"release_announcement": "results out",
				"released_at":          ts,
			},
		},
		{
			"chora.delivery.assessment.archived.v1",
			map[string]any{
				"assessment_id":   "ass-1",
				"test_set_id":     "ts-1",
				"instructor_gcid": "gcid-inst",
				"class_id":        "cls-1",
				"state":           int32(8),
				"archive_trigger": "retention_sweep",
				"archive_note":    "term ended",
				"archived_at":     ts,
			},
		},
		{
			"chora.delivery.submission.started.v1",
			map[string]any{
				"submission_id":      "sub-1",
				"assessment_id":      "ass-1",
				"test_set_id":        "ts-1",
				"learner_gcid":       "gcid-learner",
				"class_id":           "cls-1",
				"state":              int32(2),
				"attempt_number":     int32(1),
				"opens_at":           ts,
				"closes_at":          ts,
				"time_limit_seconds": int32(3600),
				"started_at":         ts,
			},
		},
		{
			"chora.delivery.submission.submitted.v1",
			map[string]any{
				"submission_id":   "sub-1",
				"assessment_id":   "ass-1",
				"test_set_id":     "ts-1",
				"learner_gcid":    "gcid-learner",
				"state":           int32(3),
				"attempt_number":  int32(1),
				"close_reason":    int32(1),
				"total_questions": int32(10),
				"answered_count":  int32(9),
				"submitted_at":    ts,
			},
		},
		{
			"chora.delivery.submission.graded.v1",
			map[string]any{
				"submission_id":             "sub-1",
				"assessment_id":             "ass-1",
				"learner_gcid":              "gcid-learner",
				"grading_job_id":            "job-1",
				"state":                     int32(4),
				"total_points_earned":       float64(8.5),
				"total_points_possible":     int32(10),
				"passing_threshold_percent": int32(70),
				"passed":                    true,
				"graded_at":                 ts,
			},
		},
		{
			"chora.delivery.submission.released.v1",
			map[string]any{
				"submission_id":        "sub-1",
				"assessment_id":        "ass-1",
				"learner_gcid":         "gcid-learner",
				"grading_job_id":       "job-1",
				"state":                int32(5),
				"release_method":       "auto",
				"release_announcement": "released",
				"released_at":          ts,
			},
		},
		{
			"chora.delivery.grading.oe_batch_requested.v1",
			map[string]any{
				"oe_batch_id":                    "batch-1",
				"assessment_id":                  "ass-1",
				"test_set_question_id":           "tsq-1",
				"question_id":                    "q-1",
				"rubric_json":                    "{}",
				"model_answer":                   "answer",
				"prompt":                         "prompt",
				"points_possible_per_submission": int32(10),
				"model_tier":                     "gpt-4o-mini",
				"per_question_feedback_enabled":  true,
				"estimated_mana_units":           int32(5),
				"dispatched_at":                  ts,
				"batch_submissions": []map[string]any{
					{
						"submission_id":        "sub-1",
						"test_set_question_id": "tsq-1",
						"question_id":          "q-1",
						"learner_gcid":         "gcid-learner",
						"response_text":        "response",
						"accommodations_json":  "{}",
					},
					{
						"submission_id": "sub-2",
					},
				},
			},
		},
		{
			"chora.delivery.grading.mcq_completed.v1",
			map[string]any{
				"grading_job_id":      "job-1",
				"submission_id":       "sub-1",
				"assessment_id":       "ass-1",
				"learner_gcid":        "gcid-learner",
				"mcq_correct_count":   int32(8),
				"mcq_incorrect_count": int32(2),
				"mcq_points_earned":   float64(8.0),
				"mcq_points_possible": int32(10),
				"no_oe_pending":       true,
				"mcq_completed_at":    ts,
			},
		},
	}

	for _, tc := range cases {
		bz, err := protomarshal.MarshalPayload(tc.topic, env, tc.payload)
		if err != nil {
			t.Errorf("%s: MarshalPayload: %v", tc.topic, err)
			continue
		}
		if len(bz) == 0 {
			t.Errorf("%s: produced zero bytes", tc.topic)
			continue
		}
		walkWireTagsAllTypes(t, bz)
		requireEnvelopeFields(t, envelopeBytes(t, bz))
	}
}

// TestBranch_AssessmentLifecycle_NilPayloads exercises the payload == nil
// early-return of every lifecycle encoder (envelope-only message).
func TestBranch_AssessmentLifecycle_NilPayloads(t *testing.T) {
	env := fixedEnvelope()
	topics := []string{
		"chora.delivery.assessment.created.v1",
		"chora.delivery.assessment.published.v1",
		"chora.delivery.assessment.opened.v1",
		"chora.delivery.assessment.closed.v1",
		"chora.delivery.assessment.grading_started.v1",
		"chora.delivery.assessment.graded.v1",
		"chora.delivery.assessment.released.v1",
		"chora.delivery.assessment.archived.v1",
		"chora.delivery.submission.started.v1",
		"chora.delivery.submission.submitted.v1",
		"chora.delivery.submission.graded.v1",
		"chora.delivery.submission.released.v1",
		"chora.delivery.grading.oe_batch_requested.v1",
		"chora.delivery.grading.failed.v1",
		"chora.delivery.grading.mcq_completed.v1",
		"chora.delivery.grading.mcq_snapshot_missing.v1",
	}
	for _, topic := range topics {
		bz, err := protomarshal.MarshalPayload(topic, env, nil)
		if err != nil {
			t.Errorf("%s: nil payload: %v", topic, err)
			continue
		}
		num, _, n := protowire.ConsumeTag(bz)
		if n < 0 || num != 1 {
			t.Errorf("%s: nil payload leading tag = %d (want envelope=1)", topic, num)
		}
	}
}

// TestBranch_AssessmentLifecycle_BadTimestamps — a wrong-typed value on every
// Timestamp slot must fail loud (exercises the embedTimestampInto error
// returns inside each encoder).
func TestBranch_AssessmentLifecycle_BadTimestamps(t *testing.T) {
	env := fixedEnvelope()
	base := map[string]string{
		"chora.delivery.assessment.created.v1":           "assessment_id",
		"chora.delivery.assessment.published.v1":         "assessment_id",
		"chora.delivery.assessment.opened.v1":            "assessment_id",
		"chora.delivery.assessment.closed.v1":            "assessment_id",
		"chora.delivery.assessment.grading_started.v1":   "assessment_id",
		"chora.delivery.assessment.graded.v1":            "assessment_id",
		"chora.delivery.assessment.released.v1":          "assessment_id",
		"chora.delivery.assessment.archived.v1":          "assessment_id",
		"chora.delivery.submission.started.v1":           "submission_id",
		"chora.delivery.submission.submitted.v1":         "submission_id",
		"chora.delivery.submission.graded.v1":            "submission_id",
		"chora.delivery.submission.released.v1":          "submission_id",
		"chora.delivery.grading.oe_batch_requested.v1":   "oe_batch_id",
		"chora.delivery.grading.failed.v1":               "submission_id",
		"chora.delivery.grading.mcq_completed.v1":        "submission_id",
		"chora.delivery.grading.mcq_snapshot_missing.v1": "submission_id",
	}
	keys := map[string][]string{
		"chora.delivery.assessment.created.v1":           {"scheduled_open_at", "scheduled_close_at", "created_at"},
		"chora.delivery.assessment.published.v1":         {"scheduled_open_at", "scheduled_close_at", "published_at"},
		"chora.delivery.assessment.opened.v1":            {"opens_at", "closes_at", "opened_at"},
		"chora.delivery.assessment.closed.v1":            {"closed_at"},
		"chora.delivery.assessment.grading_started.v1":   {"grading_started_at"},
		"chora.delivery.assessment.graded.v1":            {"graded_at"},
		"chora.delivery.assessment.released.v1":          {"released_at"},
		"chora.delivery.assessment.archived.v1":          {"archived_at"},
		"chora.delivery.submission.started.v1":           {"opens_at", "closes_at", "started_at"},
		"chora.delivery.submission.submitted.v1":         {"submitted_at"},
		"chora.delivery.submission.graded.v1":            {"graded_at"},
		"chora.delivery.submission.released.v1":          {"released_at"},
		"chora.delivery.grading.oe_batch_requested.v1":   {"dispatched_at"},
		"chora.delivery.grading.failed.v1":               {"failed_at"},
		"chora.delivery.grading.mcq_completed.v1":        {"mcq_completed_at"},
		"chora.delivery.grading.mcq_snapshot_missing.v1": {"detected_at"},
	}
	for topic, tsKeys := range keys {
		for _, k := range tsKeys {
			payload := map[string]any{base[topic]: "id-1", k: 42}
			if _, err := protomarshal.MarshalPayload(topic, env, payload); err == nil {
				t.Errorf("%s key %q bad timestamp 42: expected error", topic, k)
			}
		}
	}
}

// TestBranch_AssessmentLifecycle_BadStrings — the lifecycle encoders that use
// requireString (grading.failed + the live_quiz topics) must fail loud on a
// wrong-typed string slot.
func TestBranch_AssessmentLifecycle_BadStrings(t *testing.T) {
	env := fixedEnvelope()
	base := map[string]any{
		"submission_id":   "sub-1",
		"assessment_id":   "ass-1",
		"learner_gcid":    "gcid-learner",
		"session_id":      "sess-1",
		"live_quiz_id":    "lq-1",
		"question_id":     "q-1",
		"instructor_gcid": "gcid-inst",
	}
	cases := []struct {
		topic string
		key   string
	}{
		{"chora.delivery.grading.failed.v1", "grading_job_id"},
		{"chora.delivery.grading.failed.v1", "submission_id"},
		{"chora.delivery.grading.failed.v1", "assessment_id"},
		{"chora.delivery.grading.failed.v1", "learner_gcid"},
		{"chora.delivery.grading.failed.v1", "failure_message"},
		{"chora.delivery.grading.failed.v1", "oe_batch_id"},
		{"chora.delivery.live_quiz_session.started.v1", "session_id"},
		{"chora.delivery.live_quiz_session.started.v1", "live_quiz_id"},
		{"chora.delivery.live_quiz_session.started.v1", "instructor_gcid"},
		{"chora.delivery.live_quiz_session.score_awarded.v1", "session_id"},
		{"chora.delivery.live_quiz_session.score_awarded.v1", "live_quiz_id"},
		{"chora.delivery.live_quiz_session.score_awarded.v1", "question_id"},
		{"chora.delivery.live_quiz_session.score_awarded.v1", "atom_id"},
		{"chora.delivery.live_quiz_session.ended.v1", "session_id"},
		{"chora.delivery.live_quiz_session.ended.v1", "live_quiz_id"},
	}
	for _, tc := range cases {
		payload := cloneAny(base)
		payload[tc.key] = 42
		if _, err := protomarshal.MarshalPayload(tc.topic, env, payload); err == nil {
			t.Errorf("%s key %q bad string: expected error", tc.topic, tc.key)
		}
	}
}

func cloneAny(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// -----------------------------------------------------------------------------
// TestSet — nil payloads, bad timestamps, bad strings
// -----------------------------------------------------------------------------

func TestBranch_TestSet_NilPayloads(t *testing.T) {
	env := fixedEnvelope()
	for _, topic := range []string{
		"chora.delivery.test_set.created.v1",
		"chora.delivery.test_set.question_added.v1",
		"chora.delivery.test_set.question_updated.v1",
		"chora.delivery.test_set.question_removed.v1",
		"chora.delivery.test_set.published.v1",
	} {
		bz, err := protomarshal.MarshalPayload(topic, env, nil)
		if err != nil {
			t.Errorf("%s: nil payload: %v", topic, err)
			continue
		}
		num, _, n := protowire.ConsumeTag(bz)
		if n < 0 || num != 1 {
			t.Errorf("%s: nil payload leading tag = %d (want envelope=1)", topic, num)
		}
	}
}

func TestBranch_TestSet_BadTimestamps(t *testing.T) {
	env := fixedEnvelope()
	cases := []struct {
		topic string
		key   string
	}{
		{"chora.delivery.test_set.created.v1", "created_at"},
		{"chora.delivery.test_set.question_added.v1", "added_at"},
		{"chora.delivery.test_set.question_updated.v1", "updated_at"},
		{"chora.delivery.test_set.question_removed.v1", "removed_at"},
		{"chora.delivery.test_set.published.v1", "published_at"},
	}
	for _, tc := range cases {
		payload := map[string]any{"test_set_id": "ts-1", tc.key: 42}
		if _, err := protomarshal.MarshalPayload(tc.topic, env, payload); err == nil {
			t.Errorf("%s key %q bad timestamp: expected error", tc.topic, tc.key)
		}
	}
}

func TestBranch_TestSet_BadStrings(t *testing.T) {
	env := fixedEnvelope()
	cases := []struct {
		topic string
		key   string
	}{
		{"chora.delivery.test_set.created.v1", "test_set_id"},
		{"chora.delivery.test_set.created.v1", "tenant_id"},
		{"chora.delivery.test_set.created.v1", "author_gcid"},
		{"chora.delivery.test_set.created.v1", "title"},
		{"chora.delivery.test_set.question_added.v1", "test_set_id"},
		{"chora.delivery.test_set.question_added.v1", "test_set_question_id"},
		{"chora.delivery.test_set.question_added.v1", "question_atom_id"},
		{"chora.delivery.test_set.question_added.v1", "question_type"},
		{"chora.delivery.test_set.question_updated.v1", "test_set_id"},
		{"chora.delivery.test_set.question_updated.v1", "test_set_question_id"},
		{"chora.delivery.test_set.question_removed.v1", "test_set_id"},
		{"chora.delivery.test_set.question_removed.v1", "test_set_question_id"},
		{"chora.delivery.test_set.published.v1", "test_set_id"},
		{"chora.delivery.test_set.published.v1", "tenant_id"},
		{"chora.delivery.test_set.published.v1", "author_gcid"},
	}
	for _, tc := range cases {
		payload := map[string]any{tc.key: 42}
		if _, err := protomarshal.MarshalPayload(tc.topic, env, payload); err == nil {
			t.Errorf("%s key %q bad string: expected error", tc.topic, tc.key)
		}
	}
}

// -----------------------------------------------------------------------------
// LiveQuiz — nil payloads, bad timestamps
// -----------------------------------------------------------------------------

func TestBranch_LiveQuiz_NilPayloads(t *testing.T) {
	env := fixedEnvelope()
	for _, topic := range []string{
		"chora.delivery.live_quiz_session.started.v1",
		"chora.delivery.live_quiz_session.score_awarded.v1",
		"chora.delivery.live_quiz_session.ended.v1",
	} {
		bz, err := protomarshal.MarshalPayload(topic, env, nil)
		if err != nil {
			t.Errorf("%s: nil payload: %v", topic, err)
			continue
		}
		num, _, n := protowire.ConsumeTag(bz)
		if n < 0 || num != 1 {
			t.Errorf("%s: nil payload leading tag = %d (want envelope=1)", topic, num)
		}
	}
}

func TestBranch_LiveQuiz_BadTimestamps(t *testing.T) {
	env := fixedEnvelope()
	cases := []struct {
		topic string
		key   string
	}{
		{"chora.delivery.live_quiz_session.started.v1", "started_at"},
		{"chora.delivery.live_quiz_session.ended.v1", "ended_at"},
	}
	for _, tc := range cases {
		payload := map[string]any{"session_id": "sess-1", tc.key: 42}
		if _, err := protomarshal.MarshalPayload(tc.topic, env, payload); err == nil {
			t.Errorf("%s key %q bad timestamp: expected error", tc.topic, tc.key)
		}
	}
}

// TestBranch_ScoreAwarded_SliceShapes drives asStringSlice through the
// topic_tags key with []any (mixed element types), a scalar string (default →
// nil), and a non-string slice (default → nil).
func TestBranch_ScoreAwarded_SliceShapes(t *testing.T) {
	env := fixedEnvelope()
	base := map[string]any{
		"session_id":   "sess-1",
		"live_quiz_id": "lq-1",
		"question_id":  "q-1",
	}

	bz, err := protomarshal.MarshalPayload("chora.delivery.live_quiz_session.score_awarded.v1", env, cloneAny(base))
	if err == nil && len(bz) == 0 {
		t.Fatal("empty bytes")
	}

	withTags := cloneAny(base)
	withTags["topic_tags"] = []any{"scrum", 42, "agile", ""}
	bz, err = protomarshal.MarshalPayload("chora.delivery.live_quiz_session.score_awarded.v1", env, withTags)
	if err != nil {
		t.Fatalf("topic_tags []any: %v", err)
	}
	if got := collectWireStrings(t, bz, 10); len(got) != 2 || got[0] != "scrum" || got[1] != "agile" {
		t.Errorf("topic_tags []any = %v; want [scrum agile]", got)
	}

	for _, v := range []any{"single-string", []int{1, 2}, nil} {
		withTags := cloneAny(base)
		withTags["topic_tags"] = v
		if _, err := protomarshal.MarshalPayload("chora.delivery.live_quiz_session.score_awarded.v1", env, withTags); err != nil {
			t.Errorf("topic_tags %T: %v", v, err)
		}
	}
}

// -----------------------------------------------------------------------------
// CourseCreated — full payload + bad strings
// -----------------------------------------------------------------------------

func TestBranch_CourseCreated_FullPayload(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"course_id":       "course-1",
		"title":           "T",
		"instructor_gcid": "gcid-inst",
		"atom_path_id":    "atom-1",
		"modality":        int32(2),
		"capacity":        uint(30),
		"created_at":      env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.course.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6, 9, 10} {
		if !seen[want] {
			t.Errorf("CourseCreated full: missing field %d (seen: %v)", want, seen)
		}
	}
}

func TestBranch_CourseCreated_BadStrings(t *testing.T) {
	env := fixedEnvelope()
	base := map[string]any{"course_id": "c-1"}
	for _, key := range []string{"title", "instructor_gcid", "atom_path_id"} {
		payload := cloneAny(base)
		payload[key] = 42
		if _, err := protomarshal.MarshalPayload("chora.delivery.course.created.v1", env, payload); err == nil {
			t.Errorf("course.created key %q bad string: expected error", key)
		}
	}
}

// -----------------------------------------------------------------------------
// BookingConfirmed — bad strings
// -----------------------------------------------------------------------------

func TestBranch_BookingConfirmed_BadStrings(t *testing.T) {
	env := fixedEnvelope()
	base := map[string]any{"booking_id": "b-1"}
	for _, key := range []string{"booking_id", "course_id", "learner_gcid"} {
		payload := cloneAny(base)
		payload[key] = 42
		if _, err := protomarshal.MarshalPayload("chora.delivery.booking.confirmed.v1", env, payload); err == nil {
			t.Errorf("booking.confirmed key %q bad string: expected error", key)
		}
	}
}

// -----------------------------------------------------------------------------
// CertIssued — full payload (cert_type + expires_at), cert_id fallback,
// readString error paths
// -----------------------------------------------------------------------------

func TestBranch_CertIssued_FullAndFallback(t *testing.T) {
	env := fixedEnvelope()
	ts := env.OccurredAt.Format(time.RFC3339Nano)

	full := map[string]any{
		"certification_id": "cert-1",
		"learner_gcid":     "gcid-learner",
		"course_id":        "course-1",
		"cert_type":        int32(2),
		"issued_at":        ts,
		"expires_at":       ts,
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.certification.issued.v1", env, full)
	if err != nil {
		t.Fatalf("cert full: %v", err)
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6, 7} {
		if !seen[want] {
			t.Errorf("CertIssued full: missing field %d (seen: %v)", want, seen)
		}
	}

	// Producer-side key absent → fall back to schema-key cert_id.
	fallback := map[string]any{
		"cert_id":      "cert-2",
		"learner_gcid": "gcid-learner",
		"course_id":    "course-1",
	}
	bz, err = protomarshal.MarshalPayload("chora.delivery.certification.issued.v1", env, fallback)
	if err != nil {
		t.Fatalf("cert fallback: %v", err)
	}
	if got := collectWireStrings(t, bz, 2); len(got) != 1 || got[0] != "cert-2" {
		t.Errorf("CertIssued cert_id fallback field 2 = %v; want [cert-2]", got)
	}

	// readString error: certification_id present with wrong type.
	bad1 := map[string]any{"certification_id": 42, "learner_gcid": "g", "course_id": "c"}
	if _, err := protomarshal.MarshalPayload("chora.delivery.certification.issued.v1", env, bad1); err == nil {
		t.Error("cert certification_id=42: expected error")
	}
	// Timestamp error: expires_at wrong type.
	bad3 := map[string]any{"certification_id": "cert-1", "learner_gcid": "g", "course_id": "c", "expires_at": 42}
	if _, err := protomarshal.MarshalPayload("chora.delivery.certification.issued.v1", env, bad3); err == nil {
		t.Error("cert expires_at=42: expected error")
	}
	// readString error: certification_id absent → cert_id present with wrong type.
	bad2 := map[string]any{"cert_id": 42, "learner_gcid": "g", "course_id": "c"}
	if _, err := protomarshal.MarshalPayload("chora.delivery.certification.issued.v1", env, bad2); err == nil {
		t.Error("cert cert_id=42: expected error")
	}
}

func TestBranch_CertIssued_BadStrings(t *testing.T) {
	env := fixedEnvelope()
	base := map[string]any{"certification_id": "cert-1"}
	for _, key := range []string{"learner_gcid", "course_id"} {
		payload := cloneAny(base)
		payload[key] = 42
		if _, err := protomarshal.MarshalPayload("chora.delivery.certification.issued.v1", env, payload); err == nil {
			t.Errorf("cert.issued key %q bad string: expected error", key)
		}
	}
}

// -----------------------------------------------------------------------------
// EnrollmentCompleted — bad timestamp + bad strings
// -----------------------------------------------------------------------------

func TestBranch_EnrollmentCompleted_BadTimestampAndStrings(t *testing.T) {
	env := fixedEnvelope()
	base := map[string]any{"enrollment_id": "enr-1"}
	for _, key := range []string{"completed_at", "learner_gcid", "course_id"} {
		payload := cloneAny(base)
		payload[key] = 42
		if _, err := protomarshal.MarshalPayload("chora.delivery.enrollment.completed.v1", env, payload); err == nil {
			t.Errorf("enrollment.completed key %q bad value: expected error", key)
		}
	}
}

// -----------------------------------------------------------------------------
// Application lifecycle — core + withdrawn/rejected fallback + readString
// error paths
// -----------------------------------------------------------------------------

func TestBranch_ApplicationCore_BadStrings(t *testing.T) {
	env := fixedEnvelope()
	for _, key := range []string{"application_id", "learner_gcid", "course_id"} {
		payload := map[string]any{key: 42}
		if _, err := protomarshal.MarshalPayload("chora.delivery.application.submitted.v1", env, payload); err == nil {
			t.Errorf("application.submitted key %q bad string: expected error", key)
		}
	}
}

func TestBranch_ApplicationWithdrawn_Variants(t *testing.T) {
	env := fixedEnvelope()
	ts := env.OccurredAt.Format(time.RFC3339Nano)

	full := map[string]any{
		"application_id":   "app-1",
		"learner_gcid":     "gcid-learner",
		"course_id":        "course-1",
		"prior_status":     int32(2),
		"withdrawn_reason": "personal",
		"updated_at":       ts,
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.withdrawn.v1", env, full)
	if err != nil {
		t.Fatalf("withdrawn full: %v", err)
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{2, 3, 4, 5, 6, 7} {
		if !seen[want] {
			t.Errorf("Withdrawn full: missing field %d (seen: %v)", want, seen)
		}
	}

	// withdrawn_reason absent → fall back to generic `reason`.
	fallback := map[string]any{
		"application_id": "app-1",
		"learner_gcid":   "gcid-learner",
		"course_id":      "course-1",
		"reason":         "moved",
	}
	bz, err = protomarshal.MarshalPayload("chora.delivery.application.withdrawn.v1", env, fallback)
	if err != nil {
		t.Fatalf("withdrawn fallback: %v", err)
	}
	if got := collectWireStrings(t, bz, 6); len(got) != 1 || got[0] != "moved" {
		t.Errorf("Withdrawn reason fallback field 6 = %v; want [moved]", got)
	}

	// readString error paths.
	base := map[string]any{"application_id": "app-1", "learner_gcid": "g", "course_id": "c"}
	bad1 := cloneAny(base)
	bad1["withdrawn_reason"] = 42
	if _, err := protomarshal.MarshalPayload("chora.delivery.application.withdrawn.v1", env, bad1); err == nil {
		t.Error("withdrawn withdrawn_reason=42: expected error")
	}
	bad2 := cloneAny(base)
	bad2["reason"] = 42
	if _, err := protomarshal.MarshalPayload("chora.delivery.application.withdrawn.v1", env, bad2); err == nil {
		t.Error("withdrawn reason=42: expected error")
	}
}

func TestBranch_ApplicationRejected_Variants(t *testing.T) {
	env := fixedEnvelope()
	ts := env.OccurredAt.Format(time.RFC3339Nano)

	fallback := map[string]any{
		"application_id": "app-1",
		"learner_gcid":   "gcid-learner",
		"course_id":      "course-1",
		"reason":         "below cut-off",
		"updated_at":     ts,
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.rejected.v1", env, fallback)
	if err != nil {
		t.Fatalf("rejected fallback: %v", err)
	}
	if got := collectWireStrings(t, bz, 6); len(got) != 1 || got[0] != "below cut-off" {
		t.Errorf("Rejected reason fallback field 6 = %v; want [below cut-off]", got)
	}

	base := map[string]any{"application_id": "app-1", "learner_gcid": "g", "course_id": "c"}
	bad1 := cloneAny(base)
	bad1["rejected_reason"] = 42
	if _, err := protomarshal.MarshalPayload("chora.delivery.application.rejected.v1", env, bad1); err == nil {
		t.Error("rejected rejected_reason=42: expected error")
	}
	bad2 := cloneAny(base)
	bad2["reason"] = 42
	if _, err := protomarshal.MarshalPayload("chora.delivery.application.rejected.v1", env, bad2); err == nil {
		t.Error("rejected reason=42: expected error")
	}
}

func TestBranch_ApplicationPaid_BadStrings(t *testing.T) {
	env := fixedEnvelope()
	base := map[string]any{"application_id": "app-1", "learner_gcid": "g", "course_id": "c"}
	for _, key := range []string{"stripe_payment_intent_id", "currency"} {
		payload := cloneAny(base)
		payload[key] = 42
		if _, err := protomarshal.MarshalPayload("chora.delivery.application.paid.v1", env, payload); err == nil {
			t.Errorf("application.paid key %q bad string: expected error", key)
		}
	}
}

func TestBranch_ApplicationEnrolled_BadString(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id": "app-1",
		"learner_gcid":   "g",
		"course_id":      "c",
		"enrollment_id":  42,
	}
	if _, err := protomarshal.MarshalPayload("chora.delivery.application.enrolled.v1", env, payload); err == nil {
		t.Error("application.enrolled enrollment_id=42: expected error")
	}
}

func TestBranch_ApplicationOfferMade_BadTimestamp(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id": "app-1",
		"learner_gcid":   "g",
		"course_id":      "c",
		"updated_at":     42,
	}
	if _, err := protomarshal.MarshalPayload("chora.delivery.application.offer_made.v1", env, payload); err == nil {
		t.Error("application.offer_made updated_at=42: expected error")
	}
}

// -----------------------------------------------------------------------------
// CourseReleased — asStringSlice shapes + bad timestamps
// -----------------------------------------------------------------------------

func TestBranch_CourseReleased_SliceShapes(t *testing.T) {
	env := fixedEnvelope()

	// instructor_gcids as []any with a non-string + empty element must skip
	// both; test_set_ids as []any drives the same branch.
	payload := map[string]any{
		"course_id":        "c-1",
		"title":            "T",
		"author_gcid":      "a-1",
		"instructor_gcids": []any{"g-1", "", 42, "g-2"},
		"test_set_ids":     []any{"ts-1", ""},
		"released_at":      env.OccurredAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.course.released.v1", env, payload)
	if err != nil {
		t.Fatalf("course.released []any: %v", err)
	}
	if got := collectWireStrings(t, bz, 8); len(got) != 2 || got[0] != "g-1" || got[1] != "g-2" {
		t.Errorf("instructor_gcids (8) = %v; want [g-1 g-2]", got)
	}
	if got := collectWireStrings(t, bz, 9); len(got) != 1 || got[0] != "ts-1" {
		t.Errorf("test_set_ids (9) = %v; want [ts-1]", got)
	}

	// Scalar string → asStringSlice default (nil) → no entries, no error.
	scalar := map[string]any{
		"course_id":        "c-1",
		"title":            "T",
		"author_gcid":      "a-1",
		"instructor_gcids": "g-1",
		"released_at":      env.OccurredAt,
	}
	if _, err := protomarshal.MarshalPayload("chora.delivery.course.released.v1", env, scalar); err != nil {
		t.Fatalf("course.released scalar slice: %v", err)
	}
}

func TestBranch_CourseReleased_BadTimestamps(t *testing.T) {
	env := fixedEnvelope()
	for _, key := range []string{"scheduled_open_at", "released_at"} {
		payload := map[string]any{"course_id": "c-1", key: 42}
		if _, err := protomarshal.MarshalPayload("chora.delivery.course.released.v1", env, payload); err == nil {
			t.Errorf("course.released key %q bad timestamp: expected error", key)
		}
	}
}

// -----------------------------------------------------------------------------
// CoursePublished — nil payload, bad timestamp, []any dimensions
// -----------------------------------------------------------------------------

func TestBranch_CoursePublished_Variants(t *testing.T) {
	env := fixedEnvelope()

	// asStringSlice via chora_imda_dimensions as []any.
	payload := map[string]any{
		"course_id":             "c-1",
		"title":                 "T",
		"instructor_gcid":       "g-1",
		"published_at":          env.OccurredAt,
		"chora_imda_dimensions": []any{"accountability", "", 0, "transparency"},
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.course.published.v1", env, payload)
	if err != nil {
		t.Fatalf("course.published []any: %v", err)
	}
	if got := collectWireStrings(t, bz, 6); len(got) != 2 || got[0] != "accountability" || got[1] != "transparency" {
		t.Errorf("imda_dimensions (6) = %v; want [accountability transparency]", got)
	}

	if _, err := protomarshal.MarshalPayload("chora.delivery.course.published.v1", env, nil); err != nil {
		t.Errorf("course.published nil payload: %v", err)
	}

	bad := map[string]any{"course_id": "c-1", "published_at": 42}
	if _, err := protomarshal.MarshalPayload("chora.delivery.course.published.v1", env, bad); err == nil {
		t.Error("course.published published_at=42: expected error")
	}
}

// -----------------------------------------------------------------------------
// ExamResult — outcome enum variants + bad timestamp
// -----------------------------------------------------------------------------

func TestBranch_ExamResult_OutcomeVariants(t *testing.T) {
	env := fixedEnvelope()
	base := map[string]any{"result_id": "res-1"}
	for _, outcome := range []any{
		"fail",
		" EXAM_RESULT_OUTCOME_FAIL ",
		"EXAM_RESULT_OUTCOME_PASS",
		"critical-error",
		true,
		int8(1), // non-int32 numeric → asInt32 default → skip, no error
	} {
		payload := cloneAny(base)
		payload["outcome"] = outcome
		if _, err := protomarshal.MarshalPayload(examResultReleasedTopic, env, payload); err != nil {
			t.Errorf("exam outcome %v (%T): %v", outcome, outcome, err)
		}
	}
}

func TestBranch_ExamResult_BadTimestamp(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{"result_id": "res-1", "scored_at": 42}
	if _, err := protomarshal.MarshalPayload(examResultReleasedTopic, env, payload); err == nil {
		t.Error("exam_result scored_at=42: expected error")
	}
}

// -----------------------------------------------------------------------------
// Loose-typed coercion helpers — one value type per case-branch, driven
// through encoder keys.
// -----------------------------------------------------------------------------

// TestBranch_asInt32_Branches drives every asInt32 case via course.created's
// `capacity` slot (int32-encode-on-non-zero).
func TestBranch_asInt32_Branches(t *testing.T) {
	env := fixedEnvelope()
	values := []any{
		int(7), int32(7), int64(7), float32(7), float64(7),
		uint(7), uint32(7), uint64(7),
		nil, "7", struct{}{}, int8(7),
	}
	for _, v := range values {
		payload := map[string]any{"course_id": "c-1", "title": "T", "capacity": v}
		if _, err := protomarshal.MarshalPayload("chora.delivery.course.created.v1", env, payload); err != nil {
			t.Errorf("capacity %T: %v", v, err)
		}
	}
}

// TestBranch_asFloat64_Branches drives every asFloat64 case via
// test_set.question_added's `points` (double) slot.
func TestBranch_asFloat64_Branches(t *testing.T) {
	env := fixedEnvelope()
	values := []any{
		float64(7.5), float32(7.5), int(7), int32(7), int64(7),
		uint(7), uint32(7), uint64(7),
		nil, "7", struct{}{}, int8(7),
	}
	for _, v := range values {
		payload := map[string]any{"test_set_id": "ts-1", "test_set_question_id": "tsq-1", "points": v}
		if _, err := protomarshal.MarshalPayload("chora.delivery.test_set.question_added.v1", env, payload); err != nil {
			t.Errorf("points %T: %v", v, err)
		}
	}
}

// TestBranch_asInt64_Branches drives every asInt64 case via application.paid's
// `total_micros` (int64) slot.
func TestBranch_asInt64_Branches(t *testing.T) {
	env := fixedEnvelope()
	values := []any{
		int(7), int32(7), int64(7), float32(7), float64(7),
		uint(7), uint32(7), uint64(7),
		nil, "7", struct{}{}, int8(7),
	}
	for _, v := range values {
		payload := map[string]any{"application_id": "app-1", "learner_gcid": "g", "course_id": "c", "total_micros": v}
		if _, err := protomarshal.MarshalPayload("chora.delivery.application.paid.v1", env, payload); err != nil {
			t.Errorf("total_micros %T: %v", v, err)
		}
	}
}

// TestBranch_asTime_EdgeShapes — nil pointer, empty string and typed nil values
// on a Timestamp slot must fail loud (these reach asTime's nil/empty branches).
func TestBranch_asTime_EdgeShapes(t *testing.T) {
	env := fixedEnvelope()
	badTimes := []any{
		(*time.Time)(nil),
		"",
		nil,
		0,
	}
	for _, v := range badTimes {
		payload := map[string]any{"course_id": "c-1", "title": "T", "created_at": v}
		if _, err := protomarshal.MarshalPayload("chora.delivery.course.created.v1", env, payload); err == nil {
			t.Errorf("created_at %T (%v): expected error", v, v)
		}
	}
}

// TestBranch_Envelope_TracestateNonEmpty — fixedEnvelope() leaves Tracestate
// empty, so the envelope field 8 append (if env.Tracestate != "") never runs
// in the other tests. A non-empty tracestate must wire-encode into the nested
// envelope.
func TestBranch_Envelope_TracestateNonEmpty(t *testing.T) {
	env := fixedEnvelope()
	env.Tracestate = "congo=t61rcWkgMzE"
	payload := map[string]any{
		"application_id": "app-1",
		"course_id":      "course-1",
		"learner_gcid":   "gcid-applicant",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.submitted.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	envBz := envelopeBytes(t, bz)
	seen := walkWireTags(t, envBz)
	if !seen[8] {
		t.Errorf("envelope missing tracestate field 8 (seen: %v)", seen)
	}
	if got := collectWireStrings(t, envBz, 8); len(got) != 1 || got[0] != env.Tracestate {
		t.Errorf("envelope tracestate (8) = %v; want [%s]", got, env.Tracestate)
	}
}
