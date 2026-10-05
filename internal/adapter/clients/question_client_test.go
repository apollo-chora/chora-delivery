package clients

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// jsonUnmarshalString is a tiny helper to keep LEG3-D R5 tests compact.
func jsonUnmarshalString(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// fakeCreationGRPCClient is a test double for CreationServiceGRPCClient. The
// in-memory map is keyed by question_id; missing keys return NotFound.
type fakeCreationGRPCClient struct {
	payloads map[string]*creationv1.SnapshotQuestionByIDResponse
	err      error
	calls    int
	// lastSnapReq captures the wire request (ADR-229 caller_gcid assertions).
	lastSnapReq *creationv1.SnapshotQuestionByIDRequest

	// OT#4 — MintAtomMediaDownloadURL control.
	mintResp    *creationv1.MintAtomMediaDownloadURLResponse
	mintErr     error
	mintCalls   int
	lastMintReq *creationv1.MintAtomMediaDownloadURLRequest
}

func (f *fakeCreationGRPCClient) MintAtomMediaDownloadURL(_ context.Context, in *creationv1.MintAtomMediaDownloadURLRequest, _ ...grpc.CallOption) (*creationv1.MintAtomMediaDownloadURLResponse, error) {
	f.mintCalls++
	f.lastMintReq = in
	if f.mintErr != nil {
		return nil, f.mintErr
	}
	return f.mintResp, nil
}

func (f *fakeCreationGRPCClient) SnapshotQuestionByID(_ context.Context, in *creationv1.SnapshotQuestionByIDRequest, _ ...grpc.CallOption) (*creationv1.SnapshotQuestionByIDResponse, error) {
	f.calls++
	f.lastSnapReq = in
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.payloads[in.QuestionId]
	if !ok {
		return nil, status.Error(codes.NotFound, "question not found in chora_creation")
	}
	return v, nil
}

func TestQuestionClient_SnapshotQuestion_MCQHappyPath(t *testing.T) {
	expected := &creationv1.SnapshotQuestionByIDResponse{
		QuestionId:     "q-1",
		QuestionType:   "mcq",
		Prompt:         "What is 2+2?",
		McqPayloadJson: `{"options":[{"option_id":"opt-4","is_correct":true},{"option_id":"opt-5","is_correct":false}],"scoring":{"mode":"single_correct"}}`,
		SnapshotAt:     timestamppb.New(time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC)),
	}
	fake := &fakeCreationGRPCClient{
		payloads: map[string]*creationv1.SnapshotQuestionByIDResponse{
			"q-1": expected,
		},
	}
	c := NewQuestionClient(fake)
	snap, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-1", "")
	if err != nil {
		t.Fatalf("SnapshotQuestion: %v", err)
	}
	if snap.QuestionType != "mcq" {
		t.Errorf("question_type: want mcq, got %q", snap.QuestionType)
	}
	if snap.PayloadJSON == "" {
		t.Errorf("payload_json: empty")
	}
	if snap.CapturedAt.IsZero() {
		t.Errorf("captured_at: zero")
	}
}

func TestQuestionClient_SnapshotQuestion_OEPicksOEPayload(t *testing.T) {
	fake := &fakeCreationGRPCClient{
		payloads: map[string]*creationv1.SnapshotQuestionByIDResponse{
			"q-oe": {
				QuestionId:    "q-oe",
				QuestionType:  "oe",
				Prompt:        "Explain.",
				OePayloadJson: `{"model_answer":"sample","grader_tier":"T1"}`,
			},
		},
	}
	c := NewQuestionClient(fake)
	snap, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-oe", "")
	if err != nil {
		t.Fatalf("SnapshotQuestion: %v", err)
	}
	if snap.QuestionType != "oe" {
		t.Errorf("question_type: want oe, got %q", snap.QuestionType)
	}
	if snap.PayloadJSON == "" || snap.PayloadJSON[:1] != "{" {
		t.Errorf("payload_json: want oe payload, got %q", snap.PayloadJSON)
	}
}

