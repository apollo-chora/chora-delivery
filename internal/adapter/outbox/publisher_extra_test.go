// publisher_extra_test.go — additional TransactionalOutboxPublisher coverage:
// ADR-168 live-quiz topics, Lane A test-set topics, Lane B PublishCustom
// (assessmentAggregateOf topic routing + deriveEventType fallbacks), config
// defaults, the inner-error return of every forwarding Publish*, and the
// payload-encoding / tee error paths that must fail loud.
package outbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/outbox"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

const (
	extraTenant = "00000000-0000-0000-0000-0000000000aa"
	extraGCID   = "00000000-0000-0000-0000-0000000000bb"
)

// noCustomPublisher is a full events.Publisher that deliberately does NOT
// implement PublishCustom — used to exercise the type-assertion failure path
// in TransactionalOutboxPublisher.PublishCustom, and to return a successful
// publish for a nil application (which events.InMemoryPublisher rejects, so
// the outbox-level nil check would otherwise be unreachable).
type noCustomPublisher struct {
	project string
	service string
}

func (p *noCustomPublisher) envelope(tenant string) events.EventEnvelope {
	return events.EventEnvelope{
		EventID:        "evt-" + tenant,
		IdempotencyKey: "idem-" + tenant,
		TenantID:       tenant,
		GCID:           extraGCID,
		OccurredAt:     time.Now().UTC(),
		PublishedAt:    time.Now().UTC(),
		Traceparent:    "00-deadbeefdeadbeefdeadbeefdeadbeef-1111111122222222-01",
		SourceProject:  p.project,
		SourceService:  p.service,
		SchemaVersion:  1,
	}
}

func (p *noCustomPublisher) PublishCourseCreated(in events.CourseCreated) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicCourseCreated, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"course_id": in.CourseID}}, nil
}

func (p *noCustomPublisher) PublishCoursePublished(in events.CoursePublished) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicCoursePublished, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"course_id": in.CourseID}}, nil
}

func (p *noCustomPublisher) PublishEnrollmentCreated(in events.EnrollmentCreated) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicEnrollmentCreated, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"enrollment_id": in.EnrollmentID}}, nil
}

func (p *noCustomPublisher) PublishEnrollmentCancelled(in events.EnrollmentCancelled) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicEnrollmentCancelled, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"enrollment_id": in.EnrollmentID}}, nil
}

func (p *noCustomPublisher) PublishEnrollmentCompleted(in events.EnrollmentCompleted) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicEnrollmentCompleted, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"enrollment_id": in.EnrollmentID}}, nil
}

func (p *noCustomPublisher) PublishBookingConfirmed(in events.BookingConfirmed) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicBookingConfirmed, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"booking_id": in.BookingID}}, nil
}

func (p *noCustomPublisher) PublishCertificationIssued(in events.CertificationIssued) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicCertificationIssued, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"certification_id": in.CertificationID}}, nil
}

func (p *noCustomPublisher) PublishApplicationStateChanged(app *application.Application, _ string) (events.PublishedEvent, error) {
	tenant := ""
	if app != nil {
		tenant = app.TenantID
	}
	return events.PublishedEvent{Topic: events.TopicApplicationSubmitted, Envelope: p.envelope(tenant), Payload: map[string]any{}}, nil
}

func (p *noCustomPublisher) PublishTestSetCreated(in events.TestSetCreated) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicTestSetCreated, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"test_set_id": in.TestSetID}}, nil
}

func (p *noCustomPublisher) PublishTestSetQuestionAdded(in events.TestSetQuestionAdded) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicTestSetQuestionAdded, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"test_set_id": in.TestSetID}}, nil
}

func (p *noCustomPublisher) PublishTestSetQuestionUpdated(in events.TestSetQuestionUpdated) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicTestSetQuestionUpdated, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"test_set_id": in.TestSetID}}, nil
}

func (p *noCustomPublisher) PublishTestSetQuestionRemoved(in events.TestSetQuestionRemoved) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicTestSetQuestionRemoved, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"test_set_id": in.TestSetID}}, nil
}

func (p *noCustomPublisher) PublishTestSetPublished(in events.TestSetPublished) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicTestSetPublished, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"test_set_id": in.TestSetID}}, nil
}

func (p *noCustomPublisher) PublishLiveQuizScoreAwarded(in events.LiveQuizScoreAwarded) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicLiveQuizScoreAwarded, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"session_id": in.SessionID}}, nil
}

