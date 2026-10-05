// offering_curriculum_handler_test.go — handler-level verification of the
// R+ W2.D offering-nested Curriculum surface (read-only attached-course
// outline): GET /api/v1/offerings/{id}/curriculum.
//
// Intra-chora_delivery (Offering + CJ#2 Course + CourseContent share the DB per
// ddd-enforcement #3) — NO cross-DB query, NO new aggregate, NO migration.
// Mirrors offering_assessments_handler_test.go's in-mem harness + reqWithHeaders.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	curOfferingID = "01985e7f-2222-7abc-8def-0000000000d1"
	curCourseA    = "01985e7f-2222-7abc-8def-0000000000e1"
	curCourseB    = "01985e7f-2222-7abc-8def-0000000000e2"
	curAtomRef    = "01985e7f-2222-7abc-8def-0000000000f1"
)

// noopCurriculumPub satisfies cc.Publisher for the course-content Service. Get
// never publishes; the AddItem-seeding helper does, so a no-op keeps seeding
// side-effect-free (the read-path under test never invokes it).
type noopCurriculumPub struct{}

func (noopCurriculumPub) PublishContentComposed(context.Context, string, *cc.CourseContent) error {
	return nil
}

// newOfferingCurriculumTestServer wires Offerings + CourseCJ2 (Courses + Content)
// with in-mem repos so the curriculum route resolves DB-free.
func newOfferingCurriculumTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *delivery.InMemCourseCJ2Store, *cc.Service) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	contentSvc := cc.NewService(inmem.NewCourseContentRepo(), noopCurriculumPub{})
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{
			Courses: courseStore,
			Content: &httpapi.CourseContentDeps{Svc: contentSvc},
		},
	})
	return srv, oRepo, courseStore, contentSvc
}

// seedCurriculumOffering stores a DRAFT offering over the given courses (order
// preserved) under the standard tenant.
func seedCurriculumOffering(t *testing.T, oRepo *inmem.OfferingRepo, id string, courseIDs ...string) {
	t.Helper()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    courseIDs,
		DeliveryType: delivery.DeliveryTypeGraduate,
		Label:        "W2.D Curriculum Run",
		Capacity:     0,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = id
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save offering: %v", err)
	}
}

// seedCJ2Course stores a CJ#2 Course shell carrying a title.
func seedCJ2Course(t *testing.T, store *delivery.InMemCourseCJ2Store, id, title string) {
	t.Helper()
	c, err := delivery.NewCJ2Course(delivery.NewCJ2CourseInput{
		TenantID:   tenantID,
		AuthorGCID: instructor,
		Title:      title,
		TestSetIDs: []string{testSetID},
	})
	if err != nil {
		t.Fatalf("NewCJ2Course: %v", err)
	}
	c.ID = id
	if err := store.Save(context.Background(), c); err != nil {
		t.Fatalf("Save course: %v", err)
	}
}

// seedCourseContentItem appends one typed content item to a course's curriculum.
func seedCourseContentItem(t *testing.T, svc *cc.Service, courseID string, kind cc.Kind, ref, title string) {
	t.Helper()
	if _, err := svc.AddItem(context.Background(), tenantID, courseID, instructor, cc.AddItemParams{
		Kind: kind, Ref: ref, Title: title,
	}); err != nil {
		t.Fatalf("AddItem(%s): %v", title, err)
	}
}

