package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	mediaadapter "github.com/apollo-chora/chora-delivery/internal/adapter/objectmedia"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
)

// fakeSigner implements CourseMediaSigner for handler tests (no live GCS).
type fakeSigner struct{ max int64 }

func (f fakeSigner) MaxBytes() int64 {
	if f.max > 0 {
		return f.max
	}
	return 100 << 20 // 100 MiB default for tests
}

func (f fakeSigner) SignUpload(_ context.Context, courseID, tenantID string, in mediaadapter.SignUploadInput) (mediaadapter.SignUploadOutput, error) {
	ext, _ := mediaadapter.ExtensionForCourseMIME(in.MIME)
	return mediaadapter.SignUploadOutput{
		UploadURL:    "https://storage.googleapis.com/signed-put?obj=" + courseID,
		ObjectRef:    "gs://chora-delivery-course-media-dev/tenants/" + tenantID + "/courses/" + courseID + "/obj." + ext,
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		MaxSizeBytes: f.MaxBytes(),
	}, nil
}

// fakeResolver implements MediaURLResolver — maps each gs:// ref to a signed URL.
type fakeResolver struct{}

func (fakeResolver) ResolveDownloadURLs(_ context.Context, _ string, gsURIs []string) map[string]string {
	out := make(map[string]string, len(gsURIs))
	for _, u := range gsURIs {
		out[u] = "https://signed-get.example/" + u
	}
	return out
}

func newHandlerWith(deps *CourseContentDeps) http.HandlerFunc {
	deps.Svc = cc.NewService(inmem.NewCourseContentRepo(), noopPub{})
	return courseContentHandler(deps)
}

func TestMintUploadURL_503WhenSignerNil(t *testing.T) {
	h := newHandler() // no signer wired
	rec := do(h, http.MethodPost, "/api/v1/courses/"+crs+"/content/upload-url", `{"mime":"video/mp4","size_bytes":1000}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("signer nil: want 503, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestMintUploadURL_OK(t *testing.T) {
	h := newHandlerWith(&CourseContentDeps{Signer: fakeSigner{}})
	rec := do(h, http.MethodPost, "/api/v1/courses/"+crs+"/content/upload-url", `{"mime":"video/mp4","size_bytes":1048576,"filename":"lecture.mp4"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mint: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		UploadURL    string `json:"upload_url"`
		ObjectRef    string `json:"object_ref"`
		ExpiresAt    string `json:"expires_at"`
		MaxSizeBytes int64  `json:"max_size_bytes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UploadURL == "" || !strings.HasPrefix(got.ObjectRef, "gs://") || got.ExpiresAt == "" || got.MaxSizeBytes == 0 {
		t.Fatalf("envelope incomplete: %+v", got)
	}
}

func TestMintUploadURL_BadBody(t *testing.T) {
	h := newHandlerWith(&CourseContentDeps{Signer: fakeSigner{}})
	base := "/api/v1/courses/" + crs + "/content/upload-url"
	if rec := do(h, http.MethodPost, base, `{"size_bytes":10}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing mime: want 400, got %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, base, `{"mime":"video/mp4","size_bytes":0}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("zero size: want 400, got %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, base, `not-json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: want 400, got %d", rec.Code)
	}
}

func TestMintUploadURL_UnsupportedMime(t *testing.T) {
	h := newHandlerWith(&CourseContentDeps{Signer: fakeSigner{}})
	rec := do(h, http.MethodPost, "/api/v1/courses/"+crs+"/content/upload-url", `{"mime":"audio/mpeg","size_bytes":10}`)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("bad mime: want 415, got %d", rec.Code)
	}
}

func TestMintUploadURL_TooLarge(t *testing.T) {
	h := newHandlerWith(&CourseContentDeps{Signer: fakeSigner{max: 1024}})
	rec := do(h, http.MethodPost, "/api/v1/courses/"+crs+"/content/upload-url", `{"mime":"video/mp4","size_bytes":2048}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: want 413, got %d", rec.Code)
	}
}

func TestMintUploadURL_MethodNotAllowed(t *testing.T) {
	h := newHandlerWith(&CourseContentDeps{Signer: fakeSigner{}})
	rec := do(h, http.MethodGet, "/api/v1/courses/"+crs+"/content/upload-url", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET upload-url: want 405, got %d", rec.Code)
	}
}

func TestAddGsContentItem_AndReadResolves(t *testing.T) {
	h := newHandlerWith(&CourseContentDeps{Signer: fakeSigner{}, MediaResolver: fakeResolver{}})
	base := "/api/v1/courses/" + crs + "/content"
	gsRef := "gs://chora-delivery-course-media-dev/tenants/" + tnt + "/courses/" + crs + "/v.mp4"

	// attach an uploaded video by its gs:// ref
	rec := do(h, http.MethodPost, base, `{"kind":"video","ref":"`+gsRef+`","title":"Uploaded lecture"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add gs:// video: want 201, got %d (%s)", rec.Code, rec.Body.String())
	}

	// read → the gs:// ref is resolved to a signed URL; gs:// preserved as object_ref
	rec = do(h, http.MethodGet, base, "")
	var got struct {
		Items []struct {
			Ref       string `json:"ref"`
			ObjectRef string `json:"object_ref"`
			Kind      string `json:"kind"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Items) != 1 {
		t.Fatalf("list decode: %v items=%d body=%s", err, len(got.Items), rec.Body.String())
	}
	it := got.Items[0]
	if it.ObjectRef != gsRef {
		t.Fatalf("object_ref should preserve the gs:// URI, got %q", it.ObjectRef)
	}
	if !strings.HasPrefix(it.Ref, "https://signed-get.example/") {
		t.Fatalf("ref should be resolved to a signed URL, got %q", it.Ref)
	}
}

func TestReadKeepsRawRefWhenNoResolver(t *testing.T) {
	// No MediaResolver wired → gs:// ref returned raw (no object_ref), no crash.
	h := newHandlerWith(&CourseContentDeps{Signer: fakeSigner{}})
	base := "/api/v1/courses/" + crs + "/content"
	gsRef := "gs://chora-delivery-course-media-dev/tenants/" + tnt + "/courses/" + crs + "/v.mp4"
	_ = do(h, http.MethodPost, base, `{"kind":"video","ref":"`+gsRef+`","title":"v"}`)
	rec := do(h, http.MethodGet, base, "")
	if !strings.Contains(rec.Body.String(), gsRef) || strings.Contains(rec.Body.String(), "object_ref") {
		t.Fatalf("no resolver: expected raw gs:// ref and no object_ref, got %s", rec.Body.String())
	}
}
