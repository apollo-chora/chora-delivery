// media_resolve_test.go — OT#4 internal TDD for the learner read-time
// gs://-image resolution helpers (resolveLearnerQuestionImages +
// resolveSnapshotImages). Internal test (package httpapi) so it can
// reach the unexported helpers.
package httpapi

import (
	"context"
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

type fakeMediaResolver struct {
	mapping  map[string]string
	calls    int
	lastRefs []string
}

func (f *fakeMediaResolver) ResolveDownloadURLs(_ context.Context, _ string, gsURIs []string) map[string]string {
	f.calls++
	f.lastRefs = gsURIs
	out := map[string]string{}
	for _, u := range gsURIs {
		if s, ok := f.mapping[u]; ok {
			out[u] = s
		}
	}
	return out
}

func strptr(s string) *string { return &s }

// gs:// stem images are rewritten to signed URLs; http URLs pass through.
func TestResolveLearnerQuestionImages_RewritesGSOnly(t *testing.T) {
	resolver := &fakeMediaResolver{mapping: map[string]string{
		"gs://chora-atom-media-dev/a/stem.png": "https://signed/stem",
	}}
	adeps := &AssessmentDeps{MediaResolver: resolver}
	dtos := []map[string]interface{}{
		{"prompt": map[string]interface{}{"image_url": "gs://chora-atom-media-dev/a/stem.png"}},
		{"prompt": map[string]interface{}{"image_url": "https://storage.googleapis.com/transient/x.png?sig=1"}},
		{"prompt": map[string]interface{}{"stem": "no image here"}},
	}
	resolveLearnerQuestionImages(context.Background(), adeps, "t1", dtos)

	if got := dtos[0]["prompt"].(map[string]interface{})["image_url"]; got != "https://signed/stem" {
		t.Errorf("gs:// stem = %v; want signed URL", got)
	}
	if got := dtos[1]["prompt"].(map[string]interface{})["image_url"]; got != "https://storage.googleapis.com/transient/x.png?sig=1" {
		t.Errorf("http url should pass through unchanged; got %v", got)
	}
	if resolver.calls != 1 {
		t.Errorf("resolver calls = %d; want 1 (batched, gs:// only)", resolver.calls)
	}
	if len(resolver.lastRefs) != 1 {
		t.Errorf("batched refs = %d; want 1 (only the gs:// ref)", len(resolver.lastRefs))
	}
}

// nil resolver → no-op, no panic.
func TestResolveLearnerQuestionImages_NilResolver_NoOp(t *testing.T) {
	dtos := []map[string]interface{}{
		{"prompt": map[string]interface{}{"image_url": "gs://chora-atom-media-dev/a/stem.png"}},
	}
	resolveLearnerQuestionImages(context.Background(), &AssessmentDeps{}, "t1", dtos)
	if got := dtos[0]["prompt"].(map[string]interface{})["image_url"]; got != "gs://chora-atom-media-dev/a/stem.png" {
		t.Errorf("nil resolver should leave gs:// raw; got %v", got)
	}
}

// gs:// answer AND stem images on snapshots are rewritten in ONE batch; http
// passes through; nils stay nil (CHO-1638 result reveal — question image).
func TestResolveSnapshotImages_RewritesBothGSOnly(t *testing.T) {
	resolver := &fakeMediaResolver{mapping: map[string]string{
		"gs://chora-atom-media-dev/a/ans.png":  "https://signed/ans",
		"gs://chora-atom-media-dev/a/stem.png": "https://signed/stem",
	}}
	adeps := &AssessmentDeps{MediaResolver: resolver}
	snaps := map[string]domain.MCQSnapshot{
		"q1": {
			AnswerImageURL: strptr("gs://chora-atom-media-dev/a/ans.png"),
			ImageURL:       strptr("gs://chora-atom-media-dev/a/stem.png"),
		},
		"q2": {
			AnswerImageURL: strptr("https://storage.googleapis.com/transient/y.png?sig=1"),
			ImageURL:       strptr("https://storage.googleapis.com/transient/s.png?sig=1"),
		},
		"q3": {},
	}
	resolveSnapshotImages(context.Background(), adeps, "t1", snaps)

	if got := snaps["q1"].AnswerImageURL; got == nil || *got != "https://signed/ans" {
		t.Errorf("q1 answer image = %v; want signed URL", got)
	}
	if got := snaps["q1"].ImageURL; got == nil || *got != "https://signed/stem" {
		t.Errorf("q1 stem image = %v; want signed URL", got)
	}
	if got := snaps["q2"].AnswerImageURL; got == nil || *got != "https://storage.googleapis.com/transient/y.png?sig=1" {
		t.Errorf("q2 answer http should pass through; got %v", got)
	}
	if got := snaps["q2"].ImageURL; got == nil || *got != "https://storage.googleapis.com/transient/s.png?sig=1" {
		t.Errorf("q2 stem http should pass through; got %v", got)
	}
	if snaps["q3"].AnswerImageURL != nil || snaps["q3"].ImageURL != nil {
		t.Errorf("q3 nils should stay nil")
	}
	// One batched RPC carrying BOTH gs:// refs (answer + stem).
	if resolver.calls != 1 || len(resolver.lastRefs) != 2 {
		t.Errorf("expected 1 batched call with 2 gs:// refs (answer+stem); got calls=%d refs=%d", resolver.calls, len(resolver.lastRefs))
	}
}
