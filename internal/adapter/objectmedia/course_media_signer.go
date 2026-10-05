// Package objectmedia adapts course-content media signing to an S3-compatible
// object store (chora-common/objectstore — MinIO locally, any S3 endpoint in
// production) for chora-delivery (L3, CHO-1793).
//
// HISTORY: this replaces the former Google Cloud Storage V4 signer. The cloud
// client (the former Google Cloud Storage client) and the IAM SignBlob credential client
// are gone; URLs are now SigV4 presigned against the configured S3 endpoint.
//
// Two operations over the delivery-owned course-media bucket:
//
//   - SignUpload: mints a single-use presigned PUT URL the FE uploads a video /
//     PDF / image to. The FE then attaches the returned durable object ref as a
//     course-content item (kind=video|document).
//   - SignDownloadURL / ResolveDownloadURLs: mints fresh short-lived presigned
//     GET URLs over durable refs so the instructor read-path can preview the
//     uploaded media. Refuses any foreign bucket.
//
// The durable ref scheme is deliberately retained as `gs://` — it is an opaque
// string in the stored content-item model shared with chora-consumption's
// projection, so changing the scheme here would break a cross-service contract.
//
// Wiring: cmd/server/main.go constructs the signer with the bucket from env
// COURSE_MEDIA_BUCKET + the S3 config from objectstore.ConfigFromEnv(). Empty
// bucket → ErrCourseMediaSignerNotWired so main.go leaves the handler's signer
// nil and the route 503s (fail-loud per feedback_no_stubs_real_wiring).
package objectmedia

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/objectstore"
)

// defaultCourseMediaMaxBytes is the per-tenant size cap (2 GiB) — course
// content includes full lecture videos, much larger than the 2 MiB atom-media
// image cap. Override via env COURSE_MEDIA_MAX_BYTES (main.go → WithMaxBytes).
const defaultCourseMediaMaxBytes int64 = 2 * 1024 * 1024 * 1024

// defaultCourseMediaTTL is the presigned URL lifetime (15 min) — the
// secrets-and-env short-lived default; tight so a leaked URL is low-risk.
const defaultCourseMediaTTL = 15 * time.Minute

var (
	// ErrCourseMediaSignerNotWired is returned by the constructor when the
	// bucket is unset, and by SignUpload/SignDownloadURL when the backing
	// client is unconfigured. main.go routes this to a 503.
	ErrCourseMediaSignerNotWired = errors.New("course-media signer not wired")
	// ErrCourseMediaUnsupportedMIME is returned for a MIME outside the enum.
	ErrCourseMediaUnsupportedMIME = errors.New("unsupported course-media mime")
)

// CourseMediaSignerOption configures the signer at construction (functional
// options — keeps the constructor short).
type CourseMediaSignerOption func(*CourseMediaSigner)

// WithPresignClient wires the S3 presign client used to mint upload PUT URLs.
// main.go builds it from objectstore.ConfigFromEnv(); tests inject a stub.
func WithPresignClient(p *s3.PresignClient) CourseMediaSignerOption {
	return func(s *CourseMediaSigner) { s.presign = p }
}

// WithMaxBytes overrides the per-tenant size cap (default 2 GiB). Values <= 0
// are ignored.
func WithMaxBytes(n int64) CourseMediaSignerOption {
	return func(s *CourseMediaSigner) {
		if n > 0 {
			s.maxBytes = n
		}
	}
}

// WithTTL overrides the presigned URL lifetime (default 15 min).
func WithTTL(d time.Duration) CourseMediaSignerOption {
	return func(s *CourseMediaSigner) {
		if d > 0 {
			s.ttl = d
		}
	}
}

// CourseMediaSigner is the S3-backed course-content media signer.
type CourseMediaSigner struct {
	store    *objectstore.Store
	presign  *s3.PresignClient
	bucket   string
	maxBytes int64
	ttl      time.Duration
}

