// offering_roster_bulk_test.go - R+ ATOMIC bulk roster enrol.
// POST /api/v1/offerings/{id}/roster/bulk - enrol many learners (by GCID) into
// one of the offering's attached courses in a single all-or-nothing request.
//
// Reuses the S3 single-enrol harness (seedEnrollOffering / enrollOfferingID /
// enrollCourseA / enrollLearner1..2 / reqWithHeaders) from
// offering_roster_enroll_test.go + the event-assertion helper from
// offering_roster_enroll_event_test.go.
//
// The in-mem harness exercises the fallback path (per-learner Register +
// after-commit publish). A separate test drives the pg-shaped TRANSACTIONAL
// branch via a fake bulkEnroller + fake EnrollmentTxTee to prove the handler
// routes NEW enrolments through the tx tee and does NOT double-publish.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	bulkLearnerA = "00000000-0000-7000-8000-000000000b01"
	bulkLearnerB = "00000000-0000-7000-8000-000000000b02"
	bulkLearnerC = "00000000-0000-7000-8000-000000000b03"
)

func newBulkEnrollTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, delivery.EnrollmentPort, *events.InMemoryPublisher) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	enroll := delivery.NewInMemEnrollmentStore()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: enroll, Publisher: pub})
	return srv, oRepo, enroll, pub
}

