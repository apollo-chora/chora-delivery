// Package protodecode is the inbound-side counterpart to protomarshal — it
// decodes Pub/Sub message bytes into a snake_case map[string]any compatible
// with the legacy JSON-shape subscribers.
//
// Why this package exists for chora-delivery
// ------------------------------------------
// ADR-155 Lane B (#55) wires the agentic-dispatch chain: /submit emits
// chora.delivery.grading.oe_batch_requested.v1; the chora-ai-kernel-
// orchestrator (Python LangGraph) invokes the oe_grader Vertex engine + emits
// chora.delivery.grading.oe_batch_completed.v1. chora-delivery subscribes to
// the completion event via a Pub/Sub push handler — this package decodes the
// inbound bytes.
//
// Decode strategy
// ---------------
//  1. Topics registered in binaryDecoders attempt proto.Unmarshal first.
//  2. JSON fallback (json.Unmarshal directly) for unknown topics + transition-
//     window JSON-shape producers.
//  3. Envelope-derived fields (event_id, tenant_id, traceparent, ...) hydrate
//     from Pub/Sub message Attributes when the JSON payload doesn't carry them.
//
// Per ADR-155 the completion event's protobuf bindings are NOT yet generated
// in chora-contracts/gen/go (the proto-flat schema landed but BSR regen is
// rate-limited per protomarshal.go header). So the registry currently holds a
// pending sentinel that routes to the JSON fallback — this is the correct
// transition path until codegen lands.
//
// Mirrors services/chora-consumption/internal/adapter/events/protodecode/.
package protodecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
)

// ErrEmptyPayload — returned when the inbound bytes are empty. Callers must
// surface this to Pub/Sub via a Nack — the broker re-delivers, and persistent
// empty payloads route to DLQ.
var ErrEmptyPayload = errors.New("protodecode: empty payload")

// errUnsupportedYet is an internal sentinel used by entries whose generated
// proto bindings have not landed yet. The caller falls back to JSON.
var errUnsupportedYet = errors.New("protodecode: generated bindings pending")

// binaryDecoder takes raw wire bytes for a topic and returns the unmarshalled
// proto.Message.
type binaryDecoder func(payload []byte) (proto.Message, error)

// projector maps a successfully-unmarshalled proto.Message onto the
// snake_case map[string]any the JSON-decoded handlers consume.
type projector func(msg proto.Message, out map[string]any)

// binaryDecoders is the per-topic registry. Add an entry when a chora-delivery
// peer producer flips a topic to binary AND the gen bindings are imported.
//
// Current entries:
//   - chora.delivery.grading.oe_batch_completed.v1 — bindings pending; routes
//     to JSON fallback. Swap in a real proto.Unmarshal when chora-contracts/
//     gen/go/chora/delivery/v1 lands GradingOeBatchCompleted.
//   - chora.creation.question_batch.accepted.v1 — Lane 1c (CHO-1703 /
//     ADR-180 D10). REAL binary decode: the topic is Schema-Registry-bound
//     BINARY from day one and the QuestionBatchAccepted bindings exist in
//     chora-contracts/gen/go/chora/creation/v1. Per the in_app.created
//     dead-letter lesson an unregistered binary topic silently falls
//     through to JSON and DLQs every delivery.
var binaryDecoders = map[string]struct {
	decode  binaryDecoder
	project projector
}{
	"chora.delivery.grading.oe_batch_completed.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			return nil, errUnsupportedYet
		},
		project: nil,
	},
	"chora.creation.question_batch.accepted.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m creationv1.QuestionBatchAccepted
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectQuestionBatchAccepted,
	},
}

// projectQuestionBatchAccepted maps a QuestionBatchAccepted onto the
// snake_case map the JSON-shape handlers consume. Numeric fields project as
// float64 for parity with the json.Unmarshal fallback path (the downstream
// grInt/grFloat helpers expect JSON-number semantics on BOTH paths).
//
// Envelope-derived keys (event_id / tenant_id / gcid / traceparent / ...)
// project from the EMBEDDED envelope; hydrateFromAttrs later fills only
// missing-or-empty keys, so the payload stays authoritative over Pub/Sub
// attributes.
func projectQuestionBatchAccepted(msg proto.Message, out map[string]any) {
	m, ok := msg.(*creationv1.QuestionBatchAccepted)
	if !ok || m == nil {
		return
	}
	out["job_id"] = m.GetJobId()
	out["host_atom_id"] = m.GetHostAtomId()
	out["tenant_id"] = m.GetTenantId()
	out["author_gcid"] = m.GetAuthorGcid()
	if spec := m.GetTestSet(); spec != nil {
		out["test_set"] = map[string]any{
			"title":       spec.GetTitle(),
			"description": spec.GetDescription(),
		}
	}
	items := make([]any, 0, len(m.GetItems()))
	for _, it := range m.GetItems() {
		if it == nil {
			continue
		}
		items = append(items, map[string]any{
			"question_atom_id": it.GetQuestionAtomId(),
			"question_id":      it.GetQuestionId(),
			"question_type":    it.GetQuestionType(),
			"points":           float64(it.GetPoints()),
			"display_order":    float64(it.GetDisplayOrder()),
		})
	}
	out["items"] = items
	files := make([]any, 0, len(m.GetSourceFiles()))
	for _, f := range m.GetSourceFiles() {
		if f == nil {
			continue
		}
		files = append(files, map[string]any{
			"blob_uri":  f.GetBlobUri(),
			"mime_type": f.GetMimeType(),
			"role":      f.GetRole(),
		})
	}
	out["source_files"] = files
	if at := m.GetAcceptedAt(); at != nil {
		out["accepted_at"] = at.AsTime().UTC().Format(time.RFC3339Nano)
	}
	if env := m.GetEnvelope(); env != nil {
		setIfEmpty(out, "event_id", env.GetEventId())
		setIfEmpty(out, "idempotency_key", env.GetIdempotencyKey())
		setIfEmpty(out, "tenant_id", env.GetTenantId())
		setIfEmpty(out, "gcid", env.GetGcid())
		setIfEmpty(out, "traceparent", env.GetTraceparent())
		setIfEmpty(out, "tracestate", env.GetTracestate())
		setIfEmpty(out, "source_project", env.GetSourceProject())
		setIfEmpty(out, "source_service", env.GetSourceService())
		if at := env.GetOccurredAt(); at != nil {
			setIfEmpty(out, "occurred_at", at.AsTime().UTC().Format(time.RFC3339Nano))
		}
		if at := env.GetPublishedAt(); at != nil {
			setIfEmpty(out, "published_at", at.AsTime().UTC().Format(time.RFC3339Nano))
		}
	}
}

