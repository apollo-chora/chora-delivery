// Package clients — outbound gRPC clients for chora-delivery.
//
// QuestionClient implements domain/delivery.QuestionSnapshotter by invoking
// chora-creation's `Creation` gRPC service
// (chora-contracts/proto/services/creation/v1/creation.proto), specifically
// the SnapshotQuestionByID RPC. The client lives in the adapter layer per
// hexagonal architecture (the domain never sees gRPC types).
//
// Per ddd-enforcement #3 cross-DB queries are FORBIDDEN — this gRPC is the
// sanctioned mechanism for chora-delivery to obtain the canonical MCQ /
// OE payload at TestSet.Publish() time without joining against
// chora_creation.questions.
//
// Per `feedback_no_inline_config`: the upstream URL is sourced from env
// (`SVC_CREATION_GRPC_URL`, default `chora-creation:8081` in the mesh).
// No URLs are hardcoded in this file.
//
// Per `feedback_agentic_pubsub_only` (durable rule 2026-05-16): the
// Pub/Sub-only mandate applies to LLM-bearing operations. Authoring-time
// deterministic snapshot lookups intentionally use sync gRPC for atomicity
// with the DRAFT → PUBLISHED state transition.
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// CreationServiceGRPCClient is the minimal slice of
// creationv1.CreationClient that chora-delivery calls. Tests inject a fake;
// production wires the real gRPC dial via grpc.NewClient against
// SVC_CREATION_GRPC_URL.
type CreationServiceGRPCClient interface {
	SnapshotQuestionByID(ctx context.Context, in *creationv1.SnapshotQuestionByIDRequest, opts ...grpc.CallOption) (*creationv1.SnapshotQuestionByIDResponse, error)
	MintAtomMediaDownloadURL(ctx context.Context, in *creationv1.MintAtomMediaDownloadURLRequest, opts ...grpc.CallOption) (*creationv1.MintAtomMediaDownloadURLResponse, error)
}

// QuestionClient adapts the chora-creation Creation gRPC to the
// domain.QuestionSnapshotter port.
type QuestionClient struct {
	rpc CreationServiceGRPCClient
}

// NewQuestionClient constructs a QuestionClient from a CreationServiceGRPCClient.
// The caller owns the underlying gRPC conn lifecycle.
func NewQuestionClient(rpc CreationServiceGRPCClient) *QuestionClient {
	return &QuestionClient{rpc: rpc}
}

