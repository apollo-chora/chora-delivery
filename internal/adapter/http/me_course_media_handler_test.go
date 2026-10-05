package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// enrolledGCID matches the gcid the shared do() helper stamps so a learner can
// be Register()ed in the enrolment store for the 200 paths.
const enrolledGCID = "00000000-0000-7000-8000-0000000000ac"

const mediaURLsPath = "/v1/me/courses/" + crs + "/content/media-urls"

// gsVideoRef is a delivery-owned uploaded-video gs:// ref.
const gsVideoRef = "gs://chora-delivery-course-media-dev/tenants/" + tnt + "/courses/" + crs + "/v.mp4"

// newMeMediaHandler wires the ADR-185 handler with an in-mem enrolment store
// (optionally pre-enrolling the do() gcid in crs) + a course-content service
// seeded by `seed`. resolver is the signer port (nil exercises the 503 path).
func newMeMediaHandler(enrolled bool, resolver MediaURLResolver, seed func(svc *cc.Service)) http.HandlerFunc {
	return newMeMediaHandlerWithAssessments(enrolled, resolver, nil, seed)
}

// newMeMediaHandlerWithAssessments is newMeMediaHandler plus the CHO-2351
// learner-assessment lister. A nil lister means "assessment refs stay raw",
// which is the pre-CHO-2351 behaviour the media-only tests still assert.
func newMeMediaHandlerWithAssessments(
	enrolled bool,
	resolver MediaURLResolver,
	assessments LearnerAssessmentLister,
	seed func(svc *cc.Service),
) http.HandlerFunc {
	svc := cc.NewService(inmem.NewCourseContentRepo(), noopPub{})
	if seed != nil {
		seed(svc)
	}
	store := domain.NewInMemEnrollmentStore()
	if enrolled {
		_, _ = store.Register(context.Background(), tnt, crs, enrolledGCID)
	}
	return meCourseMediaHandler(store, &CourseContentDeps{Svc: svc, MediaResolver: resolver}, assessments)
}

func seedItem(kind, ref, title string) func(*cc.Service) {
	return func(svc *cc.Service) {
		if _, err := svc.AddItem(context.Background(), tnt, crs, enrolledGCID, cc.AddItemParams{
			Kind: cc.Kind(kind), Ref: ref, Title: title,
		}); err != nil {
			panic(err)
		}
	}
}

func TestMeCourseMedia_403WhenNotEnrolled(t *testing.T) {
	h := newMeMediaHandler(false, fakeResolver{}, seedItem("video", gsVideoRef, "Lecture"))
	rec := do(h, http.MethodGet, mediaURLsPath, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("not enrolled: want 403, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestMeCourseMedia_200ResolvedWhenEnrolled(t *testing.T) {
	// One gs:// video (resolves) + one atom (non-gs:// → omitted from the map).
	seed := func(svc *cc.Service) {
		seedItem("video", gsVideoRef, "Lecture")(svc)
		seedItem("atom", atomRef, "Intro")(svc)
	}
	h := newMeMediaHandler(true, fakeResolver{}, seed)
	rec := do(h, http.MethodGet, mediaURLsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("enrolled: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Resolved  map[string]string `json:"resolved"`
		ExpiresAt string            `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Resolved) != 1 {
		t.Fatalf("want exactly 1 resolved (the video; atom omitted), got %d: %v", len(got.Resolved), got.Resolved)
	}
	// The map is keyed by item_id, and the value is the signed URL for the gs:// ref.
	for itemID, signed := range got.Resolved {
		if itemID == "" {
			t.Fatalf("resolved key should be a content item_id, got empty")
		}
		if signed != "https://signed-get.example/"+gsVideoRef {
			t.Fatalf("resolved value should be the signed URL for the gs:// ref, got %q", signed)
		}
	}
	if got.ExpiresAt == "" {
		t.Fatalf("expires_at should be set when media resolved")
	}
}

func TestMeCourseMedia_200EmptyWhenNoMedia(t *testing.T) {
	// Enrolled, but the only item is an atom (no gs:// media) → empty map.
	h := newMeMediaHandler(true, fakeResolver{}, seedItem("atom", atomRef, "Intro"))
	rec := do(h, http.MethodGet, mediaURLsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("no media: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Resolved map[string]string `json:"resolved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Resolved) != 0 {
		t.Fatalf("want empty resolved map, got %v", got.Resolved)
	}
}

func TestMeCourseMedia_200EmptyWhenNoCurriculum(t *testing.T) {
	// Enrolled, but the course has no content aggregate yet (cc.ErrNotFound) →
	// 200 with an empty map, never a hard error.
	h := newMeMediaHandler(true, fakeResolver{}, nil)
	rec := do(h, http.MethodGet, mediaURLsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("no curriculum: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Resolved map[string]string `json:"resolved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Resolved) != 0 {
		t.Fatalf("want empty resolved map, got %v", got.Resolved)
	}
}

func TestMeCourseMedia_401WhenNoGcid(t *testing.T) {
	h := newMeMediaHandler(true, fakeResolver{}, seedItem("video", gsVideoRef, "Lecture"))
	req := httptest.NewRequest(http.MethodGet, mediaURLsPath, nil)
	req.Header.Set("X-Tenant-Id", tnt) // no gcid header
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing gcid: want 401, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestMeCourseMedia_503WhenResolverNil(t *testing.T) {
	// Enrolled (authz passes) but the signer/resolver is unwired → fail-loud 503.
	h := newMeMediaHandler(true, nil, seedItem("video", gsVideoRef, "Lecture"))
	rec := do(h, http.MethodGet, mediaURLsPath, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("resolver nil: want 503, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestMeCourseMedia_404OnBadPath(t *testing.T) {
	h := newMeMediaHandler(true, fakeResolver{}, seedItem("video", gsVideoRef, "Lecture"))
	// Missing the trailing /media-urls segment.
	rec := do(h, http.MethodGet, "/v1/me/courses/"+crs+"/content", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bad path: want 404, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestMeCourseMedia_405OnNonGet(t *testing.T) {
	h := newMeMediaHandler(true, fakeResolver{}, seedItem("video", gsVideoRef, "Lecture"))
	rec := do(h, http.MethodPost, mediaURLsPath, "{}")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: want 405, got %d (%s)", rec.Code, rec.Body.String())
	}
}