// setIfEmpty writes v at key only when the key is absent or holds "".
// Non-empty event-level fields (e.g. the top-level tenant_id) stay
// authoritative over the embedded-envelope mirror.
func setIfEmpty(out map[string]any, key, v string) {
	if v == "" {
		return
	}
	if existing, present := out[key]; present {
		if s, ok := existing.(string); !ok || s != "" {
			return
		}
	}
	out[key] = v
}

// DecodePayloadMap decodes inbound Pub/Sub message bytes into a snake_case
// map[string]any compatible with the legacy json.Unmarshal flow.
//
// For envelope-field hydration from Pub/Sub message attributes (event_id /
// tenant_id / gcid that the JSON-shape producer does NOT inline into the
// payload bytes), prefer DecodePayloadMapWithAttrs.
func DecodePayloadMap(topic string, payload []byte) (map[string]any, error) {
	return DecodePayloadMapWithAttrs(topic, payload, nil)
}

// DecodePayloadMapWithAttrs is identical to DecodePayloadMap but additionally
// hydrates envelope-derived fields from the supplied Pub/Sub attribute map.
//
// Hydration is non-destructive: if the payload itself carries a non-empty
// value for a key, it wins; the attribute value only fills in missing-or-empty
// keys.
func DecodePayloadMapWithAttrs(topic string, payload []byte, attrs map[string]string) (map[string]any, error) {
	if len(payload) == 0 {
		return nil, ErrEmptyPayload
	}

	var out map[string]any

	if entry, ok := binaryDecoders[topic]; ok {
		msg, err := entry.decode(payload)
		if err == nil && msg != nil && entry.project != nil {
			out = make(map[string]any)
			entry.project(msg, out)
		} else {
			// Binary path failed (transitioning producer or pre-gen topic) —
			// fall through to JSON. One-shot WARN per topic so partial flips
			// stay visible without spamming logs.
			warnBinaryFallback(topic, err)
		}
	} else {
		warnUnknownTopic(topic)
	}

	if out == nil {
		if err := json.Unmarshal(payload, &out); err != nil {
			return nil, fmt.Errorf("protodecode: topic %q neither binary-decodable nor JSON-decodable: %w", topic, err)
		}
		if out == nil {
			out = make(map[string]any)
		}
	}

	hydrateFromAttrs(out, attrs)
	return out, nil
}

// envelopeAttrKeys is the set of Pub/Sub attribute keys whose values are
// envelope-derived and should be merged into the payload map when missing.
//
// `owner_gcid` is an attribute-only synonym for the envelope's `gcid` —
// downstream handlers historically read raw["owner_gcid"] for ownership
// checks.
var envelopeAttrKeys = []string{
	"event_id",
	"idempotency_key",
	"tenant_id",
	"gcid",
	"owner_gcid",
	"traceparent",
	"tracestate",
	"source_project",
	"source_service",
	"occurred_at",
	"published_at",
	"schema_version",
}

// hydrateFromAttrs merges envelope-derived attribute values into out when the
// corresponding key is missing OR present-as-empty-string. Non-string values
// in out are NEVER overwritten.
func hydrateFromAttrs(out map[string]any, attrs map[string]string) {
	if out == nil || len(attrs) == 0 {
		return
	}
	for _, key := range envelopeAttrKeys {
		v, ok := attrs[key]
		if !ok || v == "" {
			continue
		}
		existing, present := out[key]
		if !present {
			out[key] = v
			continue
		}
		if s, ok := existing.(string); ok && s == "" {
			out[key] = v
		}
	}
}

// -----------------------------------------------------------------------------
// One-shot WARN logging
// -----------------------------------------------------------------------------

var (
	warnedFallbackMu sync.Mutex
	warnedFallback   = map[string]bool{}

	warnedUnknownMu sync.Mutex
	warnedUnknown   = map[string]bool{}
)

func warnBinaryFallback(topic string, err error) {
	warnedFallbackMu.Lock()
	defer warnedFallbackMu.Unlock()
	if warnedFallback[topic] {
		return
	}
	warnedFallback[topic] = true
	log.Printf("WARN protodecode: topic %q registered for binary but binary unmarshal failed (%v) — falling back to JSON. Expected during producer-side flip; investigate if persistent.", topic, err)
}

func warnUnknownTopic(topic string) {
	warnedUnknownMu.Lock()
	defer warnedUnknownMu.Unlock()
	if warnedUnknown[topic] {
		return
	}
	warnedUnknown[topic] = true
	log.Printf("WARN protodecode: topic %q has no binary decoder registered — using JSON fallback. Add an entry to internal/adapter/events/protodecode/protodecode.go when the producer flips to binary.", topic)
}
