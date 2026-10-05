package objectmedia

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/objectstore"
)

const (
	testBucket = "chora-delivery-course-media"
	testTenant = "01970000-0000-7000-8000-0000000000t1"
	testCourse = "01970000-0000-7000-8000-0000000000c1"
)

func testStore(t *testing.T) *objectstore.Store {
	t.Helper()
	store, err := objectstore.New(objectstore.Config{
		Endpoint:     "http://localhost:9000",
		AccessKey:    "chora",
		SecretKey:    "chora",
		Bucket:       testBucket,
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("objectstore.New: %v", err)
	}
	return store
}

func newSigner(t *testing.T, opts ...CourseMediaSignerOption) *CourseMediaSigner {
	t.Helper()
	presign := PresignClientFromConfig(objectstore.Config{
		Endpoint:     "http://localhost:9000",
		AccessKey:    "chora",
		SecretKey:    "chora",
		Bucket:       testBucket,
		UsePathStyle: true,
	})
	base := []CourseMediaSignerOption{WithPresignClient(presign)}
	s, err := NewCourseMediaSignerWithOptions(testStore(t), testBucket, append(base, opts...)...)
	if err != nil {
		t.Fatalf("NewCourseMediaSignerWithOptions: %v", err)
	}
	return s
}

func TestExtensionForCourseMIME(t *testing.T) {
	cases := map[string]string{
		"video/mp4":       "mp4",
		"video/webm":      "webm",
		"video/quicktime": "mov",
		"application/pdf": "pdf",
		"image/jpeg":      "jpg",
		"image/png":       "png",
		"image/webp":      "webp",
	}
	for mime, want := range cases {
		got, err := ExtensionForCourseMIME(mime)
		if err != nil || got != want {
			t.Errorf("ExtensionForCourseMIME(%q) = %q,%v want %q", mime, got, err, want)
		}
	}
	if _, err := ExtensionForCourseMIME("audio/mpeg"); !errors.Is(err, ErrCourseMediaUnsupportedMIME) {
		t.Errorf("unsupported MIME: want ErrCourseMediaUnsupportedMIME, got %v", err)
	}
}

func TestCourseMediaKeyAndURI(t *testing.T) {
	key := CourseMediaKey(testTenant, testCourse, "obj-1", "mp4")
	want := "tenants/" + testTenant + "/courses/" + testCourse + "/obj-1.mp4"
	if key != want {
		t.Errorf("CourseMediaKey = %q want %q", key, want)
	}
	if uri := CourseMediaURI(testBucket, key); uri != "gs://"+testBucket+"/"+key {
		t.Errorf("CourseMediaURI = %q", uri)
	}
}

func TestParseRefRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"https://x/y", "gs://bucket-only", "gs:///nokey.mp4", "gs://bucket/"} {
		if _, _, err := parseGSURI(bad); err == nil {
			t.Errorf("parseGSURI(%q): want error", bad)
		}
	}
	if b, k, err := parseGSURI("gs://bucket/a/b/c.mp4"); err != nil || b != "bucket" || k != "a/b/c.mp4" {
		t.Errorf("parseGSURI valid = %q,%q,%v", b, k, err)
	}
}

func TestNewCourseMediaSigner_RejectsEmptyBucket(t *testing.T) {
	if _, err := NewCourseMediaSignerWithOptions(nil, "   "); !errors.Is(err, ErrCourseMediaSignerNotWired) {
		t.Fatalf("empty bucket: want ErrCourseMediaSignerNotWired, got %v", err)
	}
}

func TestSignUpload_MintsPresignedPutURL(t *testing.T) {
	s := newSigner(t)
	out, err := s.SignUpload(context.Background(), testCourse, testTenant, SignUploadInput{
		MIME: "video/mp4", SizeBytes: 1024,
	})
	if err != nil {
		t.Fatalf("SignUpload: %v", err)
	}
	if !strings.Contains(out.UploadURL, testBucket) {
		t.Errorf("UploadURL missing bucket: %q", out.UploadURL)
	}
	if !strings.HasPrefix(out.ObjectRef, "gs://"+testBucket+"/tenants/"+testTenant+"/courses/"+testCourse+"/") ||
		!strings.HasSuffix(out.ObjectRef, ".mp4") {
		t.Errorf("ObjectRef = %q", out.ObjectRef)
	}
	if out.MaxSizeBytes != DefaultCourseMediaMaxBytes() {
		t.Errorf("MaxSizeBytes = %d", out.MaxSizeBytes)
	}
	if time.Until(out.ExpiresAt) <= 0 {
		t.Errorf("ExpiresAt in the past: %v", out.ExpiresAt)
	}
}

