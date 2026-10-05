// cloud_publisher_exhaust_test.go — external tests driving EVERY typed
// CloudPublisher.PublishX forward+tee path plus PublishCustom's aggregate
// branch selection. Complements cloud_helpers_internal_test.go (unexported
// helper error branches).
package events_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

// noCustomPublisher implements events.Publisher WITHOUT the optional
// PublishCustom capability (the assertion in CloudPublisher.PublishCustom /
// CourseContentPublisher / ExamResultPublisher depends on its absence). Plain
// struct — an embedded *events.InMemoryPublisher would promote PublishCustom.
type noCustomPublisher struct {
	inner *events.InMemoryPublisher
}

func (n *noCustomPublisher) PublishCourseCreated(in events.CourseCreated) (events.PublishedEvent, error) {
	return n.inner.PublishCourseCreated(in)
}
func (n *noCustomPublisher) PublishCoursePublished(in events.CoursePublished) (events.PublishedEvent, error) {
	return n.inner.PublishCoursePublished(in)
}
func (n *noCustomPublisher) PublishEnrollmentCreated(in events.EnrollmentCreated) (events.PublishedEvent, error) {
	return n.inner.PublishEnrollmentCreated(in)
}
func (n *noCustomPublisher) PublishEnrollmentCancelled(in events.EnrollmentCancelled) (events.PublishedEvent, error) {
	return n.inner.PublishEnrollmentCancelled(in)
}
func (n *noCustomPublisher) PublishEnrollmentCompleted(in events.EnrollmentCompleted) (events.PublishedEvent, error) {
	return n.inner.PublishEnrollmentCompleted(in)
}
func (n *noCustomPublisher) PublishBookingConfirmed(in events.BookingConfirmed) (events.PublishedEvent, error) {
	return n.inner.PublishBookingConfirmed(in)
}
func (n *noCustomPublisher) PublishCertificationIssued(in events.CertificationIssued) (events.PublishedEvent, error) {
	return n.inner.PublishCertificationIssued(in)
}
func (n *noCustomPublisher) PublishApplicationStateChanged(app *application.Application, traceparent string) (events.PublishedEvent, error) {
	return n.inner.PublishApplicationStateChanged(app, traceparent)
}
func (n *noCustomPublisher) PublishTestSetCreated(in events.TestSetCreated) (events.PublishedEvent, error) {
	return n.inner.PublishTestSetCreated(in)
}
func (n *noCustomPublisher) PublishTestSetQuestionAdded(in events.TestSetQuestionAdded) (events.PublishedEvent, error) {
	return n.inner.PublishTestSetQuestionAdded(in)
}
func (n *noCustomPublisher) PublishTestSetQuestionUpdated(in events.TestSetQuestionUpdated) (events.PublishedEvent, error) {
	return n.inner.PublishTestSetQuestionUpdated(in)
}
func (n *noCustomPublisher) PublishTestSetQuestionRemoved(in events.TestSetQuestionRemoved) (events.PublishedEvent, error) {
	return n.inner.PublishTestSetQuestionRemoved(in)
}
func (n *noCustomPublisher) PublishTestSetPublished(in events.TestSetPublished) (events.PublishedEvent, error) {
	return n.inner.PublishTestSetPublished(in)
}
func (n *noCustomPublisher) PublishLiveQuizScoreAwarded(in events.LiveQuizScoreAwarded) (events.PublishedEvent, error) {
	return n.inner.PublishLiveQuizScoreAwarded(in)
}
func (n *noCustomPublisher) PublishLiveQuizSessionStarted(in events.LiveQuizSessionStarted) (events.PublishedEvent, error) {
	return n.inner.PublishLiveQuizSessionStarted(in)
}
func (n *noCustomPublisher) PublishLiveQuizSessionEnded(in events.LiveQuizSessionEnded) (events.PublishedEvent, error) {
	return n.inner.PublishLiveQuizSessionEnded(in)
}

// nilFriendlyAppInner forwards PublishApplicationStateChanged even for a nil
// app (the real InMemoryPublisher rejects nil first, which would mask the
// CloudPublisher's own nil-application guard).
type nilFriendlyAppInner struct {
	*events.InMemoryPublisher
}

func (n *nilFriendlyAppInner) PublishApplicationStateChanged(app *application.Application, traceparent string) (events.PublishedEvent, error) {
	_ = app
	_ = traceparent
	return events.PublishedEvent{
		Topic:    events.TopicApplicationSubmitted,
		Envelope: events.EventEnvelope{EventID: "e1"},
		Payload:  map[string]interface{}{},
	}, nil
}

// -----------------------------------------------------------------------------
// Typed PublishX -> forward to inner + tee to outbox
// -----------------------------------------------------------------------------