// curriculumItem is the wire shape of one content-outline row.
type curriculumItem struct {
	ItemID   string `json:"item_id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Position int    `json:"position"`
}

// curriculumCourse is the wire shape of one attached course + its outline.
type curriculumCourse struct {
	ID    string           `json:"id"`
	Title string           `json:"title"`
	Items []curriculumItem `json:"items"`
}

// curriculumResp is the wire envelope returned by GET .../curriculum.
type curriculumResp struct {
	Courses []curriculumCourse `json:"courses"`
}

func getCurriculum(t *testing.T, srv http.Handler, offeringID, role string) (*httptest.ResponseRecorder, curriculumResp) {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+offeringID+"/curriculum", nil, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp curriculumResp
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/curriculum
// -----------------------------------------------------------------------------

func TestOfferingCurriculum_Get_ReturnsCoursesWithOrderedOutline(t *testing.T) {
	srv, oRepo, courseStore, contentSvc := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Calculus I")
	seedCourseContentItem(t, contentSvc, curCourseA, cc.KindAtom, curAtomRef, "Limits")
	seedCourseContentItem(t, contentSvc, curCourseA, cc.KindYouTube, "https://youtu.be/deriv", "Derivatives Intro")

	w, resp := getCurriculum(t, srv, curOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 1 {
		t.Fatalf("courses: want 1, got %d body=%s", len(resp.Courses), w.Body.String())
	}
	got := resp.Courses[0]
	if got.ID != curCourseA {
		t.Fatalf("course id: want %s, got %s", curCourseA, got.ID)
	}
	if got.Title != "Calculus I" {
		t.Fatalf("course title: want Calculus I, got %q", got.Title)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items: want 2, got %d body=%s", len(got.Items), w.Body.String())
	}
	if got.Items[0].Kind != "atom" || got.Items[0].Title != "Limits" || got.Items[0].Position != 0 {
		t.Fatalf("item0: want atom/Limits/0, got %s/%s/%d", got.Items[0].Kind, got.Items[0].Title, got.Items[0].Position)
	}
	if got.Items[1].Kind != "youtube" || got.Items[1].Title != "Derivatives Intro" || got.Items[1].Position != 1 {
		t.Fatalf("item1: want youtube/Derivatives Intro/1, got %s/%s/%d", got.Items[1].Kind, got.Items[1].Title, got.Items[1].Position)
	}
}

func TestOfferingCurriculum_Get_EmptyOutlineWhenNoCurriculum(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Empty Course")
	// No content seeded → the course-content service returns cc.ErrNotFound,
	// which is an EMPTY outline (200, items: []), NOT an error.

	w, resp := getCurriculum(t, srv, curOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 1 {
		t.Fatalf("courses: want 1, got %d", len(resp.Courses))
	}
	if len(resp.Courses[0].Items) != 0 {
		t.Fatalf("items: want empty, got %#v", resp.Courses[0].Items)
	}
}

func TestOfferingCurriculum_Get_PreservesCourseOrder(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA, curCourseB)
	seedCJ2Course(t, courseStore, curCourseA, "First")
	seedCJ2Course(t, courseStore, curCourseB, "Second")

	w, resp := getCurriculum(t, srv, curOfferingID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 2 {
		t.Fatalf("courses: want 2, got %d", len(resp.Courses))
	}
	if resp.Courses[0].ID != curCourseA || resp.Courses[1].ID != curCourseB {
		t.Fatalf("order: want [%s,%s], got [%s,%s]", curCourseA, curCourseB, resp.Courses[0].ID, resp.Courses[1].ID)
	}
}

func TestOfferingCurriculum_Get_404WhenOfferingMissing(t *testing.T) {
	srv, _, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCJ2Course(t, courseStore, curCourseA, "Orphan") // offering NOT seeded

	w, _ := getCurriculum(t, srv, curOfferingID, "instructor")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingCurriculum_Get_403WithoutRole(t *testing.T) {
	srv, oRepo, _, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)

	w, _ := getCurriculum(t, srv, curOfferingID, "") // no instructor/admin role
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

// PUT is unsupported on the curriculum surface (GET reads, POST authors — S1);
// an unsupported verb is 405.
func TestOfferingCurriculum_Put_405(t *testing.T) {
	srv, oRepo, _, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)

	r := reqWithHeaders(http.MethodPut, "/api/v1/offerings/"+curOfferingID+"/curriculum", []byte("{}"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d body=%s", w.Code, w.Body.String())
	}
}