func TestSignUpload_RejectsUnsupportedMIME(t *testing.T) {
	s := newSigner(t)
	if _, err := s.SignUpload(context.Background(), testCourse, testTenant, SignUploadInput{MIME: "audio/mpeg", SizeBytes: 1}); !errors.Is(err, ErrCourseMediaUnsupportedMIME) {
		t.Fatalf("want ErrCourseMediaUnsupportedMIME, got %v", err)
	}
}

func TestSignUpload_RejectsMissingIDs(t *testing.T) {
	s := newSigner(t)
	if _, err := s.SignUpload(context.Background(), "", testTenant, SignUploadInput{MIME: "video/mp4", SizeBytes: 1}); err == nil {
		t.Fatal("missing courseID: want error")
	}
	if _, err := s.SignUpload(context.Background(), testCourse, "", SignUploadInput{MIME: "video/mp4", SizeBytes: 1}); err == nil {
		t.Fatal("missing tenantID: want error")
	}
}

func TestSignUpload_NotWiredWhenNoPresign(t *testing.T) {
	s, err := NewCourseMediaSignerWithOptions(testStore(t), testBucket)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if _, err := s.SignUpload(context.Background(), testCourse, testTenant, SignUploadInput{MIME: "video/mp4", SizeBytes: 1}); !errors.Is(err, ErrCourseMediaSignerNotWired) {
		t.Fatalf("want ErrCourseMediaSignerNotWired, got %v", err)
	}
}

func TestSignDownloadURL_MintsPresignedGetURL(t *testing.T) {
	s := newSigner(t)
	ref := "gs://" + testBucket + "/tenants/" + testTenant + "/courses/" + testCourse + "/obj.mp4"
	url, exp, err := s.SignDownloadURL(context.Background(), ref)
	if err != nil {
		t.Fatalf("SignDownloadURL = %q,%v,%v", url, exp, err)
	}
	if !strings.Contains(url, testBucket) {
		t.Errorf("presigned GET URL missing bucket: %q", url)
	}
	if time.Until(exp) <= 0 {
		t.Errorf("expiry in the past: %v", exp)
	}
}

func TestSignDownloadURL_RefusesForeignBucket(t *testing.T) {
	s := newSigner(t)
	if _, _, err := s.SignDownloadURL(context.Background(), "gs://some-other-bucket/x.mp4"); err == nil {
		t.Fatal("foreign bucket: want error")
	}
}

func TestSignDownloadURL_NonRef(t *testing.T) {
	s := newSigner(t)
	if _, _, err := s.SignDownloadURL(context.Background(), "https://cdn.example.com/x.mp4"); err == nil {
		t.Fatal("non-ref URI: want error")
	}
}

func TestSignDownloadURL_NotWiredWhenNoStore(t *testing.T) {
	presign := PresignClientFromConfig(objectstore.Config{
		Endpoint: "http://localhost:9000", AccessKey: "chora", SecretKey: "chora", UsePathStyle: true,
	})
	s, err := NewCourseMediaSignerWithOptions(nil, testBucket, WithPresignClient(presign))
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	ref := "gs://" + testBucket + "/tenants/t/courses/c/obj.mp4"
	if _, _, err := s.SignDownloadURL(context.Background(), ref); !errors.Is(err, ErrCourseMediaSignerNotWired) {
		t.Fatalf("want ErrCourseMediaSignerNotWired, got %v", err)
	}
}

func TestResolveDownloadURLs_SkipsForeignAndNonRefs(t *testing.T) {
	s := newSigner(t)
	gsRef := "gs://" + testBucket + "/tenants/t/courses/c/v.mp4"
	got := s.ResolveDownloadURLs(context.Background(), testTenant, []string{
		gsRef, "https://cdn.example.com/x.mp4", "gs://other-bucket/x.mp4",
	})
	if _, ok := got[gsRef]; !ok {
		t.Fatalf("expected ref to resolve: %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("only the owned ref should resolve; got %v", got)
	}
}

func TestGettersAndDefaults(t *testing.T) {
	s := newSigner(t)
	if s.Bucket() != testBucket {
		t.Errorf("Bucket = %q", s.Bucket())
	}
	if s.MaxBytes() != DefaultCourseMediaMaxBytes() {
		t.Errorf("MaxBytes = %d", s.MaxBytes())
	}
	if DefaultCourseMediaTTL() != 15*time.Minute {
		t.Errorf("DefaultCourseMediaTTL = %v", DefaultCourseMediaTTL())
	}
	s2, err := NewCourseMediaSignerWithOptions(testStore(t), testBucket, WithMaxBytes(42), WithTTL(time.Minute))
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if s2.MaxBytes() != 42 {
		t.Errorf("WithMaxBytes ignored: %d", s2.MaxBytes())
	}
}