func (p *noCustomPublisher) PublishLiveQuizSessionStarted(in events.LiveQuizSessionStarted) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicLiveQuizSessionStarted, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"session_id": in.SessionID}}, nil
}

func (p *noCustomPublisher) PublishLiveQuizSessionEnded(in events.LiveQuizSessionEnded) (events.PublishedEvent, error) {
	return events.PublishedEvent{Topic: events.TopicLiveQuizSessionEnded, Envelope: p.envelope(in.TenantID), Payload: map[string]any{"session_id": in.SessionID}}, nil
}

// failInsertStore embeds a working InMemoryStore but always fails Insert —
// exercises the `return ev, err` after the tee write in every forwarding
// publisher (identical canned-error pattern to the dispatcher stub stores).
type failInsertStore struct {
	*outbox.InMemoryStore
}

func (s *failInsertStore) Insert(context.Context, outbox.Row) error {
	return errors.New("store: insert failed")
}

// TestOutboxPublisher_NewDefaults_AndSourceEnvFallback — constructs the
// publisher with ONLY Inner+Store so the constructor defaults (Now /
// SourceProject / SourceService) apply, and the per-event envelope source is
// empty so envSourceProject/envSourceService fall back to those defaults.
func TestOutboxPublisher_NewDefaults_AndSourceEnvFallback(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	// Empty inner source values force the config-default fallback branches.
	pub := outbox.NewTransactionalPublisher(outbox.PublisherConfig{
		Inner: events.NewInMemoryPublisher("", ""),
		Store: store,
	})
	_, err := pub.PublishCourseCreated(events.CourseCreated{
		TenantID: extraTenant, GCID: extraGCID, CourseID: "course-def-1",
		Title: "Defaults", InstructorGCID: extraGCID,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 5)
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	if rows[0].Envelope["source_project"] != "chora-489812" {
		t.Errorf("source_project = %q; want chora-489812 (config default)", rows[0].Envelope["source_project"])
	}
	if rows[0].Envelope["source_service"] != "chora-delivery" {
		t.Errorf("source_service = %q; want chora-delivery (config default)", rows[0].Envelope["source_service"])
	}
}

func TestOutboxPublisher_LiveQuizTopics_TeeIntoOutbox(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		emit      func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error)
		topic     string
		sessionID string
	}{
		{
			name: "ScoreAwarded",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishLiveQuizScoreAwarded(events.LiveQuizScoreAwarded{
					TenantID: extraTenant, GCID: extraGCID, SessionID: "sess-1",
					LiveQuizID: "lq-1", QuestionID: "q-1", AwardedPoints: 10,
					CumulativeScore: 20, Correct: true, AnswerMillis: 1500,
				})
			},
			topic:     events.TopicLiveQuizScoreAwarded,
			sessionID: "sess-1",
		},
		{
			name: "SessionStarted",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishLiveQuizSessionStarted(events.LiveQuizSessionStarted{
					TenantID: extraTenant, GCID: extraGCID, SessionID: "sess-2",
					LiveQuizID: "lq-2", InstructorGCID: extraGCID,
				})
			},
			topic:     events.TopicLiveQuizSessionStarted,
			sessionID: "sess-2",
		},
		{
			name: "SessionEnded",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishLiveQuizSessionEnded(events.LiveQuizSessionEnded{
					TenantID: extraTenant, GCID: extraGCID, SessionID: "sess-3",
					LiveQuizID: "lq-3", TotalResponses: 12,
				})
			},
			topic:     events.TopicLiveQuizSessionEnded,
			sessionID: "sess-3",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := outbox.NewInMemoryStore()
			pub := newOutboxPublisher(store)
			ev, err := tc.emit(pub)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if ev.Topic != tc.topic {
				t.Errorf("ev.Topic = %q; want %q", ev.Topic, tc.topic)
			}
			rows, _ := store.FetchPending(context.Background(), 10)
			if len(rows) != 1 {
				t.Fatalf("rows = %d; want 1", len(rows))
			}
			if rows[0].AggregateType != "live_quiz_session" {
				t.Errorf("row.AggregateType = %q; want live_quiz_session", rows[0].AggregateType)
			}
			if rows[0].AggregateID != tc.sessionID {
				t.Errorf("row.AggregateID = %q; want %q", rows[0].AggregateID, tc.sessionID)
			}
		})
	}
}