func postBulk(t *testing.T, srv http.Handler, offeringID, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringID+"/roster/bulk", b, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// -----------------------------------------------------------------------------
// Happy path - N fresh learners inserted, N events emitted (fallback path)
// -----------------------------------------------------------------------------

func TestOfferingRosterBulk_InsertsAllAndEmitsPerLearner(t *testing.T) {
	srv, oRepo, enroll, pub := newBulkEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0) // unbounded

	w := postBulk(t, srv, enrollOfferingID, "instructor", map[string]any{
		"course_id": enrollCourseA,
		"gcids":     []string{bulkLearnerA, bulkLearnerB, bulkLearnerC},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if n, _ := got["inserted_count"].(float64); int(n) != 3 {
		t.Fatalf("inserted_count: want 3, got %v", got["inserted_count"])
	}
	if ids, _ := got["enrollment_ids"].([]any); len(ids) != 3 {
		t.Fatalf("enrollment_ids: want 3, got %v", got["enrollment_ids"])
	}
	for _, g := range []string{bulkLearnerA, bulkLearnerB, bulkLearnerC} {
		if _, ok, _ := enroll.GetByCourseAndGCID(context.Background(), tenantID, enrollCourseA, g); !ok {
			t.Fatalf("learner %s not persisted", g)
		}
	}
	if got := enrollmentCreatedEvents(pub); len(got) != 3 {
		t.Fatalf("expected 3 %s events, got %d", events.TopicEnrollmentCreated, len(got))
	}
}

// -----------------------------------------------------------------------------
// Capacity 409 - a batch whose NEW enrolments would exceed the cap is rejected
// wholesale; nothing is persisted and no event is emitted (all-or-nothing).
// -----------------------------------------------------------------------------

func TestOfferingRosterBulk_409WhenBatchExceedsCapacity(t *testing.T) {
	srv, oRepo, enroll, pub := newBulkEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 2) // cap 2

	w := postBulk(t, srv, enrollOfferingID, "instructor", map[string]any{
		"course_id": enrollCourseA,
		"gcids":     []string{bulkLearnerA, bulkLearnerB, bulkLearnerC}, // 3 new > 2
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d body=%s", w.Code, w.Body.String())
	}
	for _, g := range []string{bulkLearnerA, bulkLearnerB, bulkLearnerC} {
		if _, ok, _ := enroll.GetByCourseAndGCID(context.Background(), tenantID, enrollCourseA, g); ok {
			t.Fatalf("no learner may be enrolled on a rejected batch; %s was", g)
		}
	}
	if got := enrollmentCreatedEvents(pub); len(got) != 0 {
		t.Fatalf("rejected batch must emit nothing, got %d", len(got))
	}
}

// A partially-new batch counts only the NEW learners against remaining capacity.
func TestOfferingRosterBulk_CapacityCountsOnlyNewLearners(t *testing.T) {
	srv, oRepo, enroll, _ := newBulkEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 3) // cap 3
	// Pre-enrol learner A (1 seat used).
	if _, err := enroll.Register(context.Background(), tenantID, enrollCourseA, bulkLearnerA); err != nil {
		t.Fatalf("seed enrol: %v", err)
	}
	// Batch {A(existing), B(new), C(new)} → 2 new, 1+2=3 ≤ 3 → OK.
	w := postBulk(t, srv, enrollOfferingID, "instructor", map[string]any{
		"course_id": enrollCourseA,
		"gcids":     []string{bulkLearnerA, bulkLearnerB, bulkLearnerC},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if n, _ := got["inserted_count"].(float64); int(n) != 2 {
		t.Fatalf("inserted_count: want 2 (A already enrolled), got %v", got["inserted_count"])
	}
}

// -----------------------------------------------------------------------------
// Idempotent re-submit - the second identical batch inserts nothing new and
// republishes nothing.
// -----------------------------------------------------------------------------

func TestOfferingRosterBulk_ReSubmitIsIdempotent(t *testing.T) {
	srv, oRepo, _, pub := newBulkEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	body := map[string]any{"course_id": enrollCourseA, "gcids": []string{bulkLearnerA, bulkLearnerB}}

	if w := postBulk(t, srv, enrollOfferingID, "instructor", body); w.Code != http.StatusCreated {
		t.Fatalf("first batch: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	w := postBulk(t, srv, enrollOfferingID, "instructor", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("re-submit: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if n, _ := got["inserted_count"].(float64); int(n) != 0 {
		t.Fatalf("re-submit inserted_count: want 0 (idempotent), got %v", got["inserted_count"])
	}
	if got := enrollmentCreatedEvents(pub); len(got) != 2 {
		t.Fatalf("re-submit must not republish: want 2 total events, got %d", len(got))
	}
}

// -----------------------------------------------------------------------------
// Validation + auth
// -----------------------------------------------------------------------------

func TestOfferingRosterBulk_400WhenCourseNotAttached(t *testing.T) {
	srv, oRepo, _, _ := newBulkEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	w := postBulk(t, srv, enrollOfferingID, "instructor", map[string]any{
		"course_id": enrollCourseX, "gcids": []string{bulkLearnerA},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("course not attached: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRosterBulk_400WhenGCIDsEmpty(t *testing.T) {
	srv, oRepo, _, _ := newBulkEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	w := postBulk(t, srv, enrollOfferingID, "instructor", map[string]any{
		"course_id": enrollCourseA, "gcids": []string{},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty gcids: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRosterBulk_400WhenGCIDBlank(t *testing.T) {
	srv, oRepo, _, _ := newBulkEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	w := postBulk(t, srv, enrollOfferingID, "instructor", map[string]any{
		"course_id": enrollCourseA, "gcids": []string{bulkLearnerA, "   "},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("blank gcid in batch: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRosterBulk_403WithoutRole(t *testing.T) {
	srv, oRepo, _, _ := newBulkEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	w := postBulk(t, srv, enrollOfferingID, "", map[string]any{
		"course_id": enrollCourseA, "gcids": []string{bulkLearnerA},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("no role: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRosterBulk_404WhenOfferingMissing(t *testing.T) {
	srv, _, _, _ := newBulkEnrollTestServer(t)
	w := postBulk(t, srv, enrollOfferingID, "instructor", map[string]any{
		"course_id": enrollCourseA, "gcids": []string{bulkLearnerA},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("offering missing: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// A duplicate learner within one batch is de-duplicated (enrolled once).
func TestOfferingRosterBulk_DeDupesRepeatedLearner(t *testing.T) {
	srv, oRepo, _, pub := newBulkEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	w := postBulk(t, srv, enrollOfferingID, "instructor", map[string]any{
		"course_id": enrollCourseA, "gcids": []string{bulkLearnerA, bulkLearnerA},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if n, _ := got["inserted_count"].(float64); int(n) != 1 {
		t.Fatalf("dup learner: want inserted_count 1, got %v", got["inserted_count"])
	}
	if got := enrollmentCreatedEvents(pub); len(got) != 1 {
		t.Fatalf("dup learner: want 1 event, got %d", len(got))
	}
}

// -----------------------------------------------------------------------------
// Transactional (pg-shaped) branch - the handler routes NEW enrolments through
// the tx tee and does NOT double-publish via deps.Publisher.
// -----------------------------------------------------------------------------

// fakeBulkEnroller embeds the in-mem EnrollmentPort and adds RegisterBulk, so it
// satisfies the handler's bulkEnroller type-assert (the production pg repo path).
type fakeBulkEnroller struct {
	*delivery.InMemEnrollmentStore
	registerBulkCalls int
}

func (f *fakeBulkEnroller) RegisterBulk(
	ctx context.Context, tenantID, courseID string, gcids []string,
	onInserted func(ctx context.Context, exec func(ctx context.Context, query string, args ...any) error, e *delivery.Enrollment) error,
) ([]*delivery.Enrollment, error) {
	f.registerBulkCalls++
	var out []*delivery.Enrollment
	for _, g := range gcids {
		_, existed, _ := f.GetByCourseAndGCID(ctx, tenantID, courseID, g)
		e, err := f.Register(ctx, tenantID, courseID, g)
		if err != nil {
			return nil, err
		}
		if existed {
			continue
		}
		if onInserted != nil {
			noop := func(context.Context, string, ...any) error { return nil }
			if err := onInserted(ctx, noop, e); err != nil {
				return nil, err
			}
		}
		out = append(out, e)
	}
	return out, nil
}

type fakeTxTee struct{ calls int }

func (f *fakeTxTee) RecordEnrollmentCreated(
	_ context.Context, _ func(ctx context.Context, query string, args ...any) error, _ events.EnrollmentCreated,
) error {
	f.calls++
	return nil
}

func TestOfferingRosterBulk_TransactionalBranch_TeesAndDoesNotDoublePublish(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	be := &fakeBulkEnroller{InMemEnrollmentStore: delivery.NewInMemEnrollmentStore()}
	tee := &fakeTxTee{}
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo, Enrollments: be, Publisher: pub, EnrollmentTxTee: tee,
	})
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)

	w := postBulk(t, srv, enrollOfferingID, "instructor", map[string]any{
		"course_id": enrollCourseA,
		"gcids":     []string{bulkLearnerA, bulkLearnerB, bulkLearnerC},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	if be.registerBulkCalls != 1 {
		t.Fatalf("transactional path must call RegisterBulk exactly once; got %d", be.registerBulkCalls)
	}
	if tee.calls != 3 {
		t.Fatalf("tx tee must record 3 enrolment.created rows; got %d", tee.calls)
	}
	// The transactional branch must NOT also publish via deps.Publisher
	// (the tee already durably teed each event inside the tx).
	if got := enrollmentCreatedEvents(pub); len(got) != 0 {
		t.Fatalf("transactional branch must NOT double-publish via deps.Publisher; got %d", len(got))
	}
}
