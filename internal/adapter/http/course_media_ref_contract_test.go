// course_media_ref_contract_test.go — REGRESSION test for the course-media
// scheme contract (reviewer finding on the s3:// atom-media fix).
//
// The s3:// recognition added for atom media must NOT leak into the
// delivery-owned COURSE-MEDIA paths. Course media has its own, separate,
// deliberately gs://-ONLY contract:
//
//   - internal/domain/course_content/course_content.go validateRef accepts
//     gs:// object refs for uploaded video/document items;
//   - internal/adapter/objectmedia/course_media_signer.go
//     ResolveDownloadURLs filters on gs://.
//
// So a course-content item carrying an s3:// ref is outside that contract:
// the read path must NOT collect it for the course-media resolver. These
// tests pin that at the HTTP seam for BOTH course-media read paths:
//
//	GET /api/v1/courses/{course_id}/content                  (instructor editor)
//	GET /v1/me/courses/{course_id}/content/media-urls        (ADR-185 learner)
//
// SCOPE GAP, stated plainly: the resolver is a fake that records the refs it
// is handed. What is proven end to end at the HTTP seam: an s3://
// course-content ref is never submitted to the course-media resolver, while
// a gs:// one still is. The CourseMediaSigner itself is unchanged.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// recordingCourseMediaResolver implements MediaURLResolver and records every
// ref it is handed, so a test can assert what the read path DID and DID NOT
// submit to the course-media resolver.
type recordingCourseMediaResolver struct {
	submittedRefs [][]string
}

func (r *recordingCourseMediaResolver) ResolveDownloadURLs(_ context.Context, _ string, refs []string) map[string]string {
	cp := make([]string, len(refs))
	copy(cp, refs)
	r.submittedRefs = append(r.submittedRefs, cp)
	out := make(map[string]string, len(refs))
	for _, u := range refs {
		out[u] = "https://signed-get.example/" + u
	}
	return out
}

// courseMediaItemRef builds a course-content item ref for the given scheme.
func courseMediaItemRef(scheme string) string {
	return scheme + "chora-delivery-course-media-dev/tenants/" + tnt + "/courses/" + crs + "/v.mp4"
}

// seedCourseContentItem appends a content item straight onto the in-memory
// repo's aggregate, bypassing the domain AddItem validation. The read paths
// under test never re-validate refs — they collect whatever the stored
// aggregate holds — so seeding here lets a test place an out-of-contract
// s3:// ref in the store and prove the read path does not collect it.
func seedCourseContentItem(t *testing.T, repo *inmem.CourseContentRepo, kind cc.Kind, ref, title string) {
	t.Helper()
	ctx := context.Background()
	agg, err := repo.Get(ctx, tnt, crs)
	if err != nil {
		if agg, err = cc.New(cc.NewParams{CourseID: crs, TenantID: tnt}); err != nil {
			t.Fatalf("New aggregate: %v", err)
		}
	}
	agg.Items = append(agg.Items, &cc.ContentItem{
		ItemID:   "seed-" + ref,
		CourseID: crs,
		Kind:     kind,
		Ref:      ref,
		Title:    title,
	})
	if err := repo.Save(ctx, agg); err != nil {
		t.Fatalf("Save aggregate: %v", err)
	}
}

// addCourseMediaItem appends a video item with the given ref through the
// domain service (the supported gs:// path).
func addCourseMediaItem(t *testing.T, svc *cc.Service, ref string) {
	t.Helper()
	if _, err := svc.AddItem(context.Background(), tnt, crs, enrolledGCID, cc.AddItemParams{
		Kind: cc.KindVideo, Ref: ref, Title: "Lecture",
	}); err != nil {
		t.Fatalf("AddItem(%s): %v", ref, err)
	}
}

