// media_ref.go — the durable object-ref grammar shared by the read paths
// that swap a stored media URI for a fresh signed GET URL.
//
// Two media domains live behind MediaURLResolver, and they do NOT share a
// scheme contract:
//
//   - ATOM MEDIA (question images frozen in test_set_questions.
//     payload_snapshot) is produced by chora-creation. Its re-home (W8 image
//     re-home, OT#4) moved the durable atom-media bucket onto an
//     S3-compatible store (Garage/MinIO), so durable refs are now minted as
//     `s3://<bucket>/<key>` — see chora-creation
//     internal/adapter/mediastore.AtomMediaURI. The legacy
//     `gs://<bucket>/<key>` scheme is still accepted there: chora-creation's
//     ClassifyReference + TenantOwnsRef classify BOTH schemes as durable on
//     the durable bucket, so a snapshot frozen by an earlier deployment
//     keeps resolving.
//
//   - COURSE MEDIA (uploaded course videos / PDFs) is delivery-owned. Its
//     scheme contract is deliberately `gs://`-ONLY: the domain validation
//     (internal/domain/course_content/course_content.go validateRef) and the
//     CourseMediaSigner (internal/adapter/objectmedia/course_media_signer.go
//     ResolveDownloadURLs) both recognise `gs://` alone, and the `gs://`
//     scheme is retained as an opaque cross-service ref shared with
//     chora-consumption's projection. An `s3://` course-media ref is not a
//     supported value in that contract, so these paths must NOT collect one
//     for the course-media resolver — doing so would change course-media read
//     behaviour without changing the contract that owns it.
//
// Both predicates are deliberately scheme-only: each accepts its own
// well-formed object URI(s) and rejects everything else — http(s) transient
// URLs, bare UUIDs, display names, empty strings — so a non-media value is
// never sent to a mint RPC. Neither checks the bucket: the signer behind
// MediaURLResolver owns that gate (a foreign bucket is refused per-URI and
// the ref is left raw, fail-visible).
package httpapi

import "strings"

// isDurableAtomMediaRef reports whether ref is a durable atom-media object
// URI the read path must hand to the atom-media resolver
// (clients.QuestionClient → chora-creation's MintAtomMediaDownloadURL) for a
// signed GET URL.
//
// True for `s3://<bucket>/<key>` (the scheme chora-creation's S3 adapter
// now emits) AND `gs://<bucket>/<key>` (legacy atom-media refs, still
// accepted by chora-creation's ClassifyReference / TenantOwnsRef on the
// durable bucket). False for anything else — http(s) transient URLs, UUIDs,
// free text, empty strings — so a non-media value is never sent to the mint
// RPC.
func isDurableAtomMediaRef(ref string) bool {
	return strings.HasPrefix(ref, "s3://") || strings.HasPrefix(ref, "gs://")
}

// isDurableCourseMediaRef reports whether ref is a durable course-media
// object URI the read path must hand to the delivery-owned
// CourseMediaSigner for a signed GET URL.
//
// True for `gs://<bucket>/<key>` ONLY — the delivery-owned course-media
// scheme contract. The domain validation
// (internal/domain/course_content/course_content.go) and the signer
// (internal/adapter/objectmedia/course_media_signer.go) are both
// `gs://`-only; an `s3://` course-content ref is not a supported value there
// and must NOT be collected for course-media resolution. False for anything
// else, including http(s) transient URLs and non-media values.
func isDurableCourseMediaRef(ref string) bool {
	return strings.HasPrefix(ref, "gs://")
}
