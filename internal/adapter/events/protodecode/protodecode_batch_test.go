// protodecode_batch_test.go — Lane 1c W4 (CHO-1703 / ADR-180 D10) binary
// round-trip for chora.creation.question_batch.accepted.v1.
//
// TDD RED phase: written FIRST. Unlike the oe_batch_completed sentinel, the
// generated bindings for QuestionBatchAccepted EXIST in chora-contracts
// (gen/go/chora/creation/v1/question_batch.pb.go), so the registry entry
// must do a REAL proto.Unmarshal + snake_case projection — the in_app.created
// dead-letter lesson: a binary-bound topic that silently falls through to
// JSON dead-letters every delivery.
package protodecode_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protodecode"
)

const batchTopic = "chora.creation.question_batch.accepted.v1"

func buildBatchAccepted(t *testing.T) []byte {
	t.Helper()
	msg := &creationv1.QuestionBatchAccepted{
		Envelope: &commonv1.EventEnvelope{
			EventId:        "01985e7f-eeee-7000-8000-000000000001",
			IdempotencyKey: "idem-1",
			TenantId:       "11111111-1111-7111-8111-111111111111",
			Gcid:           "00000000-0000-7000-8000-000000001999",
			Traceparent:    "00-abc-def-01",
			SourceService:  "chora-creation",
		},
		JobId:      "01985e7f-0000-7000-8000-00000000aaaa",
		HostAtomId: "01985e7f-cccc-7000-8000-000000000001",
		TenantId:   "11111111-1111-7111-8111-111111111111",
		AuthorGcid: "00000000-0000-7000-8000-000000001999",
		TestSet: &creationv1.QuestionBatchAccepted_TestSetSpec{
			Title:       "Algebra unit test",
			Description: "Composed from chapter-3.pdf",
		},
		Items: []*creationv1.QuestionBatchAccepted_Item{
			{
				QuestionAtomId: "01985e7f-aaaa-7000-8000-000000000001",
				QuestionId:     "01985e7f-bbbb-7000-8000-000000000001",
				QuestionType:   "mcq",
				Points:         5,
				DisplayOrder:   1,
			},
			{
				QuestionAtomId: "01985e7f-aaaa-7000-8000-000000000002",
				QuestionId:     "01985e7f-bbbb-7000-8000-000000000002",
				QuestionType:   "oe",
				Points:         20,
				DisplayOrder:   2,
			},
		},
		SourceFiles: []*creationv1.QuestionBatchAccepted_SourceFile{
			{BlobUri: "gs://b/src.pdf", MimeType: "application/pdf", Role: "source"},
			{BlobUri: "gs://b/rubric.pdf", MimeType: "application/pdf", Role: "rubric"},
		},
		AcceptedAt: timestamppb.Now(),
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	return raw
}

func TestDecodePayloadMap_QuestionBatchAccepted_Binary(t *testing.T) {
	raw := buildBatchAccepted(t)

	out, err := protodecode.DecodePayloadMap(batchTopic, raw)
	if err != nil {
		t.Fatalf("DecodePayloadMap: %v", err)
	}

	if got, _ := out["job_id"].(string); got != "01985e7f-0000-7000-8000-00000000aaaa" {
		t.Fatalf("job_id: %v", out["job_id"])
	}
	if got, _ := out["host_atom_id"].(string); got != "01985e7f-cccc-7000-8000-000000000001" {
		t.Fatalf("host_atom_id: %v", out["host_atom_id"])
	}
	if got, _ := out["tenant_id"].(string); got != "11111111-1111-7111-8111-111111111111" {
		t.Fatalf("tenant_id: %v", out["tenant_id"])
	}
	if got, _ := out["author_gcid"].(string); got != "00000000-0000-7000-8000-000000001999" {
		t.Fatalf("author_gcid: %v", out["author_gcid"])
	}

	spec, _ := out["test_set"].(map[string]any)
	if spec == nil || spec["title"] != "Algebra unit test" || spec["description"] != "Composed from chapter-3.pdf" {
		t.Fatalf("test_set projection: %v", out["test_set"])
	}

	items, _ := out["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items: %v", out["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["question_atom_id"] != "01985e7f-aaaa-7000-8000-000000000001" ||
		first["question_id"] != "01985e7f-bbbb-7000-8000-000000000001" ||
		first["question_type"] != "mcq" {
		t.Fatalf("item[0]: %v", first)
	}
	// Numeric projection parity with the JSON fallback path: float64.
	if pts, ok := first["points"].(float64); !ok || pts != 5 {
		t.Fatalf("item[0].points must be float64(5); got %T %v", first["points"], first["points"])
	}
	if do, ok := first["display_order"].(float64); !ok || do != 1 {
		t.Fatalf("item[0].display_order must be float64(1); got %T %v", first["display_order"], first["display_order"])
	}

	files, _ := out["source_files"].([]any)
	if len(files) != 2 {
		t.Fatalf("source_files: %v", out["source_files"])
	}
	rubric, _ := files[1].(map[string]any)
	if rubric["role"] != "rubric" || rubric["blob_uri"] != "gs://b/rubric.pdf" {
		t.Fatalf("source_files[1]: %v", rubric)
	}

	// Envelope-derived projection from the EMBEDDED envelope (binary path —
	// no Pub/Sub attributes supplied here).
	if got, _ := out["event_id"].(string); got != "01985e7f-eeee-7000-8000-000000000001" {
		t.Fatalf("event_id from embedded envelope: %v", out["event_id"])
	}
	if got, _ := out["traceparent"].(string); got != "00-abc-def-01" {
		t.Fatalf("traceparent: %v", out["traceparent"])
	}
}

func TestDecodePayloadMap_QuestionBatchAccepted_AttrsDoNotClobber(t *testing.T) {
	raw := buildBatchAccepted(t)
	out, err := protodecode.DecodePayloadMapWithAttrs(batchTopic, raw, map[string]string{
		"tenant_id": "99999999-9999-7999-8999-999999999999", // must NOT win
		"gcid":      "00000000-0000-7000-8000-00000000attr", // fills the gap
	})
	if err != nil {
		t.Fatalf("DecodePayloadMapWithAttrs: %v", err)
	}
	if got, _ := out["tenant_id"].(string); got != "11111111-1111-7111-8111-111111111111" {
		t.Fatalf("payload tenant_id must win over attrs: %v", out["tenant_id"])
	}
	// gcid is not an event-level field on this payload — attr hydration fills it.
	if got, _ := out["gcid"].(string); got == "" {
		t.Fatalf("gcid should hydrate from attrs when payload lacks it: %v", out["gcid"])
	}
}
