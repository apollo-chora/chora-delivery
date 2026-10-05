// offering_publish_handler_test.go — handler-level verification of the R+
// offering-nested Publish / Catalog-handoff surface:
//
//	GET   /api/v1/offerings/{id}/publish
//	PATCH /api/v1/offerings/{id}/publish
//
// PATCH flips the offering's catalogue-LISTED attached courses to public +
// emits the LIVE chora.delivery.course.published.v1 once per newly-published
// course (idempotent). Unlisted courses are skipped + reported listed:false.
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
	opubOfferingID = "01985e7f-8888-7abc-8def-0000000000f1"
	opubCourseA    = "01985e7f-8888-7abc-8def-000000000a01" // catalogue: private
	opubCourseB    = "01985e7f-8888-7abc-8def-000000000a02" // catalogue: public
	opubCourseC    = "01985e7f-8888-7abc-8def-000000000a03" // NOT in catalogue
)

type opubCourse struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Visibility string `json:"visibility"`
	Published  bool   `json:"published"`
	Listed     bool   `json:"listed"`
}

type opubResp struct {
	Courses        []opubCourse `json:"courses"`
	PublishedCount int          `json:"published_count"`
}

func newOfferingPublishTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *delivery.InMemCatalogue, *events.InMemoryPublisher) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	cat := delivery.NewInMemCatalogue()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		Catalogue: cat,
		Publisher: pub,
	})
	return srv, oRepo, cat, pub
}

func seedPublicCourse(t *testing.T, cat *delivery.InMemCatalogue, id, title string, vis delivery.Visibility) {
	t.Helper()
	pc := &delivery.PublicCourse{
		ID:         id,
		TenantID:   tenantID,
		Title:      title,
		Visibility: vis,
		Public:     vis == delivery.VisibilityPublic,
	}
	if err := cat.Save(context.Background(), pc); err != nil {
		t.Fatalf("seed PublicCourse: %v", err)
	}
}

