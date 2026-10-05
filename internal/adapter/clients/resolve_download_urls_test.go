// resolve_download_urls_test.go — OT#4 TDD for QuestionClient.ResolveDownloadURLs,
// the learner read-time mint-on-read over chora-creation's
// MintAtomMediaDownloadURL gRPC. Reuses fakeCreationGRPCClient from
// question_client_test.go (same package).
package clients

import (
	"context"
	"errors"
	"testing"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"
)

// Happy path — only successfully-minted refs appear; per-URI errors omitted.
func TestResolveDownloadURLs_HappyPath_PerURIIsolation(t *testing.T) {
	fake := &fakeCreationGRPCClient{
		mintResp: &creationv1.MintAtomMediaDownloadURLResponse{
			Urls: []*creationv1.MintedMediaURL{
				{GsUri: "gs://chora-atom-media-dev/a/ok.png", SignedUrl: "https://signed/ok"},
				{GsUri: "gs://evil/bad.png", Error: "foreign bucket"},
			},
		},
	}
	c := NewQuestionClient(fake)
	got := c.ResolveDownloadURLs(context.Background(), "t1", []string{
		"gs://chora-atom-media-dev/a/ok.png",
		"gs://evil/bad.png",
	})
	if len(got) != 1 {
		t.Fatalf("got %d resolved; want 1 (the bad ref omitted): %v", len(got), got)
	}
	if got["gs://chora-atom-media-dev/a/ok.png"] != "https://signed/ok" {
		t.Errorf("ok ref = %q; want https://signed/ok", got["gs://chora-atom-media-dev/a/ok.png"])
	}
	if _, present := got["gs://evil/bad.png"]; present {
		t.Errorf("errored ref should be omitted from the map")
	}
	if fake.mintCalls != 1 {
		t.Errorf("mintCalls = %d; want 1 (batched)", fake.mintCalls)
	}
	if fake.lastMintReq.GetTenantId() != "t1" || len(fake.lastMintReq.GetGsUris()) != 2 {
		t.Errorf("req tenant/uris = %q/%d; want t1/2", fake.lastMintReq.GetTenantId(), len(fake.lastMintReq.GetGsUris()))
	}
}

// Whole-batch transport error → empty map (caller leaves refs raw).
func TestResolveDownloadURLs_BatchError_EmptyMap(t *testing.T) {
	fake := &fakeCreationGRPCClient{mintErr: errors.New("grpc unavailable")}
	c := NewQuestionClient(fake)
	got := c.ResolveDownloadURLs(context.Background(), "t1", []string{"gs://chora-atom-media-dev/a/x.png"})
	if len(got) != 0 {
		t.Errorf("got %d; want 0 on batch error", len(got))
	}
}

// Empty input → no RPC, empty map.
func TestResolveDownloadURLs_EmptyInput_NoRPC(t *testing.T) {
	fake := &fakeCreationGRPCClient{}
	c := NewQuestionClient(fake)
	got := c.ResolveDownloadURLs(context.Background(), "t1", nil)
	if len(got) != 0 {
		t.Errorf("got %d; want 0", len(got))
	}
	if fake.mintCalls != 0 {
		t.Errorf("mintCalls = %d; want 0 (no RPC for empty input)", fake.mintCalls)
	}
}

// Empty tenant → no RPC (guard).
func TestResolveDownloadURLs_NoTenant_NoRPC(t *testing.T) {
	fake := &fakeCreationGRPCClient{}
	c := NewQuestionClient(fake)
	got := c.ResolveDownloadURLs(context.Background(), "", []string{"gs://chora-atom-media-dev/a/x.png"})
	if len(got) != 0 || fake.mintCalls != 0 {
		t.Errorf("expected no resolution + no RPC for empty tenant; got %d resolved, %d calls", len(got), fake.mintCalls)
	}
}