// REGRESSION — instructor editor read path. GET
// /api/v1/courses/{course_id}/content must submit ONLY the gs:// ref to the
// course-media resolver; an s3:// ref on a course-content item is outside the
// course-media contract and must NOT be collected.
func TestRegression_CourseContentRead_ResolvesGSOnlyNotS3(t *testing.T) {
	gsRef := courseMediaItemRef("gs://")
	s3Ref := courseMediaItemRef("s3://")

	// The s3:// ref is out of the course-media contract, so the domain AddItem
	// validation rejects it; seed both straight into the store to prove the
	// READ path does not collect it.
	repo := inmem.NewCourseContentRepo()
	seedCourseContentItem(t, repo, cc.KindVideo, gsRef, "GS lecture")
	seedCourseContentItem(t, repo, cc.KindVideo, s3Ref, "S3 lecture")

	resolver := &recordingCourseMediaResolver{}
	h := courseContentHandler(&CourseContentDeps{Svc: cc.NewService(repo, noopPub{}), MediaResolver: resolver})

	rec := do(h, http.MethodGet, "/api/v1/courses/"+crs+"/content", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Exactly one batched call, carrying ONLY the gs:// ref.
	if len(resolver.submittedRefs) != 1 {
		t.Fatalf("resolver calls = %d; want 1 batched call: %v", len(resolver.submittedRefs), resolver.submittedRefs)
	}
	submitted := resolver.submittedRefs[0]
	if len(submitted) != 1 || submitted[0] != gsRef {
		t.Fatalf("submitted refs = %v; want [%s] only — an s3:// course-media ref must NOT be collected", submitted, gsRef)
	}

	// The gs:// item is resolved (ref = signed URL, object_ref = durable gs://);
	// the s3:// item is left raw (no object_ref) — fail-visible, never signed.
	var body struct {
		Items []struct {
			Ref       string `json:"ref"`
			ObjectRef string `json:"object_ref"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("expected 2 items, got %d body=%s", len(body.Items), rec.Body.String())
	}
	var gsItem, s3Item struct {
		Ref       string
		ObjectRef string
	}
	for i := range body.Items {
		if body.Items[i].ObjectRef == gsRef {
			gsItem = struct {
				Ref       string
				ObjectRef string
			}{Ref: body.Items[i].Ref, ObjectRef: body.Items[i].ObjectRef}
		}
		if body.Items[i].Ref == s3Ref {
			s3Item = struct {
				Ref       string
				ObjectRef string
			}{Ref: body.Items[i].Ref, ObjectRef: body.Items[i].ObjectRef}
		}
	}
	if gsItem.Ref == "" || !strings.HasPrefix(gsItem.Ref, "https://signed-get.example/") {
		t.Fatalf("gs:// item should resolve to a signed URL, got %+v", gsItem)
	}
	if s3Item.Ref != s3Ref || s3Item.ObjectRef != "" {
		t.Fatalf("s3:// item should stay raw with no object_ref, got %+v", s3Item)
	}
}

// REGRESSION — ADR-185 learner media read path. GET
// /v1/me/courses/{course_id}/content/media-urls must submit ONLY the gs:// ref
// to the course-media resolver; an s3:// ref must NOT be collected.
func TestRegression_MeCourseMedia_ResolvesGSOnlyNotS3(t *testing.T) {
	gsRef := courseMediaItemRef("gs://")
	s3Ref := courseMediaItemRef("s3://")

	// Seed both straight into the store (the s3:// ref is out of contract for
	// the domain AddItem validation).
	repo := inmem.NewCourseContentRepo()
	seedCourseContentItem(t, repo, cc.KindVideo, gsRef, "GS lecture")
	seedCourseContentItem(t, repo, cc.KindVideo, s3Ref, "S3 lecture")
	seedCourseContentItem(t, repo, cc.KindAtom, atomRef, "Intro")

	resolver := &recordingCourseMediaResolver{}
	store := domain.NewInMemEnrollmentStore()
	if _, err := store.Register(context.Background(), tnt, crs, enrolledGCID); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h := meCourseMediaHandler(
		store,
		&CourseContentDeps{Svc: cc.NewService(repo, noopPub{}), MediaResolver: resolver},
		nil,
	)

	rec := do(h, http.MethodGet, mediaURLsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("media-urls: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Exactly one batched call, carrying ONLY the gs:// ref.
	if len(resolver.submittedRefs) != 1 {
		t.Fatalf("resolver calls = %d; want 1 batched call: %v", len(resolver.submittedRefs), resolver.submittedRefs)
	}
	submitted := resolver.submittedRefs[0]
	if len(submitted) != 1 || submitted[0] != gsRef {
		t.Fatalf("submitted refs = %v; want [%s] only — an s3:// course-media ref must NOT be collected", submitted, gsRef)
	}

	// The resolved map carries the gs:// item only.
	var got struct {
		Resolved map[string]string `json:"resolved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Resolved) != 1 {
		t.Fatalf("want exactly 1 resolved (the gs:// video), got %d: %v", len(got.Resolved), got.Resolved)
	}
	for _, signed := range got.Resolved {
		if signed != "https://signed-get.example/"+gsRef {
			t.Fatalf("resolved value should be the signed URL for the gs:// ref, got %q", signed)
		}
	}
}

// The pre-existing behaviour is unchanged: a gs:// course-media ref on its own
// is still collected and resolved (legacy gs:// keeps working everywhere).
func TestRegression_CourseContentRead_GSOnlyItemStillResolves(t *testing.T) {
	gsRef := courseMediaItemRef("gs://")

	svc := cc.NewService(inmem.NewCourseContentRepo(), noopPub{})
	addCourseMediaItem(t, svc, gsRef)

	resolver := &recordingCourseMediaResolver{}
	h := courseContentHandler(&CourseContentDeps{Svc: svc, MediaResolver: resolver})

	rec := do(h, http.MethodGet, "/api/v1/courses/"+crs+"/content", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(resolver.submittedRefs) != 1 || len(resolver.submittedRefs[0]) != 1 || resolver.submittedRefs[0][0] != gsRef {
		t.Fatalf("submitted refs = %v; want [[%s]] — a gs:// course-media ref must still be collected", resolver.submittedRefs, gsRef)
	}
}

// A non-media ref (an atom UUID) is still never collected for course-media
// resolution — the predicate's non-media exclusion is unchanged.
func TestRegression_CourseContentRead_NonMediaRefNeverCollected(t *testing.T) {
	repo := inmem.NewCourseContentRepo()
	seedCourseContentItem(t, repo, cc.KindAtom, atomRef, "Intro")

	resolver := &recordingCourseMediaResolver{}
	h := courseContentHandler(&CourseContentDeps{Svc: cc.NewService(repo, noopPub{}), MediaResolver: resolver})

	rec := do(h, http.MethodGet, "/api/v1/courses/"+crs+"/content", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(resolver.submittedRefs) != 0 {
		t.Fatalf("resolver calls = %d; want 0 — a non-media ref must never be collected: %v", len(resolver.submittedRefs), resolver.submittedRefs)
	}
}