func TestQuestionClient_SnapshotQuestion_NotFoundMapsToSentinel(t *testing.T) {
	fake := &fakeCreationGRPCClient{payloads: map[string]*creationv1.SnapshotQuestionByIDResponse{}}
	c := NewQuestionClient(fake)
	_, err := c.SnapshotQuestion(context.Background(), "tenant-A", "missing", "")
	if !errors.Is(err, domain.ErrQuestionSnapshotNotFound) {
		t.Errorf("want ErrQuestionSnapshotNotFound, got %v", err)
	}
}

func TestQuestionClient_SnapshotQuestion_TransportErrorFailsLoud(t *testing.T) {
	fake := &fakeCreationGRPCClient{err: status.Error(codes.Unavailable, "connection refused")}
	c := NewQuestionClient(fake)
	_, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-1", "")
	if err == nil {
		t.Fatalf("want error for transport failure, got nil")
	}
	// MUST NOT be the not-found sentinel — transport failures must be
	// distinguishable so callers can retry / circuit-break.
	if errors.Is(err, domain.ErrQuestionSnapshotNotFound) {
		t.Errorf("transport failure must not map to ErrQuestionSnapshotNotFound, got %v", err)
	}
}

// ADR-229 WS-2 (CHO-2133 / CHO-2139) — the SnapshotQuestionByID consent gate
// REFUSES reuse with gRPC codes.PermissionDenied (message begins
// ADR229_REUSE_DENIED). The client MUST translate that into the domain
// ErrQuestionReuseDenied sentinel (→ HTTP 403 terminal), NOT the generic
// retryable wrap (→ 502), and MUST preserve the discriminated upstream message
// so the FE can surface the reason.
func TestQuestionClient_SnapshotQuestion_PermissionDeniedMapsToReuseDenied(t *testing.T) {
	fake := &fakeCreationGRPCClient{err: status.Error(codes.PermissionDenied,
		"ADR229_REUSE_DENIED question q-1 not reusable by caller gcid-actor")}
	c := NewQuestionClient(fake)
	_, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-1", "gcid-actor")
	if !errors.Is(err, domain.ErrQuestionReuseDenied) {
		t.Fatalf("want ErrQuestionReuseDenied, got %v", err)
	}
	// Must NOT collapse into the not-found sentinel (different HTTP status).
	if errors.Is(err, domain.ErrQuestionSnapshotNotFound) {
		t.Errorf("permission-denied must not map to ErrQuestionSnapshotNotFound: %v", err)
	}
	// The discriminated upstream reason must survive for the FE.
	if !strings.Contains(err.Error(), "ADR229_REUSE_DENIED") {
		t.Errorf("want ADR229_REUSE_DENIED preserved in error, got %q", err.Error())
	}
}

// codes.InvalidArgument (e.g. a non-UUID question_id rejected upstream) is a
// terminal client error → the ErrQuestionSnapshotInvalidArgument sentinel (400).
func TestQuestionClient_SnapshotQuestion_InvalidArgumentMapsToSentinel(t *testing.T) {
	fake := &fakeCreationGRPCClient{err: status.Error(codes.InvalidArgument, "question_id must be a uuid")}
	c := NewQuestionClient(fake)
	_, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-1", "")
	if !errors.Is(err, domain.ErrQuestionSnapshotInvalidArgument) {
		t.Fatalf("want ErrQuestionSnapshotInvalidArgument, got %v", err)
	}
}

// codes.FailedPrecondition (a dependency unwired upstream) → the
// ErrQuestionSnapshotPreconditionFailed sentinel (409); the test-set stays DRAFT.
func TestQuestionClient_SnapshotQuestion_FailedPreconditionMapsToSentinel(t *testing.T) {
	fake := &fakeCreationGRPCClient{err: status.Error(codes.FailedPrecondition, "reuse deps unwired")}
	c := NewQuestionClient(fake)
	_, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-1", "")
	if !errors.Is(err, domain.ErrQuestionSnapshotPreconditionFailed) {
		t.Fatalf("want ErrQuestionSnapshotPreconditionFailed, got %v", err)
	}
}