func TestCloudPublisher_TypedPublish_TeesEveryEvent(t *testing.T) {
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	rec := &stubRecorder{}
	pub := events.NewCloudPublisher(inner, rec)

	tt := []struct {
		name        string
		call        func() error
		wantAggType string
		wantAggID   string
		wantTopic   string
		failCall    func() error // blank-tenant variant -> inner-error branch
	}{
		{
			name: "LiveQuizScoreAwarded",
			call: func() error {
				_, err := pub.PublishLiveQuizScoreAwarded(events.LiveQuizScoreAwarded{TenantID: tenantA, GCID: gcidA, SessionID: "s1", QuestionID: "q1"})
				return err
			},
			wantAggType: "live_quiz_session", wantAggID: "s1",
			wantTopic: events.TopicLiveQuizScoreAwarded,
			failCall:  func() error { _, err := pub.PublishLiveQuizScoreAwarded(events.LiveQuizScoreAwarded{}); return err },
		},
		{
			name: "LiveQuizSessionStarted",
			call: func() error {
				_, err := pub.PublishLiveQuizSessionStarted(events.LiveQuizSessionStarted{TenantID: tenantA, SessionID: "s2"})
				return err
			},
			wantAggType: "live_quiz_session", wantAggID: "s2",
			wantTopic: events.TopicLiveQuizSessionStarted,
			failCall:  func() error { _, err := pub.PublishLiveQuizSessionStarted(events.LiveQuizSessionStarted{}); return err },
		},
		{
			name: "LiveQuizSessionEnded",
			call: func() error {
				_, err := pub.PublishLiveQuizSessionEnded(events.LiveQuizSessionEnded{TenantID: tenantA, SessionID: "s3"})
				return err
			},
			wantAggType: "live_quiz_session", wantAggID: "s3",
			wantTopic: events.TopicLiveQuizSessionEnded,
			failCall:  func() error { _, err := pub.PublishLiveQuizSessionEnded(events.LiveQuizSessionEnded{}); return err },
		},
		{
			name: "CoursePublished",
			call: func() error {
				_, err := pub.PublishCoursePublished(events.CoursePublished{TenantID: tenantA, GCID: gcidA, CourseID: "course-pub"})
				return err
			},
			wantAggType: "course", wantAggID: "course-pub",
			wantTopic: events.TopicCoursePublished,
			failCall:  func() error { _, err := pub.PublishCoursePublished(events.CoursePublished{}); return err },
		},
		{
			name: "EnrollmentCancelled",
			call: func() error {
				_, err := pub.PublishEnrollmentCancelled(events.EnrollmentCancelled{TenantID: tenantA, EnrollmentID: "enr-cancel", CourseID: "c1", LearnerGCID: gcidA})
				return err
			},
			wantAggType: "enrollment", wantAggID: "enr-cancel",
			wantTopic: events.TopicEnrollmentCancelled,
			failCall:  func() error { _, err := pub.PublishEnrollmentCancelled(events.EnrollmentCancelled{}); return err },
		},
		{
			name: "BookingConfirmed",
			call: func() error {
				_, err := pub.PublishBookingConfirmed(events.BookingConfirmed{TenantID: tenantA, BookingID: "bk-1"})
				return err
			},
			wantAggType: "booking", wantAggID: "bk-1",
			wantTopic: events.TopicBookingConfirmed,
			failCall:  func() error { _, err := pub.PublishBookingConfirmed(events.BookingConfirmed{}); return err },
		},
		{
			name: "CertificationIssued",
			call: func() error {
				_, err := pub.PublishCertificationIssued(events.CertificationIssued{TenantID: tenantA, CertificationID: "cert-1"})
				return err
			},
			wantAggType: "certification", wantAggID: "cert-1",
			wantTopic: events.TopicCertificationIssued,
			failCall:  func() error { _, err := pub.PublishCertificationIssued(events.CertificationIssued{}); return err },
		},
		{
			name: "TestSetCreated",
			call: func() error {
				_, err := pub.PublishTestSetCreated(events.TestSetCreated{TenantID: tenantA, TestSetID: "ts-1"})
				return err
			},
			wantAggType: "test_set", wantAggID: "ts-1",
			wantTopic: events.TopicTestSetCreated,
			failCall:  func() error { _, err := pub.PublishTestSetCreated(events.TestSetCreated{}); return err },
		},
		{
			name: "TestSetQuestionAdded",
			call: func() error {
				_, err := pub.PublishTestSetQuestionAdded(events.TestSetQuestionAdded{TenantID: tenantA, TestSetID: "ts-add", TestSetQuestionID: "q-1"})
				return err
			},
			wantAggType: "test_set", wantAggID: "ts-add",
			wantTopic: events.TopicTestSetQuestionAdded,
			failCall:  func() error { _, err := pub.PublishTestSetQuestionAdded(events.TestSetQuestionAdded{}); return err },
		},
		{
			name: "TestSetQuestionUpdated",
			call: func() error {
				_, err := pub.PublishTestSetQuestionUpdated(events.TestSetQuestionUpdated{TenantID: tenantA, TestSetID: "ts-upd", TestSetQuestionID: "q-1"})
				return err
			},
			wantAggType: "test_set", wantAggID: "ts-upd",
			wantTopic: events.TopicTestSetQuestionUpdated,
			failCall:  func() error { _, err := pub.PublishTestSetQuestionUpdated(events.TestSetQuestionUpdated{}); return err },
		},
		{
			name: "TestSetQuestionRemoved",
			call: func() error {
				_, err := pub.PublishTestSetQuestionRemoved(events.TestSetQuestionRemoved{TenantID: tenantA, TestSetID: "ts-rm", TestSetQuestionID: "q-1"})
				return err
			},
			wantAggType: "test_set", wantAggID: "ts-rm",
			wantTopic: events.TopicTestSetQuestionRemoved,
			failCall:  func() error { _, err := pub.PublishTestSetQuestionRemoved(events.TestSetQuestionRemoved{}); return err },
		},
		{
			name: "TestSetPublished",
			call: func() error {
				_, err := pub.PublishTestSetPublished(events.TestSetPublished{TenantID: tenantA, TestSetID: "ts-pub"})
				return err
			},
			wantAggType: "test_set", wantAggID: "ts-pub",
			wantTopic: events.TopicTestSetPublished,
			failCall:  func() error { _, err := pub.PublishTestSetPublished(events.TestSetPublished{}); return err },
		},
	}

	for _, tc := range tt {
		t.Run(tc.name+" success", func(t *testing.T) {
			before := len(rec.rows)
			if err := tc.call(); err != nil {
				t.Fatalf("publish: %v", err)
			}
			if len(rec.rows) != before+1 {
				t.Fatalf("expected exactly 1 new outbox row; %d -> %d", before, len(rec.rows))
			}
			row := rec.rows[len(rec.rows)-1]
			if row.AggregateType != tc.wantAggType {
				t.Errorf("aggregate_type = %q, want %q", row.AggregateType, tc.wantAggType)
			}
			if row.AggregateID != tc.wantAggID {
				t.Errorf("aggregate_id = %q, want %q", row.AggregateID, tc.wantAggID)
			}
			if row.Topic != tc.wantTopic {
				t.Errorf("topic = %q, want %q", row.Topic, tc.wantTopic)
			}
		})
		t.Run(tc.name+" inner error", func(t *testing.T) {
			before := len(rec.rows)
			if err := tc.failCall(); err == nil {
				t.Fatalf("expected inner-publisher error, got nil")
			}
			if len(rec.rows) != before {
				t.Fatalf("no row must be written when the inner publisher errors; %d -> %d", before, len(rec.rows))
			}
		})
	}
}