// SnapshotQuestion fetches the canonical payload for a Question at TestSet.
// Publish() time. It translates the upstream gRPC status into a domain
// sentinel so the domain + HTTP layers never touch gRPC types (hexagonal
// boundary — the same idiom as the existing NOT_FOUND mapping):
//
//   - NOT_FOUND          → domain.ErrQuestionSnapshotNotFound (HTTP 409).
//   - PERMISSION_DENIED   → domain.ErrQuestionReuseDenied — ADR-229 WS-2
//     (CHO-2133) reuse-consent refusal; the message begins
//     `ADR229_REUSE_DENIED`. Terminal (HTTP 403), NOT retryable.
//   - INVALID_ARGUMENT    → domain.ErrQuestionSnapshotInvalidArgument (HTTP 400).
//   - FAILED_PRECONDITION → domain.ErrQuestionSnapshotPreconditionFailed (409).
//   - anything else (Unavailable / Internal / transport) → the wrapped gRPC
//     error — fail loud per `feedback_no_stubs_real_wiring` (HTTP 502,
//     retryable; the test-set stays DRAFT).
//
// The discriminated upstream message is preserved ahead of the 4xx sentinels
// so the FE can surface the refusal reason.
func (c *QuestionClient) SnapshotQuestion(ctx context.Context, tenantID, questionID, callerGCID string) (domain.QuestionPayloadSnapshot, error) {
	if c == nil || c.rpc == nil {
		return domain.QuestionPayloadSnapshot{}, errors.New("question client: rpc client nil")
	}
	if strings.TrimSpace(tenantID) == "" {
		return domain.QuestionPayloadSnapshot{}, errors.New("question client: tenant_id required")
	}
	if strings.TrimSpace(questionID) == "" {
		return domain.QuestionPayloadSnapshot{}, errors.New("question client: question_id required")
	}
	resp, err := c.rpc.SnapshotQuestionByID(ctx, &creationv1.SnapshotQuestionByIDRequest{
		QuestionId: questionID,
		TenantId:   tenantID,
		// ADR-229 WS-2 (CHO-2133) — the publish actor; chora-creation
		// enforces owner ∨ tenant-visible ∨ granted and writes the D2
		// audit grant. Empty = legacy migration window (predicate skipped
		// server-side with a loud log).
		CallerGcid: strings.TrimSpace(callerGCID),
	})
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.NotFound:
				return domain.QuestionPayloadSnapshot{}, domain.ErrQuestionSnapshotNotFound
			case codes.PermissionDenied:
				// ADR-229 WS-2 (CHO-2133): the reuse-consent gate REFUSED the
				// publish actor (message begins `ADR229_REUSE_DENIED`). A
				// terminal client-side refusal (HTTP 403), NOT a retryable
				// outage. Preserve the discriminated upstream reason ahead of
				// the sentinel so the FE can surface it.
				return domain.QuestionPayloadSnapshot{}, fmt.Errorf("%s: %w", st.Message(), domain.ErrQuestionReuseDenied)
			case codes.InvalidArgument:
				// Malformed snapshot request (e.g. a non-UUID question_id) —
				// terminal client error (HTTP 400).
				return domain.QuestionPayloadSnapshot{}, fmt.Errorf("%s: %w", st.Message(), domain.ErrQuestionSnapshotInvalidArgument)
			case codes.FailedPrecondition:
				// Upstream precondition unmet / dependency unwired — terminal
				// for this attempt (HTTP 409; the test-set stays DRAFT).
				return domain.QuestionPayloadSnapshot{}, fmt.Errorf("%s: %w", st.Message(), domain.ErrQuestionSnapshotPreconditionFailed)
			case codes.Unimplemented:
				// Reserved_* question types return UNIMPLEMENTED per the
				// proto. Surface as fail-loud — Phyllis scope only ships
				// MCQ + OE.
				return domain.QuestionPayloadSnapshot{}, fmt.Errorf("question client: question type unsupported (UNIMPLEMENTED): %w", err)
			}
		}
		// codes.Unavailable / codes.Internal / DeadlineExceeded / non-status
		// transport errors — fail loud + retryable (HTTP 502; the test-set
		// stays DRAFT so the caller can retry).
		return domain.QuestionPayloadSnapshot{}, fmt.Errorf("question client: snapshot rpc: %w", err)
	}
	if resp == nil {
		return domain.QuestionPayloadSnapshot{}, errors.New("question client: nil response")
	}
	payloadJSON := strings.TrimSpace(resp.GetMcqPayloadJson())
	if payloadJSON == "" {
		payloadJSON = strings.TrimSpace(resp.GetOePayloadJson())
	}
	if payloadJSON == "" {
		return domain.QuestionPayloadSnapshot{}, fmt.Errorf("question client: empty payload for question %s (type=%s)", questionID, resp.GetQuestionType())
	}
	// LEG3-D R5 fix — merge the proto top-level Prompt into the stored
	// payload JSON as `stem`. chora-delivery's /me/assessments learner
	// projection reads `stem` from payload_snapshot; without this merge the
	// working canvas renders with no question stem. See:
	//   - docs/m13/cj1-manual-smoke-findings-2026-05-16.md §LEG3-D R5
	//   - docs/m13/be-cj1-specifics-2026-05-16.md §LEG3-D R5
	// Honour an existing `stem` field in the producer JSON (future-proof:
	// if chora-creation later emits stem inline, we don't overwrite with a
	// possibly-stale Prompt). Always prefer the JSON-embedded value.
	if prompt := strings.TrimSpace(resp.GetPrompt()); prompt != "" {
		var doc map[string]any
		if err := json.Unmarshal([]byte(payloadJSON), &doc); err == nil && doc != nil {
			if existing, ok := doc["stem"].(string); !ok || strings.TrimSpace(existing) == "" {
				doc["stem"] = prompt
				if merged, mErr := json.Marshal(doc); mErr == nil {
					payloadJSON = string(merged)
				}
			}
		}
	}
	out := domain.QuestionPayloadSnapshot{
		QuestionType: resp.GetQuestionType(),
		PayloadJSON:  payloadJSON,
	}
	if ts := resp.GetSnapshotAt(); ts != nil {
		out.CapturedAt = ts.AsTime()
	}
	return out, nil
}

// ResolveDownloadURLs maps durable atom-media gs:// refs to fresh short-lived
// signed GET URLs via chora-creation's MintAtomMediaDownloadURL RPC (OT#4 —
// learner read-time mint-on-read). Best-effort:
//
//   - returns ONLY the refs that minted successfully (gs:// → signed URL);
//   - a whole-batch transport error yields an empty map (caller leaves the
//     raw gs:// — a fail-VISIBLE broken image, not a crash);
//   - a per-URI error (e.g. foreign bucket) omits that entry.
//
// Non-gs:// inputs should not be passed (the caller filters), but a passed-
// through http URL simply won't appear in the result and is left as-is.
func (c *QuestionClient) ResolveDownloadURLs(ctx context.Context, tenantID string, gsURIs []string) map[string]string {
	out := make(map[string]string, len(gsURIs))
	if c == nil || c.rpc == nil || strings.TrimSpace(tenantID) == "" || len(gsURIs) == 0 {
		return out
	}
	resp, err := c.rpc.MintAtomMediaDownloadURL(ctx, &creationv1.MintAtomMediaDownloadURLRequest{
		TenantId: tenantID,
		GsUris:   gsURIs,
	})
	if err != nil {
		// Whole-batch failure — leave refs raw (fail-visible). Logged by the
		// caller's request-scoped logger; here we degrade, not crash.
		return out
	}
	for _, u := range resp.GetUrls() {
		if u.GetError() == "" && strings.TrimSpace(u.GetSignedUrl()) != "" {
			out[u.GetGsUri()] = u.GetSignedUrl()
		}
	}
	return out
}

// Compile-time port conformance.
var _ domain.QuestionSnapshotter = (*QuestionClient)(nil)
