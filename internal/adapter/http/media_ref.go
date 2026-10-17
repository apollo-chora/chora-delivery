// media_ref.go — the durable atom-media / course-media object-ref grammar
// shared by every read path that swaps a stored object URI for a fresh
// signed GET URL.
//
// chora-creation's atom-media re-home (W8 image re-home, OT#4) moved the
// durable atom-media bucket onto an S3-compatible store (Garage/MinIO), so
// durable refs are now minted as `s3://<bucket>/<key>` — see
// chora-creation internal/adapter/mediastore.AtomMediaURI. The legacy
// `gs://<bucket>/<key>` scheme is still accepted: chora-creation's
// ClassifyReference + TenantOwnsRef classify BOTH schemes as durable on the
// durable bucket, so a snapshot frozen by an earlier deployment keeps
// resolving. Both schemes are therefore "durable media refs" here.
//
// The predicate is deliberately scheme-only, exactly like the gs:// test it
// replaces: it accepts a well-formed `s3://` / `gs://` object URI and
// rejects everything else — http(s) transient URLs, bare UUIDs, display
// names, empty strings. It does NOT check the bucket: the signer behind
// MediaURLResolver owns that gate (a foreign bucket is refused per-URI and
// the ref is left raw, fail-visible).
package httpapi

import "strings"

// durableMediaSchemes are the object-URI schemes that name a durable media
// object (atom-media image OR course-content video/document) and therefore
// must be resolved to a fresh signed GET URL before the FE can render them.
// Order is irrelevant — the check is a prefix match.
var durableMediaSchemes = []string{"s3://", "gs://"}

// isDurableMediaRef reports whether ref is a durable media object URI that
// the read path must hand to MediaURLResolver for a signed GET URL.
//
// True for `s3://<bucket>/<key>` (current atom-media scheme) and
// `gs://<bucket>/<key>` (legacy atom-media + delivery's own course-media
// refs, which keep the gs:// scheme as an opaque cross-service contract).
// False for anything else — http(s) transient URLs, UUIDs, free text, empty
// strings — so a non-media value is never sent to the mint RPC.
func isDurableMediaRef(ref string) bool {
	for _, scheme := range durableMediaSchemes {
		if strings.HasPrefix(ref, scheme) {
			return true
		}
	}
	return false
}
