// Package observability — the otlp.go file adds canonical OpenTelemetry SDK
// wiring on top of the existing request-logging shim (observability.go).
//
// Wave B follow-on (2026-05-14, tracker #153 / ZA paydown). The previous
// chora-delivery skeleton emitted per-request structured logs with W3C
// trace_id + span_id values via the shim, but NO actual OTel spans ever
// reached Cloud Trace. This file closes the gap by delegating to
// libs/chora-go-common/otel which wires the real Cloud Trace exporter
// (ADC auth + TLS via cloudtrace.googleapis.com).
//
// The legacy FromRequest / LogRequest / TraceparentHeader / TraceContext
// surface in observability.go is preserved for the HTTP layer in
// internal/adapter/http/handlers.go; the SDK layer added here is what
// makes spans actually reach Cloud Trace per Tier 3 D13 ("OTLP everywhere").
//
// Paydown II (2026-05-14, tracker #151 / C(a).S1.b follow-on) added the
// async variant InitAsync that returns a bootstrap.OTLPHandle; pod
// bootstrap can now run pgx pool init with the FULL bootstrap deadline
// while OTLP wiring proceeds in its own goroutine + own deadline. See
// libs/chora-go-common/bootstrap/README.md for the migration recipe.
package observability

import (
	"context"

	"github.com/apollo-chora/chora-common/bootstrap"
	commonobs "github.com/apollo-chora/chora-common/observability"
	commonotel "github.com/apollo-chora/chora-common/otel"
)

// ServiceVersion follows semver per OpenInference convention. Matches the
// constant in cmd/server/main.go.
const ServiceVersion = "0.1.0"

// Shutdown is called by main on graceful shutdown to flush in-flight spans.
type Shutdown func(context.Context) error

// Init wires the OTel SDK via the canonical lib and registers a global
// TracerProvider. Returns a shutdown func the caller MUST defer in main.
//
// Public signature matches the Wave-B convention used across all 11 services
// (cf. chora-notifications, chora-governance, chora-identity, chora-creation,
// chora-gateway, chora-consumption).
//
// Prefer InitAsync in new call sites — it decouples OTLP init from pgx
// pool bootstrap per C(a).S1 path (b).
func Init(ctx context.Context) (Shutdown, error) {
	return commonotel.Init(ctx, ServiceName, ServiceVersion)
}

// InitAsync is the fail-soft non-blocking variant of Init per C(a).S1
// path (b) — tracker #151. Delegates to commonobs.InitOTLPAsync so OTLP
// init runs in its own goroutine with its own deadline
// (CHORA_OTLP_INIT_TIMEOUT_SECONDS, default 15s). Timeout / init-error
// degrade to a no-op shutdown so pgx pool + Pub/Sub clients get the FULL
// bootstrap budget.
func InitAsync(ctx context.Context) *bootstrap.OTLPHandle {
	return commonobs.InitOTLPAsync(ctx, ServiceName, ServiceVersion)
}