// -----------------------------------------------------------------------------
// PublishApplicationStateChanged — real app, nil-app (cloud guard) + inner-error
// -----------------------------------------------------------------------------

func TestCloudPublisher_PublishApplicationStateChanged_TeesRealApp(t *testing.T) {
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	rec := &stubRecorder{}
	pub := events.NewCloudPublisher(inner, rec)

	app := buildAppAtStatus(t, application.StatusAccepted)
	ev, err := pub.PublishApplicationStateChanged(app, "")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if ev.Topic != "chora.delivery.application.accepted.v1" {
		t.Fatalf("topic = %q", ev.Topic)
	}
	if len(rec.rows) != 1 {
		t.Fatalf("expected 1 outbox row, got %d", len(rec.rows))
	}
	if rec.rows[0].AggregateType != "application" || rec.rows[0].AggregateID != app.ID {
		t.Fatalf("row aggregate = %s/%s, want application/%s", rec.rows[0].AggregateType, rec.rows[0].AggregateID, app.ID)
	}
}

func TestCloudPublisher_PublishApplicationStateChanged_NilAppGuard(t *testing.T) {
	// Inner must forward nil apps successfully for the cloud-level guard to
	// be reachable (the real InMemoryPublisher rejects nil first).
	pub := events.NewCloudPublisher(&nilFriendlyAppInner{InMemoryPublisher: events.NewInMemoryPublisher("p", "s")}, &stubRecorder{})
	_, err := pub.PublishApplicationStateChanged(nil, "")
	if err == nil || !strings.Contains(err.Error(), "nil application") {
		t.Fatalf("want cloud nil-application error, got %v", err)
	}

	// Inner-level guard (InMemoryPublisher rejects nil before teeing).
	innerPub := events.NewCloudPublisher(events.NewInMemoryPublisher("p", "s"), &stubRecorder{})
	if _, err := innerPub.PublishApplicationStateChanged(nil, ""); err == nil {
		t.Fatal("expected inner nil-application error")
	}
}

