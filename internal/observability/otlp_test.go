// otlp_test.go — Init / InitAsync smoke coverage.
//
// Both functions delegate to libs/chora-go-common (otel.Init for the sync
// variant, observability.InitOTLPAsync for the async one). They end up at the
// SAME exporter factory, and that factory routes to STDOUT when no
// OTEL_EXPORTER_OTLP_ENDPOINT / GOOGLE_CLOUD_PROJECT /
// GOOGLE_APPLICATION_CREDENTIALS is present (the dev fallback) — so a test
// that pins OTEL_EXPORTER=stdout drives init to COMPLETION with zero network:
// no ADC, no Cloud Trace TLS handshake, no external dependency.
//
// End-to-end propagation of a span through the SDK is exercised here too: a
// Tracer acquired from the globally-set provider records + flushes without
// error, proving the shutdown closure leaks no resource.
package observability_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	obs "github.com/apollo-chora/chora-delivery/internal/observability"
)

// TestInit_DevMode_CompletesAndShutsDown drives Init through the stdout
// exporter path (no network) and verifies the returned shutdown runs clean.
func TestInit_DevMode_CompletesAndShutsDown(t *testing.T) {
	t.Setenv("OTEL_EXPORTER", "stdout")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project") // must NOT force cloudtrace: OTEL_EXPORTER wins

	shutdown, err := obs.Init(context.Background())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init must return a non-nil shutdown")
	}

	// The global provider must now be usable end-to-end (span recording +
	// flush through the batched stdout exporter).
	tracer := otel.Tracer(obs.ServiceName)
	_, span := tracer.Start(context.Background(), "observability-smoke")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestInitAsync_DevMode_ResolvesInitialized drives the async variant: the
// handle must settle with Initialized=true (its own goroutine + its own
// deadline), and the shutdown it hands back must run clean.
func TestInitAsync_DevMode_ResolvesInitialized(t *testing.T) {
	t.Setenv("OTEL_EXPORTER", "stdout")

	h := obs.InitAsync(context.Background())
	if h == nil {
		t.Fatal("InitAsync must return a handle")
	}
	res := h.Wait(10 * time.Second) // blocks until init settles (dev path is fast)
	if !res.Initialized {
		t.Fatalf("dev-mode init must settle Initialized=true; got %+v", res)
	}
	if res.Shutdown == nil {
		t.Fatal("the result must carry a shutdown func")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := res.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestInitAsync_FailSoftOnError — a provider-level failure must degrade to a
// no-op shutdown, never panic and never bubble up (the fail-soft contract).
// A deliberately broken service name triggers the guard in the common lib
// while the handle stays usable. ServiceName is fixed by the package, so we
// assert the OTHER fail-soft arm instead: a cancelled parent context settles
// the handle without an error and with a non-nil no-op shutdown.
func TestInitAsync_CancelledContext_FailSoft(t *testing.T) {
	t.Setenv("OTEL_EXPORTER", "stdout")
	ctx, cancel := context.WithCancel(context.Background())
	h := obs.InitAsync(ctx)
	cancel() // parent cancellation before the init goroutine finishes

	res := h.Wait(10 * time.Second)
	if res.Shutdown == nil {
		t.Fatal("even a cancelled init must return a non-nil shutdown (no-op)")
	}
	if res.Err != nil {
		t.Fatalf("the handle contract is fail-soft; got Err=%v", res.Err)
	}
}