func TestOutboxPublisher_TestSetTopics_TeeIntoOutbox(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		emit      func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error)
		topic     string
		testSetID string
	}{
		{
			name: "Created",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishTestSetCreated(events.TestSetCreated{
					TenantID: extraTenant, GCID: extraGCID, TestSetID: "ts-1",
					AuthorGCID: extraGCID, Title: "Quiz",
				})
			},
			topic:     events.TopicTestSetCreated,
			testSetID: "ts-1",
		},
		{
			name: "QuestionAdded",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishTestSetQuestionAdded(events.TestSetQuestionAdded{
					TenantID: extraTenant, GCID: extraGCID, TestSetID: "ts-2",
					TestSetQuestionID: "q-1", QuestionAtomID: "a-1",
					QuestionType: "mcq", DisplayOrder: 1, Points: 5,
				})
			},
			topic:     events.TopicTestSetQuestionAdded,
			testSetID: "ts-2",
		},
		{
			name: "QuestionUpdated",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishTestSetQuestionUpdated(events.TestSetQuestionUpdated{
					TenantID: extraTenant, GCID: extraGCID, TestSetID: "ts-3",
					TestSetQuestionID: "q-2", DisplayOrder: 2, Points: 10,
				})
			},
			topic:     events.TopicTestSetQuestionUpdated,
			testSetID: "ts-3",
		},
		{
			name: "QuestionRemoved",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishTestSetQuestionRemoved(events.TestSetQuestionRemoved{
					TenantID: extraTenant, GCID: extraGCID, TestSetID: "ts-4",
					TestSetQuestionID: "q-3",
				})
			},
			topic:     events.TopicTestSetQuestionRemoved,
			testSetID: "ts-4",
		},
		{
			name: "Published",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishTestSetPublished(events.TestSetPublished{
					TenantID: extraTenant, GCID: extraGCID, TestSetID: "ts-5",
					AuthorGCID: extraGCID, QuestionCount: 4, TotalPoints: 20,
				})
			},
			topic:     events.TopicTestSetPublished,
			testSetID: "ts-5",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := outbox.NewInMemoryStore()
			pub := newOutboxPublisher(store)
			ev, err := tc.emit(pub)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if ev.Topic != tc.topic {
				t.Errorf("ev.Topic = %q; want %q", ev.Topic, tc.topic)
			}
			rows, _ := store.FetchPending(context.Background(), 10)
			if len(rows) != 1 {
				t.Fatalf("rows = %d; want 1", len(rows))
			}
			if rows[0].AggregateType != "test_set" {
				t.Errorf("row.AggregateType = %q; want test_set", rows[0].AggregateType)
			}
			if rows[0].AggregateID != tc.testSetID {
				t.Errorf("row.AggregateID = %q; want %q", rows[0].AggregateID, tc.testSetID)
			}
		})
	}
}