// A transient transport outage (Unavailable) must stay a generic retryable
// error — it must NOT be reclassified as any terminal 4xx sentinel, so the
// publish handler keeps returning 502 and the test-set stays DRAFT for retry.
func TestQuestionClient_SnapshotQuestion_UnavailableStaysGenericRetryable(t *testing.T) {
	fake := &fakeCreationGRPCClient{err: status.Error(codes.Unavailable, "connection refused")}
	c := NewQuestionClient(fake)
	_, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-1", "")
	if err == nil {
		t.Fatalf("want error for transport outage, got nil")
	}
	for _, sentinel := range []error{
		domain.ErrQuestionReuseDenied,
		domain.ErrQuestionSnapshotInvalidArgument,
		domain.ErrQuestionSnapshotPreconditionFailed,
		domain.ErrQuestionSnapshotNotFound,
	} {
		if errors.Is(err, sentinel) {
			t.Errorf("Unavailable must stay generic-retryable, but matched %v", sentinel)
		}
	}
}

func TestQuestionClient_SnapshotQuestion_EmptyPayloadFailsLoud(t *testing.T) {
	fake := &fakeCreationGRPCClient{
		payloads: map[string]*creationv1.SnapshotQuestionByIDResponse{
			"q-empty": {
				QuestionId:   "q-empty",
				QuestionType: "mcq",
				// no payload — server bug
			},
		},
	}
	c := NewQuestionClient(fake)
	_, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-empty", "")
	if err == nil {
		t.Fatalf("want error for empty payload, got nil")
	}
}

func TestQuestionClient_SnapshotQuestion_RejectsEmptyArgs(t *testing.T) {
	c := NewQuestionClient(&fakeCreationGRPCClient{})
	if _, err := c.SnapshotQuestion(context.Background(), "", "q-1", ""); err == nil {
		t.Errorf("want error for empty tenant_id")
	}
	if _, err := c.SnapshotQuestion(context.Background(), "tenant-A", "", ""); err == nil {
		t.Errorf("want error for empty question_id")
	}
}

// LEG3-D R5 fix — chora-creation returns the question stem on the
// top-level proto `Prompt` field (separate from McqPayloadJson). The
// /me/assessments learner-projection reads `stem` from payload_snapshot
// JSON, so the client MUST merge Prompt into the stored payload JSON
// before handing off to the domain. Otherwise the working canvas
// renders with no question stem (cj1-manual-smoke-findings-2026-05-16.md
// §LEG3-D R5 — Snapshot content corruption).
func TestQuestionClient_SnapshotQuestion_MCQ_MergesPromptAsStem(t *testing.T) {
	fake := &fakeCreationGRPCClient{
		payloads: map[string]*creationv1.SnapshotQuestionByIDResponse{
			"q-1": {
				QuestionId:     "q-1",
				QuestionType:   "mcq",
				Prompt:         "What gas do plants release as byproduct of photosynthesis?",
				McqPayloadJson: `{"options":[{"option_id":"opt-A","label":"Oxygen","is_correct":true,"explainer":"yes"}],"scoring":{"mode":"single_correct"}}`,
			},
		},
	}
	c := NewQuestionClient(fake)
	snap, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-1", "")
	if err != nil {
		t.Fatalf("SnapshotQuestion: %v", err)
	}
	var doc map[string]any
	if err := jsonUnmarshalString(snap.PayloadJSON, &doc); err != nil {
		t.Fatalf("payload_json invalid JSON: %v (raw=%q)", err, snap.PayloadJSON)
	}
	stem, _ := doc["stem"].(string)
	if stem != "What gas do plants release as byproduct of photosynthesis?" {
		t.Errorf("payload_json.stem: want prompt verbatim, got %q (full=%q)", stem, snap.PayloadJSON)
	}
	// options[] must survive the merge — grader + projection both read it.
	opts, _ := doc["options"].([]any)
	if len(opts) != 1 {
		t.Errorf("payload_json.options: want 1, got %d (full=%q)", len(opts), snap.PayloadJSON)
	}
}