func getPublish(t *testing.T, srv http.Handler, offeringID, role string) (*httptest.ResponseRecorder, opubResp) {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+offeringID+"/publish", nil, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp opubResp
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

func patchPublish(t *testing.T, srv http.Handler, offeringID, role string) (*httptest.ResponseRecorder, opubResp) {
	t.Helper()
	r := reqWithHeaders(http.MethodPatch, "/api/v1/offerings/"+offeringID+"/publish", []byte("{}"), instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp opubResp
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

func countCoursePublished(pub *events.InMemoryPublisher) int {
	n := 0
	for _, ev := range pub.History() {
		if ev.Topic == events.TopicCoursePublished {
			n++
		}
	}
	return n
}

// -----------------------------------------------------------------------------

func TestOfferingPublish_Get_ReportsPerCourseStatus(t *testing.T) {
	srv, oRepo, cat, _ := newOfferingPublishTestServer(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA, opubCourseB, opubCourseC)
	seedPublicCourse(t, cat, opubCourseA, "Course A", delivery.VisibilityPrivate)
	seedPublicCourse(t, cat, opubCourseB, "Course B", delivery.VisibilityPublic)
	// courseC intentionally not in the catalogue.

	w, resp := getPublish(t, srv, opubOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 3 {
		t.Fatalf("courses: want 3, got %d body=%s", len(resp.Courses), w.Body.String())
	}
	byID := map[string]opubCourse{}
	for _, c := range resp.Courses {
		byID[c.ID] = c
	}
	if a := byID[opubCourseA]; !a.Listed || a.Published || a.Visibility != "private" {
		t.Fatalf("course A: want listed/private/unpublished, got %+v", a)
	}
	if b := byID[opubCourseB]; !b.Listed || !b.Published || b.Visibility != "public" {
		t.Fatalf("course B: want listed/public/published, got %+v", b)
	}
	if c := byID[opubCourseC]; c.Listed || c.Published {
		t.Fatalf("course C: want NOT listed, got %+v", c)
	}
}

func TestOfferingPublish_Patch_PublishesListedAndEmitsOnce(t *testing.T) {
	srv, oRepo, cat, pub := newOfferingPublishTestServer(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA, opubCourseB, opubCourseC)
	seedPublicCourse(t, cat, opubCourseA, "Course A", delivery.VisibilityPrivate)
	seedPublicCourse(t, cat, opubCourseB, "Course B", delivery.VisibilityPublic) // already public
	// courseC not listed.

	w, resp := patchPublish(t, srv, opubOfferingID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	// Only A newly transitions into public → 1 publish, 1 event. B was already
	// public (no signal, no dup). C is not listed (skipped).
	if resp.PublishedCount != 1 {
		t.Fatalf("published_count: want 1, got %d", resp.PublishedCount)
	}
	if got := countCoursePublished(pub); got != 1 {
		t.Fatalf("course.published.v1 emits: want 1, got %d", got)
	}
	// A is now public.
	_, after := getPublish(t, srv, opubOfferingID, "instructor")
	for _, c := range after.Courses {
		if c.ID == opubCourseA && (!c.Published || c.Visibility != "public") {
			t.Fatalf("course A not public after publish: %+v", c)
		}
	}
}

func TestOfferingPublish_Patch_IdempotentNoDupEvent(t *testing.T) {
	srv, oRepo, cat, pub := newOfferingPublishTestServer(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	seedPublicCourse(t, cat, opubCourseA, "Course A", delivery.VisibilityPrivate)

	patchPublish(t, srv, opubOfferingID, "training-admin")            // first: publishes A
	w, resp := patchPublish(t, srv, opubOfferingID, "training-admin") // second: no-op
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if resp.PublishedCount != 0 {
		t.Fatalf("second publish must be a no-op, published_count=%d", resp.PublishedCount)
	}
	if got := countCoursePublished(pub); got != 1 {
		t.Fatalf("emits after two publishes: want 1 (no dup), got %d", got)
	}
}

func TestOfferingPublish_Get_404WhenMissing(t *testing.T) {
	srv, _, _, _ := newOfferingPublishTestServer(t)
	w, _ := getPublish(t, srv, opubOfferingID, "instructor")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", w.Code)
	}
}

func TestOfferingPublish_403WithoutRole(t *testing.T) {
	srv, oRepo, _, _ := newOfferingPublishTestServer(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	w, _ := getPublish(t, srv, opubOfferingID, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("GET status: want 403, got %d", w.Code)
	}
	wp, _ := patchPublish(t, srv, opubOfferingID, "")
	if wp.Code != http.StatusForbidden {
		t.Fatalf("PATCH status: want 403, got %d", wp.Code)
	}
}

func TestOfferingPublish_405OnPost(t *testing.T) {
	srv, oRepo, _, _ := newOfferingPublishTestServer(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+opubOfferingID+"/publish", []byte("{}"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// B2.2 — cross-tenant publish requires a HIGHER role bar than in-tenant admin.
// A bare instructor may build/curate but NOT expose a course cross-tenant.
// -----------------------------------------------------------------------------

func TestOfferingPublish_Patch_403ForBareInstructor(t *testing.T) {
	srv, oRepo, cat, pub := newOfferingPublishTestServer(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	seedPublicCourse(t, cat, opubCourseA, "Course A", delivery.VisibilityPrivate)
	w, _ := patchPublish(t, srv, opubOfferingID, "instructor") // bare instructor
	if w.Code != http.StatusForbidden {
		t.Fatalf("PATCH publish by instructor: want 403, got %d body=%s", w.Code, w.Body.String())
	}
	if got := countCoursePublished(pub); got != 0 {
		t.Fatalf("no course.published event may fire on a 403, got %d", got)
	}
}

func TestOfferingPublish_Patch_AdminAllowed(t *testing.T) {
	srv, oRepo, cat, _ := newOfferingPublishTestServer(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	seedPublicCourse(t, cat, opubCourseA, "Course A", delivery.VisibilityPrivate)
	w, resp := patchPublish(t, srv, opubOfferingID, "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH publish by admin: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if resp.PublishedCount != 1 {
		t.Fatalf("admin publish: want published_count 1, got %d", resp.PublishedCount)
	}
}

// -----------------------------------------------------------------------------
// B2.1 — a course may only be flipped cross-tenant PUBLIC when its CJ#2
// authoring lifecycle is PUBLISHED (closes the state/public decoupling). Gated
// only when the CJ#2 course port is wired (production always wires it).
// -----------------------------------------------------------------------------

// newOfferingPublishTestServerCJ2 additionally wires the CJ#2 course port so the
// state gate is enforceable.
func newOfferingPublishTestServerCJ2(t *testing.T) (http.Handler, *inmem.OfferingRepo, *delivery.InMemCatalogue, *events.InMemoryPublisher, *delivery.InMemCourseCJ2Store) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	cat := delivery.NewInMemCatalogue()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	courseStore := delivery.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		Catalogue: cat,
		Publisher: pub,
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore},
	})
	return srv, oRepo, cat, pub, courseStore
}

// seedCJ2CourseState stores a CJ#2 course shell with an explicit state.
func seedCJ2CourseState(t *testing.T, store *delivery.InMemCourseCJ2Store, id, title string, state delivery.CourseState) {
	t.Helper()
	if err := store.Save(context.Background(), &delivery.Course{
		ID: id, TenantID: tenantID, State: state, AuthorGCID: instructor, Title: title,
	}); err != nil {
		t.Fatalf("seed CJ2 course: %v", err)
	}
}

func TestOfferingPublish_Patch_RefusesDraftCourse(t *testing.T) {
	srv, oRepo, cat, pub, courseStore := newOfferingPublishTestServerCJ2(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	seedPublicCourse(t, cat, opubCourseA, "Course A", delivery.VisibilityPrivate) // would transition
	seedCJ2CourseState(t, courseStore, opubCourseA, "Course A", delivery.CourseStateDraft)

	w, resp := patchPublish(t, srv, opubOfferingID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if resp.PublishedCount != 0 {
		t.Fatalf("a DRAFT course must NOT be published cross-tenant, published_count=%d", resp.PublishedCount)
	}
	if got := countCoursePublished(pub); got != 0 {
		t.Fatalf("no course.published event for a draft course, got %d", got)
	}
	// still not public
	_, after := getPublish(t, srv, opubOfferingID, "training-admin")
	for _, c := range after.Courses {
		if c.ID == opubCourseA && c.Published {
			t.Fatalf("draft course must remain unpublished, got %+v", c)
		}
	}
}

func TestOfferingPublish_Patch_AllowsPublishedCourse(t *testing.T) {
	srv, oRepo, cat, pub, courseStore := newOfferingPublishTestServerCJ2(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	seedPublicCourse(t, cat, opubCourseA, "Course A", delivery.VisibilityPrivate)
	seedCJ2CourseState(t, courseStore, opubCourseA, "Course A", delivery.CourseStatePublished)

	w, resp := patchPublish(t, srv, opubOfferingID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if resp.PublishedCount != 1 {
		t.Fatalf("a PUBLISHED course must publish, published_count=%d", resp.PublishedCount)
	}
	if got := countCoursePublished(pub); got != 1 {
		t.Fatalf("course.published event: want 1, got %d", got)
	}
}

func TestOfferingPublish_503WhenCatalogueUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // Catalogue nil
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	w, _ := getPublish(t, srv, opubOfferingID, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503, got %d body=%s", w.Code, w.Body.String())
	}
}