// TestOutboxPublisher_PublishCustom_TeesAggregatePerTopic — exercises
// assessmentAggregateOf routing (assessment / submission / grading /
// exam_result / course / unknown) AND the deriveEventType fallback branches
// (<4 segments and non-v{N} suffix) through the real store row.
func TestOutboxPublisher_PublishCustom_TeesAggregatePerTopic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		topic       string
		payload     map[string]any
		wantAggType string
		wantAggID   string
		wantEvent   string
	}{
		{
			name: "Assessment", topic: "chora.delivery.assessment.created.v1",
			payload:     map[string]any{"assessment_id": "assess-1"},
			wantAggType: "assessment", wantAggID: "assess-1", wantEvent: "assessment.created",
		},
		{
			name: "Submission", topic: "chora.delivery.submission.submitted.v1",
			payload:     map[string]any{"submission_id": "sub-1"},
			wantAggType: "submission", wantAggID: "sub-1", wantEvent: "submission.submitted",
		},
		{
			name: "Grading", topic: "chora.delivery.grading.failed.v1",
			payload:     map[string]any{"oe_batch_id": "batch-1"},
			wantAggType: "grading", wantAggID: "batch-1", wantEvent: "grading.failed",
		},
		{
			name: "ExamResult", topic: "chora.delivery.exam_result.released.v1",
			payload:     map[string]any{"result_id": "res-1"},
			wantAggType: "exam_result", wantAggID: "res-1", wantEvent: "exam_result.released",
		},
		{
			name: "CourseContentComposed", topic: "chora.delivery.course.content_composed.v1",
			payload:     map[string]any{"course_id": "course-1"},
			wantAggType: "course", wantAggID: "course-1", wantEvent: "course.content_composed",
		},
		{
			name: "NoVersionSuffix", topic: "chora.delivery.submission.scores",
			payload:     map[string]any{"submission_id": "sub-2"},
			wantAggType: "submission", wantAggID: "sub-2", wantEvent: "submission.scores",
		},
		{
			name: "UnknownTopic", topic: "app.some.event",
			payload:     map[string]any{},
			wantAggType: "unknown", wantAggID: "", wantEvent: "app.some.event",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := outbox.NewInMemoryStore()
			pub := newOutboxPublisher(store)
			ev, err := pub.PublishCustom(tc.topic, extraTenant, extraGCID, tc.payload)
			if err != nil {
				t.Fatalf("%s: PublishCustom: %v", tc.name, err)
			}
			if ev.Topic != tc.topic {
				t.Errorf("ev.Topic = %q; want %q", ev.Topic, tc.topic)
			}
			rows, _ := store.FetchPending(context.Background(), 10)
			if len(rows) != 1 {
				t.Fatalf("rows = %d; want 1", len(rows))
			}
			if rows[0].AggregateType != tc.wantAggType {
				t.Errorf("row.AggregateType = %q; want %q", rows[0].AggregateType, tc.wantAggType)
			}
			if rows[0].AggregateID != tc.wantAggID {
				t.Errorf("row.AggregateID = %q; want %q", rows[0].AggregateID, tc.wantAggID)
			}
			if rows[0].EventType != tc.wantEvent {
				t.Errorf("row.EventType = %q; want %q", rows[0].EventType, tc.wantEvent)
			}
		})
	}
}

func TestOutboxPublisher_PublishCustom_RejectsInnerWithoutCustom(t *testing.T) {
	t.Parallel()
	pub := outbox.NewTransactionalPublisher(outbox.PublisherConfig{
		Inner: &noCustomPublisher{project: "chora-489812", service: "chora-delivery"},
		Store: outbox.NewInMemoryStore(),
	})
	_, err := pub.PublishCustom("chora.delivery.assessment.created.v1", extraTenant, extraGCID, map[string]any{"assessment_id": "a-1"})
	if err == nil {
		t.Fatal("expected error when inner publisher lacks PublishCustom")
	}
	if !strings.Contains(err.Error(), "does not implement PublishCustom") {
		t.Errorf("err = %v; want 'does not implement PublishCustom'", err)
	}
}

func TestOutboxPublisher_PublishCustom_SurfacesInnerError(t *testing.T) {
	t.Parallel()
	pub := newOutboxPublisher(outbox.NewInMemoryStore())
	_, err := pub.PublishCustom("chora.delivery.assessment.created.v1", "", extraGCID, map[string]any{"assessment_id": "a-1"})
	if err == nil {
		t.Fatal("expected inner error for empty tenant")
	}
}