func TestQuestionClient_SnapshotQuestion_OE_MergesPromptAsStem(t *testing.T) {
	fake := &fakeCreationGRPCClient{
		payloads: map[string]*creationv1.SnapshotQuestionByIDResponse{
			"q-oe": {
				QuestionId:    "q-oe",
				QuestionType:  "oe",
				Prompt:        "Explain in 2-3 sentences how chlorophyll converts light.",
				OePayloadJson: `{"model_answer":"...","grader_tier":"T1"}`,
			},
		},
	}
	c := NewQuestionClient(fake)
	snap, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-oe", "")
	if err != nil {
		t.Fatalf("SnapshotQuestion: %v", err)
	}
	var doc map[string]any
	if err := jsonUnmarshalString(snap.PayloadJSON, &doc); err != nil {
		t.Fatalf("payload_json invalid JSON: %v (raw=%q)", err, snap.PayloadJSON)
	}
	stem, _ := doc["stem"].(string)
	if stem != "Explain in 2-3 sentences how chlorophyll converts light." {
		t.Errorf("payload_json.stem: want prompt verbatim, got %q (full=%q)", stem, snap.PayloadJSON)
	}
	// model_answer must survive — the OE grader needs it.
	if _, ok := doc["model_answer"]; !ok {
		t.Errorf("payload_json: missing model_answer after merge (full=%q)", snap.PayloadJSON)
	}
}

// Guard rail — if chora-creation already returns `stem` in the payload JSON
// (future-proofing if the producer schema changes), the client must NOT
// overwrite it with an empty Prompt. We always prefer the explicit Prompt
// if non-empty (the proto contract), but a missing Prompt + already-set
// stem must round-trip.
func TestQuestionClient_SnapshotQuestion_EmptyPromptPreservesExistingStem(t *testing.T) {
	fake := &fakeCreationGRPCClient{
		payloads: map[string]*creationv1.SnapshotQuestionByIDResponse{
			"q-1": {
				QuestionId:     "q-1",
				QuestionType:   "mcq",
				Prompt:         "", // producer sent empty top-level prompt
				McqPayloadJson: `{"stem":"existing stem","options":[{"option_id":"opt-A","label":"x","is_correct":true,"explainer":"y"}]}`,
			},
		},
	}
	c := NewQuestionClient(fake)
	snap, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-1", "")
	if err != nil {
		t.Fatalf("SnapshotQuestion: %v", err)
	}
	var doc map[string]any
	if err := jsonUnmarshalString(snap.PayloadJSON, &doc); err != nil {
		t.Fatalf("payload_json invalid JSON: %v (raw=%q)", err, snap.PayloadJSON)
	}
	stem, _ := doc["stem"].(string)
	if stem != "existing stem" {
		t.Errorf("payload_json.stem: want existing-stem preserved when Prompt empty, got %q (full=%q)", stem, snap.PayloadJSON)
	}
}

// ADR-229 WS-2 (CHO-2133) — the client threads the publish actor as
// caller_gcid (field 3) so chora-creation's snapshot gate evaluates the
// consent predicate for the REAL caller. Empty caller = legacy migration
// window (creation skips the predicate, loud-logged there).
func TestQuestionClient_SnapshotQuestion_ThreadsCallerGcid(t *testing.T) {
	fake := &fakeCreationGRPCClient{
		payloads: map[string]*creationv1.SnapshotQuestionByIDResponse{
			"q-1": {QuestionId: "q-1", QuestionType: "mcq", McqPayloadJson: `{"stem":"s"}`},
		},
	}
	c := NewQuestionClient(fake)
	if _, err := c.SnapshotQuestion(context.Background(), "tenant-A", "q-1", "gcid-actor-7"); err != nil {
		t.Fatalf("SnapshotQuestion: %v", err)
	}
	if fake.lastSnapReq == nil {
		t.Fatalf("no SnapshotQuestionByID request captured")
	}
	if got := fake.lastSnapReq.GetCallerGcid(); got != "gcid-actor-7" {
		t.Errorf("caller_gcid = %q; want gcid-actor-7", got)
	}
}