// NewCourseMediaSignerWithOptions constructs the signer. Returns
// ErrCourseMediaSignerNotWired when bucket is empty so main.go can route the
// handler to 503.
func NewCourseMediaSignerWithOptions(store *objectstore.Store, bucket string, opts ...CourseMediaSignerOption) (*CourseMediaSigner, error) {
	if strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("course-media bucket not set: %w", ErrCourseMediaSignerNotWired)
	}
	s := &CourseMediaSigner{
		store:    store,
		bucket:   bucket,
		maxBytes: defaultCourseMediaMaxBytes,
		ttl:      defaultCourseMediaTTL,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// PresignClientFromConfig builds an S3 presign client from an objectstore
// config so the signer can mint presigned PUT URLs (objectstore exposes only
// PresignGet). Returns nil when the config lacks an endpoint or credentials.
func PresignClientFromConfig(cfg objectstore.Config) *s3.PresignClient {
	if strings.TrimSpace(cfg.Endpoint) == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil
	}
	endpoint := cfg.Endpoint
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       region,
		UsePathStyle: cfg.UsePathStyle,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
	})
	return s3.NewPresignClient(client)
}

// Bucket returns the configured object-store bucket name.
func (s *CourseMediaSigner) Bucket() string { return s.bucket }

// MaxBytes returns the per-tenant size cap so the HTTP handler can early-reject
// oversized uploads with 413 before touching the signer.
func (s *CourseMediaSigner) MaxBytes() int64 { return s.maxBytes }

// SignUploadInput is the SignUpload request.
type SignUploadInput struct {
	MIME      string
	SizeBytes int64
	Filename  string // optional; informational only
}

// SignUploadOutput mirrors the OpenAPI ContentUploadUrl schema.
type SignUploadOutput struct {
	UploadURL    string
	ObjectRef    string // canonical durable object ref (gs:// scheme retained)
	ExpiresAt    time.Time
	MaxSizeBytes int64
}

// SignUpload mints a presigned PUT URL for a course-content media object. The
// object key is locked at tenants/{tenant}/courses/{course}/{uuid}.{ext} — the
// tenant prefix scopes auditing, the course prefix groups a course's media,
// and the UUIDv7 object id avoids collision on re-upload.
func (s *CourseMediaSigner) SignUpload(ctx context.Context, courseID, tenantID string, in SignUploadInput) (SignUploadOutput, error) {
	if strings.TrimSpace(courseID) == "" {
		return SignUploadOutput{}, errors.New("course-media: courseID required")
	}
	if strings.TrimSpace(tenantID) == "" {
		return SignUploadOutput{}, errors.New("course-media: tenantID required")
	}
	ext, err := ExtensionForCourseMIME(in.MIME)
	if err != nil {
		return SignUploadOutput{}, fmt.Errorf("course-media: %w", err)
	}
	if s.presign == nil {
		return SignUploadOutput{}, fmt.Errorf("course-media: upload signer not configured: %w", ErrCourseMediaSignerNotWired)
	}

	objectID := uuid.Must(uuid.NewV7()).String()
	key := CourseMediaKey(tenantID, courseID, objectID, ext)
	expiresAt := time.Now().UTC().Add(s.ttl)

	mime := in.MIME
	presigned, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(mime),
	}, func(o *s3.PresignOptions) {
		o.Expires = s.ttl
	})
	if err != nil {
		return SignUploadOutput{}, fmt.Errorf("course-media: presign PUT: %w", err)
	}
	return SignUploadOutput{
		UploadURL:    presigned.URL,
		ObjectRef:    CourseMediaURI(s.bucket, key),
		ExpiresAt:    expiresAt,
		MaxSizeBytes: s.maxBytes,
	}, nil
}