// TestOutboxPublisher_PublishCustom_EncodingErrorSurfaces — booking.confirmed.v1
// HAS a binary protobuf encoder; a seat_number of the wrong type is a REAL
// encoder error (not ErrUnsupportedTopic) and must fail loud — never silently
// fall back to JSON.
func TestOutboxPublisher_PublishCustom_EncodingErrorSurfaces(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	_, err := pub.PublishCustom("chora.delivery.booking.confirmed.v1", extraTenant, extraGCID, map[string]any{
		"booking_id": "b-1", "course_id": "c-1", "learner_gcid": extraGCID, "seat_number": "not-an-int",
	})
	if err == nil {
		t.Fatal("expected encoding error from non-int32 seat_number")
	}
	if !strings.Contains(err.Error(), "seat_number") {
		t.Errorf("err = %v; want mention of seat_number", err)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 0 {
		t.Errorf("rows = %d; want 0 (failed publish must not tee)", len(rows))
	}
}

// TestOutboxPublisher_PublishCustom_JSONFallbackMarshalErrorSurfaces —
// course.content_composed.v1 has NO binary encoder, so the JSON fallback
// runs; a non-JSON-marshalable payload value must surface that error.
func TestOutboxPublisher_PublishCustom_JSONFallbackMarshalErrorSurfaces(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	_, err := pub.PublishCustom("chora.delivery.course.content_composed.v1", extraTenant, extraGCID, map[string]any{
		"course_id": "c-1", "broken": make(chan int),
	})
	if err == nil {
		t.Fatal("expected JSON fallback marshal error")
	}
}

// TestOutboxPublisher_ForwardingPublishers_SurfaceInnerError — every typed
// forwarder with an empty tenant must surface the inner publisher error via
// its early `return ev, err` (the 3-statement branch baseline tests miss).
func TestOutboxPublisher_ForwardingPublishers_SurfaceInnerError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		emit func(p *outbox.TransactionalOutboxPublisher) error
	}{
		{name: "CourseCreated", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishCourseCreated(events.CourseCreated{})
			return err
		}},
		{name: "CoursePublished", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishCoursePublished(events.CoursePublished{})
			return err
		}},
		{name: "EnrollmentCreated", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishEnrollmentCreated(events.EnrollmentCreated{})
			return err
		}},
		{name: "EnrollmentCancelled", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishEnrollmentCancelled(events.EnrollmentCancelled{})
			return err
		}},
		{name: "EnrollmentCompleted", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishEnrollmentCompleted(events.EnrollmentCompleted{})
			return err
		}},
		{name: "BookingConfirmed", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishBookingConfirmed(events.BookingConfirmed{})
			return err
		}},
		{name: "CertificationIssued", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishCertificationIssued(events.CertificationIssued{})
			return err
		}},
		{name: "TestSetCreated", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishTestSetCreated(events.TestSetCreated{})
			return err
		}},
		{name: "TestSetQuestionAdded", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishTestSetQuestionAdded(events.TestSetQuestionAdded{})
			return err
		}},
		{name: "TestSetQuestionUpdated", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishTestSetQuestionUpdated(events.TestSetQuestionUpdated{})
			return err
		}},
		{name: "TestSetQuestionRemoved", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishTestSetQuestionRemoved(events.TestSetQuestionRemoved{})
			return err
		}},
		{name: "TestSetPublished", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishTestSetPublished(events.TestSetPublished{})
			return err
		}},
		{name: "LiveQuizScoreAwarded", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishLiveQuizScoreAwarded(events.LiveQuizScoreAwarded{})
			return err
		}},
		{name: "LiveQuizSessionStarted", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishLiveQuizSessionStarted(events.LiveQuizSessionStarted{})
			return err
		}},
		{name: "LiveQuizSessionEnded", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishLiveQuizSessionEnded(events.LiveQuizSessionEnded{})
			return err
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pub := newOutboxPublisher(outbox.NewInMemoryStore())
			if err := tc.emit(pub); err == nil {
				t.Fatalf("%s: expected inner publisher error for empty tenant", tc.name)
			}
		})
	}
}