// -----------------------------------------------------------------------------
// PublishCustom — successful aggregate-selection branches + inner-without-
// PublishCustom error.
//
// Synthetic-but-well-formed topic names (chora.delivery.{submission|grading}.x.v1)
// keep the payload JSON-encodable for the outbox while still exercising the
// prefix-based aggregate selection in the CloudPublisher.
// -----------------------------------------------------------------------------

func TestCloudPublisher_PublishCustom_AggregateBranchSelection(t *testing.T) {
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	rec := &stubRecorder{}
	pub := events.NewCloudPublisher(inner, rec)

	tt := []struct {
		name      string
		topic     string
		payload   map[string]interface{}
		wantAgg   string
		wantAggID string
	}{
		{"submission with id", "chora.delivery.submission.graded.v1", map[string]interface{}{"submission_id": "sub-9"}, "submission", "sub-9"},
		{"submission without id", "chora.delivery.submission.graded.v1", map[string]interface{}{}, "submission", ""},
		{"grading with batch", "chora.delivery.grading.failed.v1", map[string]interface{}{"oe_batch_id": "batch-9"}, "grading", "batch-9"},
		{"default with assessment", "chora.delivery.custom.topic.v1", map[string]interface{}{"assessment_id": "ass-9"}, "assessment", "ass-9"},
		{"default without assessment", "chora.delivery.custom.topic.v1", map[string]interface{}{}, "assessment", ""},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			before := len(rec.rows)
			ev, err := pub.PublishCustom(tc.topic, "tenant-1", "gcid-1", tc.payload)
			if err != nil {
				t.Fatalf("PublishCustom: %v", err)
			}
			if ev.Topic != tc.topic {
				t.Fatalf("ev.Topic = %q, want %q", ev.Topic, tc.topic)
			}
			if len(rec.rows) != before+1 {
				t.Fatalf("expected 1 new row; %d -> %d", before, len(rec.rows))
			}
			row := rec.rows[len(rec.rows)-1]
			if row.AggregateType != tc.wantAgg || row.AggregateID != tc.wantAggID {
				t.Fatalf("row aggregate = %s/%s, want %s/%s", row.AggregateType, row.AggregateID, tc.wantAgg, tc.wantAggID)
			}
		})
	}
}

func TestCloudPublisher_PublishCustom_InnerWithoutCapability(t *testing.T) {
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	pub := events.NewCloudPublisher(&noCustomPublisher{inner: inner}, &stubRecorder{})
	_, err := pub.PublishCustom("chora.delivery.assessment.created.v1", "t", "g", map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "does not implement PublishCustom") {
		t.Fatalf("want inner-capability error, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Inner-error branches for the three typed publishes covered previously by
// other tests + the PublishCustom inner-error path (empty tenant).
// -----------------------------------------------------------------------------

func TestCloudPublisher_TypedPublish_InnerErrorBranches(t *testing.T) {
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	rec := &stubRecorder{}
	pub := events.NewCloudPublisher(inner, rec)
	before := len(rec.rows)

	if _, err := pub.PublishCourseCreated(events.CourseCreated{}); err == nil {
		t.Fatal("PublishCourseCreated: want inner error")
	}
	if _, err := pub.PublishEnrollmentCreated(events.EnrollmentCreated{}); err == nil {
		t.Fatal("PublishEnrollmentCreated: want inner error")
	}
	if _, err := pub.PublishEnrollmentCompleted(events.EnrollmentCompleted{}); err == nil {
		t.Fatal("PublishEnrollmentCompleted: want inner error")
	}
	if _, err := pub.PublishApplicationStateChanged(nil, ""); err == nil {
		t.Fatal("PublishApplicationStateChanged(nil): want inner error")
	}
	// InMemoryPublisher.PublishCustom rejects an empty tenant -> the
	// CloudPublisher's post-forward error branch.
	if _, err := pub.PublishCustom("chora.delivery.custom.topic.v1", "", "g", map[string]interface{}{}); err == nil {
		t.Fatal("PublishCustom: want inner error")
	}
	if len(rec.rows) != before {
		t.Fatalf("no rows expected after inner failures; %d -> %d", before, len(rec.rows))
	}
}

// -----------------------------------------------------------------------------
// NewCloudPublisher — trivial construction path
// -----------------------------------------------------------------------------

func TestNewCloudPublisher_Constructs(t *testing.T) {
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	rec := &stubRecorder{}
	pub := events.NewCloudPublisher(inner, rec)
	if pub == nil {
		t.Fatal("nil CloudPublisher")
	}
	var _ events.Publisher = pub // compile-time contract
}
