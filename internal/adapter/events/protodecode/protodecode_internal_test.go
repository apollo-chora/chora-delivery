// protodecode_internal_test.go — in-package coverage for the projector guard
// branches that are unreachable through the public DecodePayloadMap path:
// wrong-typed/nil messages, nil repeated items/files (nil repeated-message
// entries are dropped by proto.Marshal, so a public round-trip cannot contain
// them), and the hydrateFromAttrs nil-map short-circuit.
package protodecode

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
)

func TestProjectQuestionBatchAccepted_WrongTypeIsNoOp(t *testing.T) {
	out := map[string]any{}
	projectQuestionBatchAccepted(nil, out) // nil dynamic value -> !ok guard
	if len(out) != 0 {
		t.Fatalf("nil message must produce no projection, got %v", out)
	}
	// A non-QuestionBatchAccepted proto message (commonv1.EventEnvelope is a
	// real proto.Message) -> the type assertion fails -> no projection.
	projectQuestionBatchAccepted(&commonv1.EventEnvelope{}, out)
	if len(out) != 0 {
		t.Fatalf("wrong-typed message must produce no projection, got %v", out)
	}
}

func TestProjectQuestionBatchAccepted_SkipsNilItemsAndFiles(t *testing.T) {
	msg := &creationv1.QuestionBatchAccepted{
		Items: []*creationv1.QuestionBatchAccepted_Item{
			nil,
			{QuestionAtomId: "atom-1", QuestionId: "q-1", QuestionType: "mcq", Points: 5, DisplayOrder: 1},
		},
		SourceFiles: []*creationv1.QuestionBatchAccepted_SourceFile{
			nil,
			{BlobUri: "gs://b/src.pdf", MimeType: "application/pdf", Role: "source"},
		},
	}
	out := map[string]any{}
	projectQuestionBatchAccepted(msg, out)

	items, _ := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 projected item (nil skipped), got %d", len(items))
	}
	files, _ := out["source_files"].([]any)
	if len(files) != 1 {
		t.Fatalf("expected 1 projected source file (nil skipped), got %d", len(files))
	}
	// Envelope-less + AcceptedAt-less message: envelope-derived keys absent.
	if _, present := out["event_id"]; present {
		t.Fatalf("event_id must be absent without an embedded envelope")
	}
	if _, present := out["accepted_at"]; present {
		t.Fatalf("accepted_at must be absent when unset")
	}
}

func TestProjectQuestionBatchAccepted_ProjectsEnvelopeTimestamps(t *testing.T) {
	at := timestamppb.New(time.Date(2026, 7, 1, 8, 30, 0, 0, time.UTC))
	msg := &creationv1.QuestionBatchAccepted{
		Envelope: &commonv1.EventEnvelope{
			EventId:        "evt-1",
			IdempotencyKey: "idem-1",
			TenantId:       "tenant-1",
			Gcid:           "gcid-1",
			Traceparent:    "00-trace-01",
			Tracestate:     "vendor=test",
			SourceProject:  "chora-creation",
			SourceService:  "chora-creation",
			OccurredAt:     at,
			PublishedAt:    at,
		},
	}
	out := map[string]any{}
	projectQuestionBatchAccepted(msg, out)

	want := at.AsTime().UTC().Format(time.RFC3339Nano)
	if got := out["occurred_at"]; got != want {
		t.Fatalf("occurred_at = %v, want %q", got, want)
	}
	if got := out["published_at"]; got != want {
		t.Fatalf("published_at = %v, want %q", got, want)
	}
	// setIfEmpty: a pre-existing empty-string key is still filled.
	outBlanks := map[string]any{"tenant_id": ""}
	projectQuestionBatchAccepted(msg, outBlanks)
	if outBlanks["tenant_id"] != "tenant-1" {
		t.Fatalf("tenant_id must hydrate into the blank key, got %v", outBlanks["tenant_id"])
	}
}

func TestHydrateFromAttrs_NilMapShortCircuits(t *testing.T) {
	hydrateFromAttrs(nil, map[string]string{"tenant_id": "t"}) // must not panic
}