// TestOutboxPublisher_ForwardingPublishers_SurfaceTeeError — with a store
// whose Insert always fails, every forwarding publisher (typed + custom +
// application) must surface that error after the inner publish succeeded.
func TestOutboxPublisher_ForwardingPublishers_SurfaceTeeError(t *testing.T) {
	t.Parallel()
	app, err := application.NewApplication(application.NewApplicationInput{
		TenantID: extraTenant, CourseID: "app-1", GCID: extraGCID,
	})
	if err != nil {
		t.Fatalf("NewApplication: %v", err)
	}
	if err := app.Transition(application.StatusSubmitted); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	cases := []struct {
		name string
		emit func(p *outbox.TransactionalOutboxPublisher) error
	}{
		{name: "CourseCreated", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishCourseCreated(events.CourseCreated{TenantID: extraTenant, GCID: extraGCID, CourseID: "c-1", Title: "T", InstructorGCID: extraGCID})
			return err
		}},
		{name: "CoursePublished", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishCoursePublished(events.CoursePublished{TenantID: extraTenant, GCID: extraGCID, CourseID: "c-1", Title: "T"})
			return err
		}},
		{name: "EnrollmentCreated", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishEnrollmentCreated(events.EnrollmentCreated{TenantID: extraTenant, GCID: extraGCID, EnrollmentID: "e-1", CourseID: "c-1", LearnerGCID: extraGCID})
			return err
		}},
		{name: "EnrollmentCancelled", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishEnrollmentCancelled(events.EnrollmentCancelled{TenantID: extraTenant, GCID: extraGCID, EnrollmentID: "e-1", CourseID: "c-1", LearnerGCID: extraGCID})
			return err
		}},
		{name: "EnrollmentCompleted", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishEnrollmentCompleted(events.EnrollmentCompleted{TenantID: extraTenant, GCID: extraGCID, EnrollmentID: "e-1", CourseID: "c-1", LearnerGCID: extraGCID, Passed: true})
			return err
		}},
		{name: "BookingConfirmed", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishBookingConfirmed(events.BookingConfirmed{TenantID: extraTenant, GCID: extraGCID, BookingID: "b-1", CourseID: "c-1", LearnerGCID: extraGCID})
			return err
		}},
		{name: "CertificationIssued", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishCertificationIssued(events.CertificationIssued{TenantID: extraTenant, GCID: extraGCID, CertificationID: "cert-1", CourseID: "c-1", LearnerGCID: extraGCID})
			return err
		}},
		{name: "TestSetCreated", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishTestSetCreated(events.TestSetCreated{TenantID: extraTenant, GCID: extraGCID, TestSetID: "ts-1", AuthorGCID: extraGCID})
			return err
		}},
		{name: "TestSetQuestionAdded", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishTestSetQuestionAdded(events.TestSetQuestionAdded{TenantID: extraTenant, GCID: extraGCID, TestSetID: "ts-1", TestSetQuestionID: "q-1"})
			return err
		}},
		{name: "TestSetQuestionUpdated", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishTestSetQuestionUpdated(events.TestSetQuestionUpdated{TenantID: extraTenant, GCID: extraGCID, TestSetID: "ts-1", TestSetQuestionID: "q-1"})
			return err
		}},
		{name: "TestSetQuestionRemoved", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishTestSetQuestionRemoved(events.TestSetQuestionRemoved{TenantID: extraTenant, GCID: extraGCID, TestSetID: "ts-1", TestSetQuestionID: "q-1"})
			return err
		}},
		{name: "TestSetPublished", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishTestSetPublished(events.TestSetPublished{TenantID: extraTenant, GCID: extraGCID, TestSetID: "ts-1", AuthorGCID: extraGCID})
			return err
		}},
		{name: "LiveQuizScoreAwarded", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishLiveQuizScoreAwarded(events.LiveQuizScoreAwarded{TenantID: extraTenant, GCID: extraGCID, SessionID: "s-1", LiveQuizID: "lq-1", QuestionID: "q-1"})
			return err
		}},
		{name: "LiveQuizSessionStarted", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishLiveQuizSessionStarted(events.LiveQuizSessionStarted{TenantID: extraTenant, GCID: extraGCID, SessionID: "s-1", LiveQuizID: "lq-1"})
			return err
		}},
		{name: "LiveQuizSessionEnded", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishLiveQuizSessionEnded(events.LiveQuizSessionEnded{TenantID: extraTenant, GCID: extraGCID, SessionID: "s-1", LiveQuizID: "lq-1"})
			return err
		}},
		{name: "ApplicationStateChanged", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishApplicationStateChanged(app, "")
			return err
		}},
		{name: "PublishCustom", emit: func(p *outbox.TransactionalOutboxPublisher) error {
			_, err := p.PublishCustom("chora.delivery.assessment.created.v1", extraTenant, extraGCID, map[string]any{"assessment_id": "a-1"})
			return err
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pub := newOutboxPublisher(&failInsertStore{outbox.NewInMemoryStore()})
			err := tc.emit(pub)
			if err == nil {
				t.Fatalf("%s: expected tee error from failing store, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "insert failed") {
				t.Errorf("%s: err = %v; want store insert failure", tc.name, err)
			}
		})
	}
}

// TestOutboxPublisher_NilApplication_AfterInnerSucceeds — the outbox-level
// nil check (publisher.go) sits AFTER the inner publisher call; this exercises
// it with an inner publisher that succeeds for a nil application.
func TestOutboxPublisher_NilApplication_AfterInnerSucceeds(t *testing.T) {
	t.Parallel()
	pub := outbox.NewTransactionalPublisher(outbox.PublisherConfig{
		Inner: &noCustomPublisher{project: "chora-489812", service: "chora-delivery"},
		Store: outbox.NewInMemoryStore(),
	})
	_, err := pub.PublishApplicationStateChanged(nil, "")
	if err == nil {
		t.Fatal("expected error for nil application")
	}
	if !strings.Contains(err.Error(), "nil application") {
		t.Errorf("err = %v; want contains 'nil application'", err)
	}
}