// SignDownloadURL mints a fresh short-lived presigned GET URL over the durable
// object named by ref. Refuses any bucket other than the configured one.
func (s *CourseMediaSigner) SignDownloadURL(ctx context.Context, ref string) (string, time.Time, error) {
	bucket, key, err := parseGSURI(ref)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("course-media: %w", err)
	}
	if bucket != s.bucket {
		return "", time.Time{}, fmt.Errorf(
			"course-media: refusing to sign foreign bucket %q (signer is scoped to %q)", bucket, s.bucket)
	}
	if s.store == nil {
		return "", time.Time{}, fmt.Errorf("course-media: download signer not configured: %w", ErrCourseMediaSignerNotWired)
	}
	expiresAt := time.Now().UTC().Add(s.ttl)
	url, err := s.store.PresignGet(ctx, key, s.ttl)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("course-media: presign GET: %w", err)
	}
	return url, expiresAt, nil
}

// ResolveDownloadURLs best-effort maps each durable ref to a fresh presigned
// GET URL. Non-matching refs and per-ref signing failures are skipped (the
// read-path keeps the raw ref) — matches the MediaURLResolver shape used
// elsewhere in chora-delivery. tenantID is accepted for parity / future
// per-tenant scoping.
func (s *CourseMediaSigner) ResolveDownloadURLs(ctx context.Context, _ string, refs []string) map[string]string {
	out := make(map[string]string, len(refs))
	for _, ref := range refs {
		if !strings.HasPrefix(ref, "gs://") {
			continue
		}
		signed, _, err := s.SignDownloadURL(ctx, ref)
		if err != nil {
			continue
		}
		out[ref] = signed
	}
	return out
}

// -----------------------------------------------------------------------------
// Pure-function helpers (exported for the HTTP handler + tests).
// -----------------------------------------------------------------------------

// ExtensionForCourseMIME maps the supported course-media MIME enum to a
// canonical file extension. Returns an error for anything else so the HTTP
// handler can emit 415. Strict on case + whitespace (the OpenAPI enum is
// lowercase; the handler trims before validating).
func ExtensionForCourseMIME(mime string) (string, error) {
	switch mime {
	case "video/mp4":
		return "mp4", nil
	case "video/webm":
		return "webm", nil
	case "video/quicktime":
		return "mov", nil
	case "application/pdf":
		return "pdf", nil
	case "image/jpeg":
		return "jpg", nil
	case "image/png":
		return "png", nil
	case "image/webp":
		return "webp", nil
	default:
		return "", fmt.Errorf("%w: %q", ErrCourseMediaUnsupportedMIME, mime)
	}
}

// CourseMediaKey returns the canonical object key for a course-media upload:
// tenants/{tenant_id}/courses/{course_id}/{object_id}.{ext}.
func CourseMediaKey(tenantID, courseID, objectID, ext string) string {
	return "tenants/" + tenantID + "/courses/" + courseID + "/" + objectID + "." + ext
}

// CourseMediaURI joins bucket + key into the canonical durable object ref. The
// `gs://` scheme is retained as an opaque cross-service ref (see package doc).
func CourseMediaURI(bucket, key string) string {
	return "gs://" + bucket + "/" + key
}

// DefaultCourseMediaMaxBytes exposes the default size cap (2 GiB).
func DefaultCourseMediaMaxBytes() int64 { return defaultCourseMediaMaxBytes }

// DefaultCourseMediaTTL exposes the presigned-URL lifetime.
func DefaultCourseMediaTTL() time.Duration { return defaultCourseMediaTTL }

// parseGSURI splits the durable ref into bucket + key. Returns an error for a
// non-matching URI, a bucket-only URI, or an empty key.
func parseGSURI(ref string) (bucket, key string, err error) {
	const scheme = "gs://"
	if !strings.HasPrefix(ref, scheme) {
		return "", "", fmt.Errorf("not a %s URI: %q", scheme, ref)
	}
	rest := ref[len(scheme):]
	i := strings.IndexByte(rest, '/')
	if i <= 0 || i >= len(rest)-1 {
		return "", "", fmt.Errorf("malformed %s URI (want %sbucket/key): %q", scheme, scheme, ref)
	}
	return rest[:i], rest[i+1:], nil
}
